package network

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/vpnaas/endpointgroups"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// vpnEndpointGroupTypes are the endpoint types upstream accepts on create.
var vpnEndpointGroupTypes = []string{"subnet", "cidr"}

func newVPNEndpointGroupCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "group", Short: "Manage VPN endpoint groups (requires the vpn-endpoint-groups extension)"}
	cmd.AddCommand(
		newVPNEndpointGroupCreateCommand(a, o),
		newVPNDeleteCommand(a, o, "delete <endpoint-group> [<endpoint-group> ...]", "Delete endpoint group(s)", runVPNEndpointGroupDelete),
		newVPNListCommand(a, o, "List endpoint groups", runVPNEndpointGroupList),
		newVPNEndpointGroupSetCommand(a, o),
		newVPNShowCommand(a, o, "show <endpoint-group>", "Show endpoint group details", runVPNEndpointGroupShow),
	)
	return cmd
}

func resolveVPNEndpointGroupID(ctx context.Context, client *gophercloud.ServiceClient, nameOrID string) (string, error) {
	return resolveByName(client, "endpoint group", nameOrID, func(c *gophercloud.ServiceClient) ([]endpointgroups.EndpointGroup, error) {
		pages, err := endpointgroups.List(c, endpointgroups.ListOpts{Name: nameOrID}).AllPages(ctx)
		if err != nil {
			return nil, err
		}
		return endpointgroups.ExtractEndpointGroups(pages)
	}, func(g endpointgroups.EndpointGroup) string { return g.ID })
}

func vpnEndpointGroupShowFields(g *endpointgroups.EndpointGroup) ([]string, []any) {
	return []string{"Description", "Endpoints", "ID", "Name", "Project", "Type"},
		[]any{g.Description, g.Endpoints, g.ID, g.Name, g.ProjectID, g.Type}
}

func runVPNEndpointGroupList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, long bool, w io.Writer) error {
	pages, err := endpointgroups.List(client, nil).AllPages(ctx)
	if err != nil {
		return explainVPNEndpointGroups(ctx, client, fmt.Errorf("listing endpoint groups: %w", err))
	}
	all, err := endpointgroups.ExtractEndpointGroups(pages)
	if err != nil {
		return fmt.Errorf("parsing endpoint group list: %w", err)
	}
	cols := []string{"ID", "Name", "Type", "Endpoints"}
	if long {
		cols = append(cols, "Description", "Project")
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(all))}
	for _, g := range all {
		row := []any{g.ID, g.Name, g.Type, g.Endpoints}
		if long {
			row = append(row, g.Description, g.ProjectID)
		}
		t.Rows = append(t.Rows, row)
	}
	return o.WriteList(w, t)
}

func runVPNEndpointGroupShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, w io.Writer) (err error) {
	defer func() { err = explainVPNEndpointGroups(ctx, client, err) }()
	id, err := resolveVPNEndpointGroupID(ctx, client, ref)
	if err != nil {
		return err
	}
	g, err := vpnExtracted(endpointgroups.Get(ctx, client, id).Extract())
	if err != nil {
		return fmt.Errorf("getting endpoint group %s: %w", ref, err)
	}
	fields, values := vpnEndpointGroupShowFields(g)
	return o.WriteSingle(w, fields, values)
}

func runVPNEndpointGroupDelete(ctx context.Context, client *gophercloud.ServiceClient, refs []string, w io.Writer) error {
	return explainVPNEndpointGroups(ctx, client, runVPNDelete(ctx, client, "endpoint group", refs, resolveVPNEndpointGroupID,
		func(id string) error { return endpointgroups.Delete(ctx, client, id).ExtractErr() }, w))
}

type vpnEndpointGroupFlags struct {
	name          string
	description   string
	typ           string
	values        []string
	project       string
	projectDomain string
	// projectID is --project resolved by RunE, so the seam needs no identity client.
	projectID string
}

func newVPNEndpointGroupCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &vpnEndpointGroupFlags{}
	cmd := &cobra.Command{
		Use:   "create <name> --type <type> --value <endpoint> [--value <endpoint> ...]",
		Short: "Create an endpoint group",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, session, err := newNetworkSession(ctx, a)
			if err != nil {
				return err
			}
			if f.projectID, err = resolveProjectRef(ctx, session, f.project, f.projectDomain); err != nil {
				return err
			}
			f.name = args[0]
			return runVPNEndpointGroupCreate(ctx, client, o, f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.description, flagDescription, "", "description for the endpoint group")
	fl.StringVar(&f.typ, "type", "", "type of the endpoints in the group (subnet, cidr; required)")
	fl.StringArrayVar(&f.values, "value", nil, "endpoint of the group: a subnet (name or ID) or a CIDR, per --type (repeatable; required)")
	fl.StringVar(&f.project, flagProject, "", "owner's project (name or ID)")
	fl.StringVar(&f.projectDomain, flagProjectDomain, "", projectDomainHelp)
	_ = cmd.MarkFlagRequired("type")
	_ = cmd.MarkFlagRequired("value")
	return cmd
}

// runVPNEndpointGroupCreate mirrors upstream CreateEndpointGroup: subnet
// endpoints are resolved to IDs, CIDRs are sent as given.
func runVPNEndpointGroupCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *vpnEndpointGroupFlags, w io.Writer) (err error) {
	defer func() { err = explainVPNEndpointGroups(ctx, client, err) }()
	typeChoice := []*vpnChoice{{flag: "type", attr: "type", choices: vpnEndpointGroupTypes, value: f.typ}}
	attrs := map[string]any{}
	if err := applyVPNChoices(attrs, typeChoice); err != nil {
		return err
	}
	if f.projectID != "" {
		attrs["project_id"] = f.projectID
	}
	if f.description != "" {
		attrs["description"] = f.description
	}
	if f.name != "" {
		attrs["name"] = f.name
	}
	endpoints := f.values
	if attrs["type"] == "subnet" {
		if endpoints, err = resolveEach(ctx, client, f.values, resolveSubnetID); err != nil {
			return err
		}
	}
	attrs["endpoints"] = endpoints
	g, err := vpnExtracted(endpointgroups.Create(ctx, client, vpnBody{key: "endpoint_group", attrs: attrs}).Extract())
	if err != nil {
		return fmt.Errorf("creating endpoint group: %w", err)
	}
	fields, values := vpnEndpointGroupShowFields(g)
	return o.WriteSingle(w, fields, values)
}

func newVPNEndpointGroupSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &vpnEndpointGroupFlags{}
	cmd := &cobra.Command{
		Use:   "set <endpoint-group>",
		Short: "Set endpoint group properties",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			return runVPNEndpointGroupSet(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.description, flagDescription, "", "description for the endpoint group")
	fl.StringVar(&f.name, "name", "", "new name for the endpoint group")
	return cmd
}

func runVPNEndpointGroupSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, f *vpnEndpointGroupFlags, w io.Writer) (err error) {
	defer func() { err = explainVPNEndpointGroups(ctx, client, err) }()
	attrs := map[string]any{}
	if f.description != "" {
		attrs["description"] = f.description
	}
	if f.name != "" {
		attrs["name"] = f.name
	}
	if len(attrs) == 0 {
		return fmt.Errorf("vpn endpoint group set requires at least one attribute flag")
	}
	id, err := resolveVPNEndpointGroupID(ctx, client, ref)
	if err != nil {
		return err
	}
	g, err := vpnExtracted(endpointgroups.Update(ctx, client, id, vpnBody{key: "endpoint_group", attrs: attrs}).Extract())
	if err != nil {
		return fmt.Errorf("updating endpoint group %s: %w", ref, err)
	}
	fields, values := vpnEndpointGroupShowFields(g)
	return o.WriteSingle(w, fields, values)
}

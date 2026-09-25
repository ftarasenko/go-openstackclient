package network

// VPNaaS (neutron-vpnaas): "vpn service", "vpn ike policy", "vpn ipsec
// policy", "vpn endpoint group" and "vpn ipsec site connection", mirroring
// upstream OSC network/v2/vpnaas/*.py.
//
// Request bodies are built as the attribute map upstream's take_action builds
// (an attribute is sent only when its flag was given) and handed to the typed
// gophercloud calls through vpnBody. The typed opts cannot be used as they
// are: services.CreateOpts always sends "admin_state_up" (null when unset),
// services.UpdateOpts has no subnet_id/flavor_id, and ikepolicies.UpdateOpts
// spells phase1_negotiation_mode "phase_1_negotiation_mode", so neutron would
// reject the one update that sets it. None of the builders carries a revision
// guard, so there is nothing a bodyext adapter would add.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/vpnaas/services"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// newVPNCommands builds the "vpn" noun. Its multi-word children are nested
// parents ("vpn ike policy", "vpn ipsec policy", "vpn ipsec site connection",
// "vpn endpoint group") so cobra resolves upstream's exact command paths.
func newVPNCommands(a *auth.Options, o *output.Options) []*cobra.Command {
	vpn := &cobra.Command{
		Use: "vpn",
		Short: "Manage VPNaaS resources (requires neutron's vpnaas extension; " +
			"endpoint groups also need vpn-endpoint-groups)",
	}
	ike := &cobra.Command{Use: "ike", Short: "Manage IKE policies"}
	ike.AddCommand(newIKEPolicyCommand(a, o))

	site := &cobra.Command{Use: "site", Short: "Manage IPsec site connections"}
	site.AddCommand(newIPsecSiteConnectionCommand(a, o))
	ipsec := &cobra.Command{Use: "ipsec", Short: "Manage IPsec policies and site connections"}
	ipsec.AddCommand(newIPsecPolicyCommand(a, o), site)

	endpoint := &cobra.Command{Use: "endpoint", Short: "Manage VPN endpoint groups"}
	endpoint.AddCommand(newVPNEndpointGroupCommand(a, o))

	vpn.AddCommand(newVPNServiceCommand(a, o), ike, ipsec, endpoint)
	return []*cobra.Command{vpn}
}

// vpnBody is a whole VPNaaS request body: {key: attrs}. It satisfies every
// VPNaaS create/update builder interface, so the typed gophercloud calls send
// exactly the attributes upstream would.
type vpnBody struct {
	key   string
	attrs map[string]any
}

func (b vpnBody) toMap() map[string]any {
	attrs := b.attrs
	if attrs == nil {
		attrs = map[string]any{}
	}
	return map[string]any{b.key: attrs}
}

// ToServiceCreateMap implements services.CreateOptsBuilder.
func (b vpnBody) ToServiceCreateMap() (map[string]any, error) { return b.toMap(), nil }

// ToServiceUpdateMap implements services.UpdateOptsBuilder.
func (b vpnBody) ToServiceUpdateMap() (map[string]any, error) { return b.toMap(), nil }

// ToPolicyCreateMap implements ikepolicies/ipsecpolicies.CreateOptsBuilder.
func (b vpnBody) ToPolicyCreateMap() (map[string]any, error) { return b.toMap(), nil }

// ToPolicyUpdateMap implements ikepolicies/ipsecpolicies.UpdateOptsBuilder.
func (b vpnBody) ToPolicyUpdateMap() (map[string]any, error) { return b.toMap(), nil }

// ToEndpointGroupCreateMap implements endpointgroups.CreateOptsBuilder.
func (b vpnBody) ToEndpointGroupCreateMap() (map[string]any, error) { return b.toMap(), nil }

// ToEndpointGroupUpdateMap implements endpointgroups.UpdateOptsBuilder.
func (b vpnBody) ToEndpointGroupUpdateMap() (map[string]any, error) { return b.toMap(), nil }

// ToConnectionCreateMap implements siteconnections.CreateOptsBuilder.
func (b vpnBody) ToConnectionCreateMap() (map[string]any, error) { return b.toMap(), nil }

// ToConnectionUpdateMap implements siteconnections.UpdateOptsBuilder.
func (b vpnBody) ToConnectionUpdateMap() (map[string]any, error) { return b.toMap(), nil }

// vpnExtracted turns a response that decoded without its resource key into an
// error: gophercloud's Extract returns a nil pointer and no error then.
func vpnExtracted[T any](v *T, err error) (*T, error) {
	if err == nil && v == nil {
		err = errors.New("the response carried no resource")
	}
	return v, err
}

// explainVPNaaS names an undeployed VPNaaS plugin on a 404.
func explainVPNaaS(ctx context.Context, client *gophercloud.ServiceClient, err error) error {
	return explainMissingService(ctx, client, err, extVPNaaS)
}

// explainVPNEndpointGroups is explainVPNaaS for endpoint groups, which also
// need the vpn-endpoint-groups extension: either missing alias is named.
func explainVPNEndpointGroups(ctx context.Context, client *gophercloud.ServiceClient, err error) error {
	return explainMissingService(ctx, client, explainVPNaaS(ctx, client, err), extVPNEndpointGroups)
}

// vpnChoice is a string flag restricted to a fixed set of values. Upstream
// lower-cases the value before checking it (type=_convert_to_lowercase).
type vpnChoice struct {
	flag, attr, help string
	choices          []string
	value            string
}

func bindVPNChoices(cmd *cobra.Command, choices []*vpnChoice) {
	for _, c := range choices {
		cmd.Flags().StringVar(&c.value, c.flag, "", fmt.Sprintf("%s (%s)", c.help, strings.Join(c.choices, ", ")))
	}
}

// applyVPNChoices validates the given choice flags and sets their attributes.
func applyVPNChoices(attrs map[string]any, choices []*vpnChoice) error {
	for _, c := range choices {
		if c.value == "" {
			continue
		}
		v := strings.ToLower(c.value)
		if !slices.Contains(c.choices, v) {
			return fmt.Errorf("--%s %q: must be one of %s", c.flag, c.value, strings.Join(c.choices, ", "))
		}
		attrs[c.attr] = v
	}
	return nil
}

// vpnKeyValues splits the specs of a repeatable "k=v,k=v" flag into ordered
// pairs, later specs overriding earlier ones.
func vpnKeyValues(flag string, specs []string) ([][2]string, error) {
	var pairs [][2]string
	for _, spec := range specs {
		for _, part := range strings.Split(spec, ",") {
			k, v, err := splitKV(part)
			if err != nil {
				return nil, fmt.Errorf("--%s: %w", flag, err)
			}
			pairs = append(pairs, [2]string{k, v})
		}
	}
	return pairs, nil
}

// parseVPNLifetime turns --lifetime units=<units>,value=<value> into the
// lifetime attribute, validated as upstream's validate_lifetime_dict does: the
// only unit is seconds and the value is an integer of at least 60.
func parseVPNLifetime(specs []string) (map[string]any, error) {
	pairs, err := vpnKeyValues("lifetime", specs)
	if err != nil || len(pairs) == 0 {
		return nil, err
	}
	out := map[string]any{}
	for _, p := range pairs {
		switch k, v := p[0], p[1]; k {
		case "units":
			if v != "seconds" {
				return nil, fmt.Errorf("--lifetime units=%q: the only supported unit is seconds", v)
			}
			out[k] = v
		case "value":
			n, err := strconv.Atoi(v)
			if err != nil || n < 60 {
				return nil, fmt.Errorf("--lifetime value=%q: must be an integer of at least 60", v)
			}
			out[k] = n
		default:
			return nil, fmt.Errorf("--lifetime: unknown key %q (valid: units, value)", k)
		}
	}
	return out, nil
}

// vpnDPDActions are upstream's DPD_SUPPORTED_ACTIONS.
var vpnDPDActions = []string{"hold", "clear", "restart", "restart-by-peer", "disabled"}

// parseVPNDPD turns --dpd action=<action>,interval=<interval>,timeout=<timeout>
// into the dpd attribute, validated as upstream's validate_dpd_dict does.
func parseVPNDPD(specs []string) (map[string]any, error) {
	pairs, err := vpnKeyValues("dpd", specs)
	if err != nil || len(pairs) == 0 {
		return nil, err
	}
	out := map[string]any{}
	for _, p := range pairs {
		switch k, v := p[0], p[1]; k {
		case "action":
			if !slices.Contains(vpnDPDActions, v) {
				return nil, fmt.Errorf("--dpd action=%q: must be one of %s", v, strings.Join(vpnDPDActions, ", "))
			}
			out[k] = v
		case "interval", "timeout":
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 {
				return nil, fmt.Errorf("--dpd %s=%q: must be a positive integer", k, v)
			}
			out[k] = n
		default:
			return nil, fmt.Errorf("--dpd: unknown key %q (valid: action, interval, timeout)", k)
		}
	}
	return out, nil
}

// vpnFlavorRef is the part of a neutron flavor the lookup needs.
type vpnFlavorRef struct {
	ID string `json:"id"`
}

// resolveVPNFlavorID resolves a neutron service flavor name or ID. No flavors
// package is vendored, so the name lookup is one raw GET /flavors?name=.
func resolveVPNFlavorID(ctx context.Context, client *gophercloud.ServiceClient, nameOrID string) (string, error) {
	return resolveByName(client, "flavor", nameOrID, func(c *gophercloud.ServiceClient) ([]vpnFlavorRef, error) {
		var body struct {
			Flavors []vpnFlavorRef `json:"flavors"`
		}
		resp, err := c.Get(ctx, c.ServiceURL("flavors")+"?"+url.Values{"name": {nameOrID}}.Encode(), &body, nil)
		if resp != nil {
			defer func() { _ = resp.Body.Close() }()
		}
		if _, _, err = gophercloud.ParseResponse(resp, err); err != nil {
			return nil, err
		}
		return body.Flavors, nil
	}, func(f vpnFlavorRef) string { return f.ID })
}

// vpnEnableDisable is upstream's --enable/--disable pair: admin_state_up is
// sent only when one of them was given.
func vpnEnableDisable(attrs map[string]any, enable, disable bool) {
	switch {
	case enable:
		attrs["admin_state_up"] = true
	case disable:
		attrs["admin_state_up"] = false
	}
}

// newVPNListCommand, newVPNShowCommand and newVPNDeleteCommand build the
// read and delete verbs every VPNaaS noun shares; only the seam differs.
func newVPNListCommand(a *auth.Options, o *output.Options, short string,
	run func(context.Context, *gophercloud.ServiceClient, *output.Options, bool, io.Writer) error,
) *cobra.Command {
	var long bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			return run(ctx, client, o, long, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&long, "long", false, "list additional fields in output")
	return cmd
}

func newVPNShowCommand(a *auth.Options, o *output.Options, use, short string,
	run func(context.Context, *gophercloud.ServiceClient, *output.Options, string, io.Writer) error,
) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
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
			return run(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

func newVPNDeleteCommand(a *auth.Options, o *output.Options, use, short string,
	run func(context.Context, *gophercloud.ServiceClient, []string, io.Writer) error,
) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			return run(ctx, client, args, cmd.OutOrStdout())
		},
	}
}

// runVPNDelete resolves and deletes every ref, attempting all of them.
func runVPNDelete(ctx context.Context, client *gophercloud.ServiceClient, kind string, refs []string,
	resolver func(context.Context, *gophercloud.ServiceClient, string) (string, error),
	del func(id string) error, w io.Writer,
) error {
	return batchdelete.Each(refs, func(ref string) error {
		id, err := resolver(ctx, client, ref)
		if err != nil {
			return err
		}
		if err := del(id); err != nil {
			return fmt.Errorf("deleting %s %s: %w", kind, ref, err)
		}
		_, err = fmt.Fprintf(w, "Deleted %s %s\n", kind, ref)
		return err
	})
}

// ---- vpn service ----------------------------------------------------------

func newVPNServiceCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "service", Short: "Manage VPN services"}
	cmd.AddCommand(
		newVPNServiceCreateCommand(a, o),
		newVPNDeleteCommand(a, o, "delete <vpn-service> [<vpn-service> ...]", "Delete VPN service(s)", runVPNServiceDelete),
		newVPNListCommand(a, o, "List VPN services", runVPNServiceList),
		newVPNServiceSetCommand(a, o),
		newVPNShowCommand(a, o, "show <vpn-service>", "Show VPN service details", runVPNServiceShow),
	)
	return cmd
}

func resolveVPNServiceID(ctx context.Context, client *gophercloud.ServiceClient, nameOrID string) (string, error) {
	return resolveByName(client, "VPN service", nameOrID, func(c *gophercloud.ServiceClient) ([]services.Service, error) {
		pages, err := services.List(c, services.ListOpts{Name: nameOrID}).AllPages(ctx)
		if err != nil {
			return nil, err
		}
		return services.ExtractServices(pages)
	}, func(s services.Service) string { return s.ID })
}

// vpnServiceShowFields renders upstream's show columns, which are the resource
// attributes under their display names, sorted.
func vpnServiceShowFields(s *services.Service) ([]string, []any) {
	return []string{
		"Description", "Ext v4 IP", "Ext v6 IP", "Flavor", "ID", "Name",
		"Project", "Router", "State", "Status", "Subnet",
	}, []any{
		s.Description, s.ExternalV4IP, s.ExternalV6IP, s.FlavorID, s.ID, s.Name,
		s.ProjectID, s.RouterID, s.AdminStateUp, s.Status, s.SubnetID,
	}
}

func runVPNServiceList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, long bool, w io.Writer) error {
	pages, err := services.List(client, nil).AllPages(ctx)
	if err != nil {
		return explainVPNaaS(ctx, client, fmt.Errorf("listing VPN services: %w", err))
	}
	all, err := services.ExtractServices(pages)
	if err != nil {
		return fmt.Errorf("parsing VPN service list: %w", err)
	}
	cols := []string{"ID", "Name", "Router", "Subnet", "Flavor", "State", "Status"}
	if long {
		cols = append(cols, "Description", "Project", "Ext v4 IP", "Ext v6 IP")
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(all))}
	for _, s := range all {
		row := []any{s.ID, s.Name, s.RouterID, s.SubnetID, s.FlavorID, s.AdminStateUp, s.Status}
		if long {
			row = append(row, s.Description, s.ProjectID, s.ExternalV4IP, s.ExternalV6IP)
		}
		t.Rows = append(t.Rows, row)
	}
	return o.WriteList(w, t)
}

func runVPNServiceShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, w io.Writer) (err error) {
	defer func() { err = explainVPNaaS(ctx, client, err) }()
	id, err := resolveVPNServiceID(ctx, client, ref)
	if err != nil {
		return err
	}
	s, err := vpnExtracted(services.Get(ctx, client, id).Extract())
	if err != nil {
		return fmt.Errorf("getting VPN service %s: %w", ref, err)
	}
	fields, values := vpnServiceShowFields(s)
	return o.WriteSingle(w, fields, values)
}

func runVPNServiceDelete(ctx context.Context, client *gophercloud.ServiceClient, refs []string, w io.Writer) error {
	return explainVPNaaS(ctx, client, runVPNDelete(ctx, client, "VPN service", refs, resolveVPNServiceID,
		func(id string) error { return services.Delete(ctx, client, id).ExtractErr() }, w))
}

type vpnServiceFlags struct {
	name          string
	description   string
	subnet        string
	flavor        string
	router        string
	enable        bool
	disable       bool
	project       string
	projectDomain string
	// projectID is --project resolved by RunE, so the seam needs no identity client.
	projectID string
}

// bindVPNServiceCommon registers upstream's _get_common_parser flags.
func bindVPNServiceCommon(cmd *cobra.Command, f *vpnServiceFlags) {
	fl := cmd.Flags()
	fl.StringVar(&f.description, flagDescription, "", "description for the VPN service")
	fl.StringVar(&f.subnet, "subnet", "", "local private subnet (name or ID)")
	fl.StringVar(&f.flavor, "flavor", "", "flavor for the VPN service (name or ID)")
	fl.BoolVar(&f.enable, "enable", false, "enable the VPN service")
	fl.BoolVar(&f.disable, "disable", false, "disable the VPN service")
	cmd.MarkFlagsMutuallyExclusive("enable", "disable")
}

// vpnServiceAttrs is upstream's _get_common_attrs plus --name.
func vpnServiceAttrs(ctx context.Context, client *gophercloud.ServiceClient, f *vpnServiceFlags) (map[string]any, error) {
	attrs := map[string]any{}
	if f.description != "" {
		attrs["description"] = f.description
	}
	if f.subnet != "" {
		id, err := resolveSubnetID(ctx, client, f.subnet)
		if err != nil {
			return nil, err
		}
		attrs["subnet_id"] = id
	}
	if f.flavor != "" {
		id, err := resolveVPNFlavorID(ctx, client, f.flavor)
		if err != nil {
			return nil, err
		}
		attrs["flavor_id"] = id
	}
	vpnEnableDisable(attrs, f.enable, f.disable)
	if f.name != "" {
		attrs["name"] = f.name
	}
	return attrs, nil
}

func newVPNServiceCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &vpnServiceFlags{}
	cmd := &cobra.Command{
		Use:   "create <name> --router <router>",
		Short: "Create a VPN service",
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
			return runVPNServiceCreate(ctx, client, o, f, cmd.OutOrStdout())
		},
	}
	bindVPNServiceCommon(cmd, f)
	fl := cmd.Flags()
	fl.StringVar(&f.router, "router", "", "router for the VPN service (name or ID; required)")
	fl.StringVar(&f.project, flagProject, "", "owner's project (name or ID)")
	fl.StringVar(&f.projectDomain, flagProjectDomain, "", projectDomainHelp)
	_ = cmd.MarkFlagRequired("router")
	return cmd
}

func runVPNServiceCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *vpnServiceFlags, w io.Writer) (err error) {
	defer func() { err = explainVPNaaS(ctx, client, err) }()
	attrs, err := vpnServiceAttrs(ctx, client, f)
	if err != nil {
		return err
	}
	if f.projectID != "" {
		attrs["project_id"] = f.projectID
	}
	routerID, err := resolveRouterID(ctx, client, f.router)
	if err != nil {
		return err
	}
	attrs["router_id"] = routerID
	s, err := vpnExtracted(services.Create(ctx, client, vpnBody{key: "vpnservice", attrs: attrs}).Extract())
	if err != nil {
		return fmt.Errorf("creating VPN service: %w", err)
	}
	fields, values := vpnServiceShowFields(s)
	return o.WriteSingle(w, fields, values)
}

func newVPNServiceSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &vpnServiceFlags{}
	cmd := &cobra.Command{
		Use:   "set <vpn-service>",
		Short: "Set VPN service properties",
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
			return runVPNServiceSet(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	bindVPNServiceCommon(cmd, f)
	cmd.Flags().StringVar(&f.name, "name", "", "new name for the VPN service")
	return cmd
}

func runVPNServiceSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, f *vpnServiceFlags, w io.Writer) (err error) {
	defer func() { err = explainVPNaaS(ctx, client, err) }()
	attrs, err := vpnServiceAttrs(ctx, client, f)
	if err != nil {
		return err
	}
	if len(attrs) == 0 {
		return fmt.Errorf("vpn service set requires at least one attribute flag")
	}
	id, err := resolveVPNServiceID(ctx, client, ref)
	if err != nil {
		return err
	}
	s, err := vpnExtracted(services.Update(ctx, client, id, vpnBody{key: "vpnservice", attrs: attrs}).Extract())
	if err != nil {
		return fmt.Errorf("updating VPN service %s: %w", ref, err)
	}
	fields, values := vpnServiceShowFields(s)
	return o.WriteSingle(w, fields, values)
}

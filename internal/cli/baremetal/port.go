package baremetal

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/baremetal/v1/ports"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// newPortCommand builds "baremetal port ...".
func newPortCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "port",
		Short: "Manage baremetal ports",
	}
	cmd.AddCommand(
		newPortListCommand(a, o),
		newPortShowCommand(a, o),
		newPortCreateCommand(a, o),
		newPortDeleteCommand(a, o),
		newPortSetCommand(a, o),
	)
	return cmd
}

// portListFlags holds the filters accepted by "port list".
//
// Flag names follow upstream OSC (`openstack baremetal port list`). The KeyStack
// command reference at https://docs.keystack.ru/ was not reachable at
// implementation time (HTTP 403), so these are UNVERIFIED against KeyStack and
// fall back to upstream OSC semantics.
type portListFlags struct {
	long    bool
	node    string
	address string
	limit   int
	marker  string
	sortKey string
	sortDir string
}

func newPortListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &portListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List baremetal ports",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runPortList(ctx, client, o, f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&f.long, "long", false, "list additional fields in output")
	fl.StringVar(&f.node, "node", "", "limit to ports of this node (name or UUID)")
	fl.StringVar(&f.address, "address", "", "limit to the port with this MAC address")
	fl.IntVar(&f.limit, "limit", 0, "maximum number of ports to return")
	fl.StringVar(&f.marker, "marker", "", "UUID of the last port from the previous page")
	fl.StringVar(&f.sortKey, "sort-key", "", "sort output by this port attribute")
	fl.StringVar(&f.sortDir, "sort-dir", "", "sort direction: asc or desc")
	return cmd
}

func runPortList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *portListFlags, w io.Writer) error {
	opts := ports.ListOpts{
		Node:    f.node,
		Address: f.address,
		Limit:   f.limit,
		Marker:  f.marker,
		SortKey: f.sortKey,
		SortDir: f.sortDir,
	}
	pages, err := ports.List(client, opts).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing baremetal ports: %w", err)
	}
	all, err := ports.ExtractPorts(pages)
	if err != nil {
		return fmt.Errorf("parsing baremetal port list: %w", err)
	}
	// Limit is only the page size to ironic; enforce it as a hard result cap.
	if f.limit > 0 && len(all) > f.limit {
		all = all[:f.limit]
	}
	return o.WriteList(w, portListTable(all, f.long))
}

func portListTable(list []ports.Port, long bool) output.Table {
	cols := []string{"UUID", "Address"}
	if long {
		cols = append(cols, "Node UUID", "PXE Enabled", "Physical Network", "Portgroup UUID")
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(list))}
	for _, p := range list {
		row := []any{p.UUID, p.Address}
		if long {
			row = append(row, p.NodeUUID, p.PXEEnabled, p.PhysicalNetwork, p.PortGroupUUID)
		}
		t.Rows = append(t.Rows, row)
	}
	return t
}

func portShowFields(p *ports.Port) ([]string, []any) {
	fields := []string{
		"uuid", "address", "node_uuid", "portgroup_uuid", "pxe_enabled",
		"physical_network", "local_link_connection", "is_smartnic", "extra",
		"created_at", "updated_at",
	}
	values := []any{
		p.UUID, p.Address, p.NodeUUID, p.PortGroupUUID, p.PXEEnabled,
		p.PhysicalNetwork, p.LocalLinkConnection, p.IsSmartNIC, p.Extra,
		p.CreatedAt, p.UpdatedAt,
	}
	return fields, values
}

func newPortShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <port>",
		Short: "Show details of a baremetal port",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runPortShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
	return cmd
}

func runPortShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, id string, w io.Writer) error {
	p, err := ports.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("getting baremetal port %s: %w", id, err)
	}
	fields, values := portShowFields(p)
	return o.WriteSingle(w, fields, values)
}

// portCreateFlags holds the attributes accepted by "port create".
type portCreateFlags struct {
	node            string
	address         string
	portGroup       string
	physicalNetwork string
	pxeEnabled      bool
	pxeEnabledSet   bool
	extra           []string
}

func newPortCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &portCreateFlags{}
	cmd := &cobra.Command{
		Use:   "create <address>",
		Short: "Create a new baremetal port",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			f.pxeEnabledSet = cmd.Flags().Changed("pxe-enabled")
			f.address = args[0]
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runPortCreate(ctx, client, o, f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.node, "node", "", "UUID of the node this port belongs to (required)")
	fl.StringVar(&f.portGroup, "port-group", "", "UUID of the portgroup this port belongs to")
	fl.StringVar(&f.physicalNetwork, "physical-network", "", "name of the physical network")
	fl.BoolVar(&f.pxeEnabled, "pxe-enabled", false, "whether PXE is enabled on the port")
	fl.StringArrayVar(&f.extra, "extra", nil, "arbitrary metadata key=value (repeatable)")
	_ = cmd.MarkFlagRequired("node")
	return cmd
}

func runPortCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *portCreateFlags, w io.Writer) error {
	extra, err := parseKeyValMap(f.extra)
	if err != nil {
		return fmt.Errorf("parsing --extra: %w", err)
	}
	opts := ports.CreateOpts{
		NodeUUID:        f.node,
		Address:         f.address,
		PortGroupUUID:   f.portGroup,
		PhysicalNetwork: f.physicalNetwork,
		Extra:           extra,
	}
	if f.pxeEnabledSet {
		opts.PXEEnabled = &f.pxeEnabled
	}
	p, err := ports.Create(ctx, client, opts).Extract()
	if err != nil {
		return fmt.Errorf("creating baremetal port: %w", err)
	}
	fields, values := portShowFields(p)
	return o.WriteSingle(w, fields, values)
}

func newPortDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <port> [<port> ...]",
		Short: "Delete baremetal port(s)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runPortDelete(ctx, client, args, cmd.OutOrStdout())
		},
	}
	return cmd
}

func runPortDelete(ctx context.Context, client *gophercloud.ServiceClient, ids []string, w io.Writer) error {
	for _, id := range ids {
		if err := ports.Delete(ctx, client, id).ExtractErr(); err != nil {
			return fmt.Errorf("deleting baremetal port %s: %w", id, err)
		}
		if _, err := fmt.Fprintf(w, "Deleted port %s\n", id); err != nil {
			return err
		}
	}
	return nil
}

// portSetFlags holds the mutable attributes accepted by "port set".
type portSetFlags struct {
	node            string
	address         string
	physicalNetwork string
	pxeEnabled      bool
	pxeEnabledSet   bool
	extra           []string
}

func newPortSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &portSetFlags{}
	cmd := &cobra.Command{
		Use:   "set <port>",
		Short: "Set baremetal port properties",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			f.pxeEnabledSet = cmd.Flags().Changed("pxe-enabled")
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runPortSet(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.node, "node", "", "set the owning node UUID")
	fl.StringVar(&f.address, "address", "", "set the MAC address")
	fl.StringVar(&f.physicalNetwork, "physical-network", "", "set the physical network name")
	fl.BoolVar(&f.pxeEnabled, "pxe-enabled", false, "set whether PXE is enabled")
	fl.StringArrayVar(&f.extra, "extra", nil, "set an extra metadata key=value (repeatable)")
	return cmd
}

func runPortSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, id string, f *portSetFlags, w io.Writer) error {
	var ops ports.UpdateOpts
	if f.node != "" {
		ops = append(ops, ports.UpdateOperation{Op: ports.ReplaceOp, Path: "/node_uuid", Value: f.node})
	}
	if f.address != "" {
		ops = append(ops, ports.UpdateOperation{Op: ports.ReplaceOp, Path: "/address", Value: f.address})
	}
	if f.physicalNetwork != "" {
		ops = append(ops, ports.UpdateOperation{Op: ports.ReplaceOp, Path: "/physical_network", Value: f.physicalNetwork})
	}
	if f.pxeEnabledSet {
		ops = append(ops, ports.UpdateOperation{Op: ports.ReplaceOp, Path: "/pxe_enabled", Value: f.pxeEnabled})
	}
	for _, p := range f.extra {
		k, v, err := parseKeyVal(p)
		if err != nil {
			return fmt.Errorf("parsing --extra: %w", err)
		}
		ops = append(ops, ports.UpdateOperation{Op: ports.AddOp, Path: "/extra/" + k, Value: v})
	}
	if len(ops) == 0 {
		return fmt.Errorf("port set requires at least one attribute flag")
	}
	p, err := ports.Update(ctx, client, id, ops).Extract()
	if err != nil {
		return fmt.Errorf("updating baremetal port %s: %w", id, err)
	}
	fields, values := portShowFields(p)
	return o.WriteSingle(w, fields, values)
}

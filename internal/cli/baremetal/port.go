package baremetal

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"slices"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/baremetal/v1/ports"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
	"github.com/ftarasenko/go-openstackclient/internal/cli/paging"
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
	long            bool
	node            string
	address         string
	portGroup       string
	physicalNetwork string
	limit           int
	marker          string
	sortKey         string
	sortDir         string
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
	fl.StringVar(&f.portGroup, "port-group", "", "limit to ports of this port group (name or UUID)")
	fl.StringVar(&f.physicalNetwork, "physical-network", "", "limit to ports on this physical network (filtered client-side)")
	fl.IntVar(&f.limit, "limit", 0, "maximum number of ports to return")
	fl.StringVar(&f.marker, "marker", "", "UUID of the last port from the previous page")
	fl.StringVar(&f.sortKey, "sort-key", "", "sort output by this port attribute")
	fl.StringVar(&f.sortDir, "sort-dir", "", "sort direction: asc or desc")
	addListFieldsAliases(cmd, o)
	return cmd
}

func runPortList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *portListFlags, w io.Writer) error {
	opts := portListQuery{
		ListOpts: ports.ListOpts{
			Node:    f.node,
			Address: f.address,
			Marker:  f.marker,
			SortKey: f.sortKey,
			SortDir: f.sortDir,
		},
		portGroup: f.portGroup,
	}
	// The plain list carries only uuid and address; --long, a -c naming
	// another column, or the physical-network filter need /ports/detail.
	fields, list := portFields, ports.List
	if f.long || f.physicalNetwork != "" ||
		len(o.SelectedColumns(labels(portDetailFields)...)) > len(o.SelectedColumns(labels(portFields)...)) {
		fields, list = portDetailFields, ports.ListDetail
	}
	// ironic has no physical_network query parameter, so --limit cannot be
	// handed to the API when filtering on it.
	limit := f.limit
	if f.physicalNetwork != "" {
		limit = 0
	}
	opts.Limit = limit
	all, err := paging.Collect(ctx, list(client, opts), limit, pageMaps("ports"))
	if err != nil {
		return fmt.Errorf("listing baremetal ports: %w", err)
	}
	if f.physicalNetwork != "" {
		all = slices.DeleteFunc(all, func(p map[string]any) bool { return p["physical_network"] != f.physicalNetwork })
		if f.limit > 0 && len(all) > f.limit {
			all = all[:f.limit]
		}
	}
	return o.WriteList(w, listTable(all, fields))
}

// portListQuery adds ironic's portgroup filter, which gophercloud's
// ports.ListOpts does not model.
type portListQuery struct {
	ports.ListOpts
	portGroup string
}

// ToPortListQuery implements ports.ListOptsBuilder.
func (q portListQuery) ToPortListQuery() (string, error) {
	s, err := q.ListOpts.ToPortListQuery()
	return q.withPortGroup(s, err)
}

// ToPortListDetailQuery implements ports.ListOptsBuilder for /ports/detail.
func (q portListQuery) ToPortListDetailQuery() (string, error) {
	s, err := q.ListOpts.ToPortListDetailQuery()
	return q.withPortGroup(s, err)
}

func (q portListQuery) withPortGroup(s string, err error) (string, error) {
	if err != nil || q.portGroup == "" {
		return s, err
	}
	return appendQuery(s, "portgroup="+url.QueryEscape(q.portGroup)), nil
}

// portDetailFields are python-ironicclient's PORT_DETAILED_RESOURCE, the
// columns of `port list --long`; portFields are the short list's.
var (
	portDetailFields = []string{
		"uuid", "address", "created_at", "extra", "node_uuid", "category", "vendor",
		"local_link_connection", "portgroup_uuid", "pxe_enabled", "physical_network",
		"updated_at", "internal_info", "is_smartnic", "name",
	}
	portFields = []string{"uuid", "address"}
)

// portShowKnown adds description (API 1.97), which python-ironicclient's
// detailed set lacks, to the fields `port show` always carries.
var portShowKnown = append(slices.Clone(portDetailFields), "description")

// portShowFields renders a port body as `port show` does upstream.
func portShowFields(body map[string]any) ([]string, []any) {
	return showFields(body, portShowKnown)
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
	p, err := resultMap(ports.Get(ctx, client, id).Result)
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
	name            string
	portGroup       string
	physicalNetwork string
	pxeEnabled      bool
	pxeEnabledSet   bool
	extra           []string
}

// portNameOpts adds ironic's port `name` to a create body. gophercloud's
// ports.CreateOpts has no such field — the attribute arrived in API 1.88, after
// the package was written — so the builder interface is implemented here rather
// than hand-rolling the whole request.
type portNameOpts struct {
	ports.CreateOpts
	Name string
}

func (o portNameOpts) ToPortCreateMap() (map[string]any, error) {
	b, err := o.CreateOpts.ToPortCreateMap()
	if err != nil {
		return nil, err
	}
	if o.Name != "" {
		b["name"] = o.Name
	}
	return b, nil
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
			f.pxeEnabledSet = cmd.Flags().Changed(flagPXEEnabled)
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
	fl.StringVar(&f.name, "name", "", "name for the port (requires ironic API 1.88, OpenStack 2024.1)")
	fl.StringVar(&f.portGroup, "port-group", "", "UUID of the portgroup this port belongs to")
	fl.StringVar(&f.physicalNetwork, "physical-network", "", "name of the physical network")
	fl.BoolVar(&f.pxeEnabled, flagPXEEnabled, false, "whether PXE is enabled on the port")
	fl.StringArrayVar(&f.extra, "extra", nil, "arbitrary metadata key=value (repeatable)")
	_ = cmd.MarkFlagRequired("node")
	return cmd
}

// portColumns are the columns a port write verb renders.
var portColumns = knownColumns(portShowKnown)

func runPortCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *portCreateFlags, w io.Writer) error {
	if err := o.CheckColumns(portColumns...); err != nil {
		return err
	}
	extra, err := parseKeyValMap(f.extra)
	if err != nil {
		return fmt.Errorf("parsing --extra: %w", err)
	}
	opts := portNameOpts{
		CreateOpts: ports.CreateOpts{
			NodeUUID:        f.node,
			Address:         f.address,
			PortGroupUUID:   f.portGroup,
			PhysicalNetwork: f.physicalNetwork,
			Extra:           extra,
		},
		Name: f.name,
	}
	if f.pxeEnabledSet {
		opts.PXEEnabled = &f.pxeEnabled
	}
	p, err := resultMap(ports.Create(ctx, client, opts).Result)
	if err != nil {
		if f.name != "" {
			// A port name is rejected as an unknown attribute below ironic 1.88,
			// which surfaces as a bare 400 about the body rather than the version.
			return fmt.Errorf("creating baremetal port: %w", explainMicroversion(ctx, client, featurePortName, err))
		}
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
	// Attempt every id (as OSC does) rather than aborting on the first failure;
	// batchdelete.Each reports the successes and joins the failures into a
	// single error.
	return batchdelete.Each(ids, func(id string) error {
		if err := ports.Delete(ctx, client, id).ExtractErr(); err != nil {
			return fmt.Errorf("deleting baremetal port %s: %w", id, err)
		}
		if _, err := fmt.Fprintf(w, "Deleted port %s\n", id); err != nil {
			return err
		}
		return nil
	})
}

// portSetFlags holds the mutable attributes accepted by "port set".
type portSetFlags struct {
	node            string
	address         string
	name            string
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
			f.pxeEnabledSet = cmd.Flags().Changed(flagPXEEnabled)
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
	fl.StringVar(&f.name, "name", "", "set the port name (requires ironic API 1.88, OpenStack 2024.1)")
	fl.StringVar(&f.physicalNetwork, "physical-network", "", "set the physical network name")
	fl.BoolVar(&f.pxeEnabled, flagPXEEnabled, false, "set whether PXE is enabled")
	fl.StringArrayVar(&f.extra, "extra", nil, "set an extra metadata key=value (repeatable)")
	return cmd
}

func runPortSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, id string, f *portSetFlags, w io.Writer) error {
	if err := o.CheckColumns(portColumns...); err != nil {
		return err
	}
	var ops ports.UpdateOpts
	if f.node != "" {
		ops = append(ops, ports.UpdateOperation{Op: ports.ReplaceOp, Path: "/node_uuid", Value: f.node})
	}
	if f.address != "" {
		ops = append(ops, ports.UpdateOperation{Op: ports.ReplaceOp, Path: "/address", Value: f.address})
	}
	if f.name != "" {
		ops = append(ops, ports.UpdateOperation{Op: ports.ReplaceOp, Path: "/name", Value: f.name})
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
		ops = append(ops, ports.UpdateOperation{Op: ports.AddOp, Path: "/extra/" + escapeJSONPointer(k), Value: v})
	}
	if len(ops) == 0 {
		return fmt.Errorf("port set requires at least one attribute flag")
	}
	p, err := resultMap(ports.Update(ctx, client, id, ops).Result)
	if err != nil {
		if f.name != "" {
			return fmt.Errorf("updating baremetal port %s: %w", id,
				explainMicroversion(ctx, client, featurePortName, err))
		}
		return fmt.Errorf("updating baremetal port %s: %w", id, err)
	}
	fields, values := portShowFields(p)
	return o.WriteSingle(w, fields, values)
}

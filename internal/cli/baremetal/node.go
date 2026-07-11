package baremetal

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/baremetal/v1/nodes"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// newNodeCommand builds "baremetal node ...".
func newNodeCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "node",
		Short: "Manage baremetal nodes",
	}
	cmd.AddCommand(newNodeListCommand(a, o))
	return cmd
}

// nodeListFlags holds the filters/pagination accepted by "node list".
//
// Flag names follow upstream OSC (`openstack baremetal node list`). The
// KeyStack command reference at https://docs.keystack.ru/ was not reachable at
// implementation time (HTTP 403), so these are UNVERIFIED against KeyStack and
// fall back to upstream OSC semantics — see the PR description.
type nodeListFlags struct {
	long           bool
	limit          int
	marker         string
	maintenance    bool
	maintenanceSet bool
	associated     bool
	associatedSet  bool
	provisionState string
	driver         string
	resourceClass  string
	sortKey        string
	sortDir        string
}

func newNodeListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &nodeListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List baremetal nodes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			f.maintenanceSet = cmd.Flags().Changed("maintenance")
			f.associatedSet = cmd.Flags().Changed("associated")

			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runNodeList(ctx, client, o, f, cmd.OutOrStdout())
		},
	}

	fl := cmd.Flags()
	fl.BoolVar(&f.long, "long", false, "list additional fields in output")
	fl.IntVar(&f.limit, "limit", 0, "maximum number of nodes to return")
	fl.StringVar(&f.marker, "marker", "", "UUID of the last node from the previous page")
	fl.BoolVar(&f.maintenance, "maintenance", false, "limit to nodes in maintenance mode (use --maintenance=false to invert)")
	fl.BoolVar(&f.associated, "associated", false, "limit to nodes associated with an instance (use --associated=false to invert)")
	fl.StringVar(&f.provisionState, "provision-state", "", "limit to nodes in this provision state")
	fl.StringVar(&f.driver, "driver", "", "limit to nodes using this driver")
	fl.StringVar(&f.resourceClass, "resource-class", "", "limit to nodes with this resource class")
	fl.StringVar(&f.sortKey, "sort-key", "", "sort output by this node attribute")
	fl.StringVar(&f.sortDir, "sort-dir", "", "sort direction: asc or desc")
	return cmd
}

// runNodeList performs the list and renders it. It takes an already-built
// service client so it can be exercised directly against a mock endpoint in
// tests.
func runNodeList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *nodeListFlags, w io.Writer) error {
	opts := nodes.ListOpts{
		Limit:          f.limit,
		Marker:         f.marker,
		ProvisionState: nodes.ProvisionState(f.provisionState),
		Driver:         f.driver,
		ResourceClass:  f.resourceClass,
		SortKey:        f.sortKey,
		SortDir:        f.sortDir,
	}
	if f.maintenanceSet {
		opts.Maintenance = f.maintenance
	}
	if f.associatedSet {
		opts.Associated = f.associated
	}

	pages, err := nodes.List(client, opts).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing baremetal nodes: %w", err)
	}
	all, err := nodes.ExtractNodes(pages)
	if err != nil {
		return fmt.Errorf("parsing baremetal node list: %w", err)
	}

	return o.WriteList(w, nodeListTable(all, f.long))
}

// nodeListTable builds the output table. The default column set matches
// `openstack baremetal node list`; --long adds the operationally useful extras.
func nodeListTable(list []nodes.Node, long bool) output.Table {
	cols := []string{"UUID", "Name", "Instance UUID", "Power State", "Provisioning State", "Maintenance"}
	if long {
		cols = append(cols, "Driver", "Resource Class", "Target Provision State", "Last Error")
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(list))}
	for _, n := range list {
		row := []any{n.UUID, n.Name, n.InstanceUUID, n.PowerState, n.ProvisionState, n.Maintenance}
		if long {
			row = append(row, n.Driver, n.ResourceClass, n.TargetProvisionState, n.LastError)
		}
		t.Rows = append(t.Rows, row)
	}
	return t
}

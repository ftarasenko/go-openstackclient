package baremetal

import (
	"context"
	"fmt"
	"io"
	"strconv"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/baremetal/v1/nodes"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/paging"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// newNodeCommand builds "baremetal node ...".
func newNodeCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "node",
		Short: "Manage baremetal nodes",
	}
	cmd.AddCommand(newNodeListCommand(a, o))
	cmd.AddCommand(newNodeShowCommand(a, o))
	cmd.AddCommand(newNodeCreateCommand(a, o))
	cmd.AddCommand(newNodeDeleteCommand(a, o))
	cmd.AddCommand(newNodeSetCommand(a, o))
	cmd.AddCommand(newNodeUnsetCommand(a, o))
	cmd.AddCommand(newNodeMaintenanceCommand(a, o))
	cmd.AddCommand(newNodePowerCommand(a, o))
	cmd.AddCommand(newNodeBootDeviceCommand(a, o))
	cmd.AddCommand(newNodeInventoryCommand(a, o))
	cmd.AddCommand(newNodeAbortCommand(a, o))
	// Upstream ironic spells this "baremetal node reboot"; koc groups it under
	// "node power". Register the upstream spelling too — hidden, since it
	// duplicates a visible sibling exactly.
	reboot := newNodePowerActionCommand(a, o, "reboot", "Reboot a node (upstream spelling of \"node power reboot\")", nodes.Rebooting)
	reboot.Hidden = true
	cmd.AddCommand(reboot)
	for _, sub := range newNodeProvisionCommands(a, o) {
		cmd.AddCommand(sub)
	}
	cmd.AddCommand(newNodeCleanCommand(a, o))
	cmd.AddCommand(newNodeServiceCommand(a, o))
	cmd.AddCommand(newNodeRescueCommand(a, o))
	cmd.AddCommand(newNodeUnholdCommand(a, o))
	cmd.AddCommand(newNodeValidateCommand(a, o))
	cmd.AddCommand(newNodeVIFCommand(a, o))
	cmd.AddCommand(newNodeBIOSCommand(a, o))
	cmd.AddCommand(newNodeFirmwareCommand(a, o))
	cmd.AddCommand(newNodeInjectNMICommand(a, o))
	return cmd
}

// nodeDetailFields are python-ironicclient's NODE_DETAILED_RESOURCE: the
// columns of `node list --long`, and the fields `node show` always carries.
var nodeDetailFields = []string{
	"allocation_uuid", "automated_clean", "bios_interface", "boot_interface", "boot_mode",
	"chassis_uuid", "clean_step", "conductor", "conductor_group", "console_enabled",
	"console_interface", "created_at", "raid_config", "deploy_interface", "deploy_step",
	"description", "disable_power_off", "driver", "driver_info", "driver_internal_info",
	"extra", "fault", "firmware_interface", "health", "inspect_interface",
	"inspection_finished_at", "inspection_started_at", "instance_info", "instance_name",
	"instance_uuid", "last_error", "lessee", "maintenance", "maintenance_reason",
	"management_interface", "name", "network_data", "network_interface", "owner",
	"parent_node", "power_interface", "power_state", "properties", "protected",
	"protected_reason", "provision_updated_at", "provision_state", "raid_interface",
	"rescue_interface", "reservation", "resource_class", "retired", "retired_reason",
	"secure_boot", "storage_interface", "target_power_state", "target_provision_state",
	"target_raid_config", "traits", "uuid", "updated_at", "vendor_interface",
}

// nodeFields are the columns of the short `node list`.
var nodeFields = []string{"uuid", "name", "instance_uuid", "power_state", "provision_state", "maintenance"}

// nodeShowFields renders a node body as `node show` does upstream.
func nodeShowFields(body map[string]any) ([]string, []any) {
	return showFields(body, nodeDetailFields)
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
	fl.StringVar(&f.resourceClass, flagResourceClass, "", "limit to nodes with this resource class")
	fl.StringVar(&f.sortKey, "sort-key", "", "sort output by this node attribute")
	fl.StringVar(&f.sortDir, "sort-dir", "", "sort direction: asc or desc")
	addListFieldsAliases(cmd, o)
	return cmd
}

// runNodeList performs the list and renders it. It takes an already-built
// service client so it can be exercised directly against a mock endpoint in
// tests.
func runNodeList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *nodeListFlags, w io.Writer) error {
	opts := nodeListQuery{
		ListOpts: nodes.ListOpts{
			Limit:          f.limit,
			Marker:         f.marker,
			ProvisionState: nodes.ProvisionState(f.provisionState),
			Driver:         f.driver,
			ResourceClass:  f.resourceClass,
			SortKey:        f.sortKey,
			SortDir:        f.sortDir,
		},
	}
	// gophercloud's nodes.ListOpts models these as plain bools with `q:` tags, so
	// BuildQueryString drops the false zero-value and the API returns ALL nodes —
	// silently ignoring --maintenance=false / --associated=false. Carry the
	// explicitly-set values as pointers so the wrapper below always emits the
	// parameter, including the negative case.
	if f.maintenanceSet {
		opts.maintenance = &f.maintenance
	}
	if f.associatedSet {
		opts.associated = &f.associated
	}

	// ironic's plain list carries only the six short columns; anything else —
	// --long, or a -c naming a detail column — needs /nodes/detail.
	fields, list := nodeFields, nodes.List
	if f.long || len(o.SelectedColumns(labels(nodeDetailFields)...)) > len(o.SelectedColumns(labels(nodeFields)...)) {
		fields, list = nodeDetailFields, nodes.ListDetail
	}
	all, err := paging.Collect(ctx, list(client, opts), f.limit, pageMaps("nodes"))
	if err != nil {
		return fmt.Errorf("listing baremetal nodes: %w", err)
	}
	return o.WriteList(w, listTable(all, fields))
}

// nodeListQuery wraps nodes.ListOpts to force the boolean filters that
// gophercloud's BuildQueryString would otherwise drop on their false
// zero-value. When maintenance/associated are set, the parameter is appended
// explicitly so both maintenance=true and maintenance=false reach the API.
type nodeListQuery struct {
	nodes.ListOpts
	maintenance *bool
	associated  *bool
}

// ToNodeListQuery implements nodes.ListOptsBuilder.
func (q nodeListQuery) ToNodeListQuery() (string, error) {
	s, err := q.ListOpts.ToNodeListQuery()
	return q.withBools(s, err)
}

// ToNodeListDetailQuery implements nodes.ListOptsBuilder for /nodes/detail.
func (q nodeListQuery) ToNodeListDetailQuery() (string, error) {
	s, err := q.ListOpts.ToNodeListDetailQuery()
	return q.withBools(s, err)
}

func (q nodeListQuery) withBools(s string, err error) (string, error) {
	if err != nil {
		return "", err
	}
	var extra []string
	if q.maintenance != nil {
		extra = append(extra, "maintenance="+strconv.FormatBool(*q.maintenance))
	}
	if q.associated != nil {
		extra = append(extra, "associated="+strconv.FormatBool(*q.associated))
	}
	return appendQuery(s, extra...), nil
}

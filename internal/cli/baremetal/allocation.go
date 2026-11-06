package baremetal

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/baremetal/v1/allocations"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/koc/internal/auth"
	"github.com/ftarasenko/koc/internal/cli/batchdelete"
	"github.com/ftarasenko/koc/internal/output"
	bmapi "github.com/ftarasenko/koc/pkg/openstack/baremetal"
)

// "baremetal allocation" — ironic's node-reservation resource (API 1.52): ask
// for a node matching a resource class and traits, and ironic picks one.
//
// Verb and flag names mirror upstream python-ironicclient. The KeyStack command
// reference at https://docs.keystack.ru/ was not reachable at implementation
// time (HTTP 403), so these are UNVERIFIED against KeyStack and fall back to
// upstream semantics.

// allocationPollInterval and allocationPollTimeout bound the --wait loop. Vars,
// not consts, so tests can shorten the interval.
var (
	allocationPollInterval = 2 * time.Second
	allocationPollTimeout  = 5 * time.Minute
)

func newAllocationCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "allocation",
		Short: "Manage baremetal allocations",
	}
	cmd.AddCommand(
		newAllocationListCommand(a, o),
		newAllocationShowCommand(a, o),
		newAllocationCreateCommand(a, o),
		newAllocationDeleteCommand(a, o),
		newAllocationSetCommand(a, o),
		newAllocationUnsetCommand(a, o),
	)
	return cmd
}

// allocation is ironic's allocation plus `owner`, which gophercloud v2 does
// not model but upstream `openstack baremetal allocation show` prints.
type allocation = bmapi.Allocation

func allocationShowFields(al *allocation) ([]string, []any) {
	return []string{
		"uuid", "name", "state", "node_uuid", "resource_class", "traits",
		"candidate_nodes", "last_error", "extra", "owner", "created_at", "updated_at",
	}, []any{
		al.UUID, al.Name, al.State, al.NodeUUID, al.ResourceClass, al.Traits,
		al.CandidateNodes, al.LastError, al.Extra, al.Owner, al.CreatedAt, al.UpdatedAt,
	}
}

// --- list -------------------------------------------------------------------

type allocationListFlags struct {
	node          string
	resourceClass string
	state         string
	owner         string
	long          bool
	limit         int
	marker        string
	sortKey       string
	sortDir       string
}

func newAllocationListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &allocationListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List baremetal allocations",
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
			return runAllocationList(ctx, client, o, f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.node, "node", "", "limit to allocations of this node (name or UUID)")
	fl.StringVar(&f.resourceClass, flagResourceClass, "", "limit to allocations of this resource class")
	fl.StringVar(&f.state, "state", "", "limit to allocations in this state: allocating, active or error")
	fl.StringVar(&f.owner, "owner", "", "limit to allocations with this owner (project ID)")
	fl.BoolVar(&f.long, "long", false, "list additional fields in output")
	fl.IntVar(&f.limit, "limit", 0, "maximum number of allocations to return")
	fl.StringVar(&f.marker, "marker", "", "UUID of the last allocation from the previous page")
	fl.StringVar(&f.sortKey, "sort-key", "", "sort output by this allocation attribute")
	fl.StringVar(&f.sortDir, "sort-dir", "", "sort direction: asc or desc")
	addListFieldsAliases(cmd, o)
	return cmd
}

func runAllocationList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	f *allocationListFlags, w io.Writer,
) error {
	opts := bmapi.AllocationListOpts{
		ListOpts: allocations.ListOpts{
			Node:          f.node,
			ResourceClass: f.resourceClass,
			State:         allocations.AllocationState(f.state),
			Limit:         f.limit,
			Marker:        f.marker,
			SortKey:       f.sortKey,
			SortDir:       f.sortDir,
		},
		Owner: f.owner,
	}
	// Ironic treats "limit" as a page size, so it is also enforced as a hard
	// result cap — the same treatment node/port listings get.
	all, err := bmapi.ListAllocations(ctx, client, opts, f.limit)
	if err != nil {
		return err
	}
	cols := []string{"UUID", "Name", "Resource Class", "State", "Node UUID"}
	if f.long {
		cols = append(cols, "Traits", "Candidate Nodes", "Last Error", "Extra", "Owner")
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(all))}
	for _, al := range all {
		row := []any{al.UUID, al.Name, al.ResourceClass, al.State, al.NodeUUID}
		if f.long {
			row = append(row, al.Traits, al.CandidateNodes, al.LastError, al.Extra, al.Owner)
		}
		t.Rows = append(t.Rows, row)
	}
	return o.WriteList(w, t)
}

// --- show / delete ----------------------------------------------------------

func newAllocationShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <allocation>",
		Short: "Show details of a baremetal allocation",
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
			return runAllocationShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runAllocationShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, id string, w io.Writer) error {
	al, err := bmapi.GetAllocation(ctx, client, id)
	if err != nil {
		return err
	}
	fields, values := allocationShowFields(al)
	return o.WriteSingle(w, fields, values)
}

func newAllocationDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <allocation> [<allocation> ...]",
		Short: "Delete baremetal allocation(s)",
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
			return runAllocationDelete(ctx, client, args, cmd.OutOrStdout())
		},
	}
}

func runAllocationDelete(ctx context.Context, client *gophercloud.ServiceClient, ids []string, w io.Writer) error {
	return batchdelete.Each(ids, func(id string) error {
		if err := bmapi.DeleteAllocation(ctx, client, id); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "Deleted allocation %s\n", id); err != nil {
			return err
		}
		return nil
	})
}

// --- create -----------------------------------------------------------------

type allocationCreateFlags struct {
	resourceClass  string
	name           string
	uuid           string
	owner          string
	traits         []string
	candidateNodes []string
	extra          []string
	wait           bool
	waitTimeout    time.Duration
}

func newAllocationCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &allocationCreateFlags{}
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a baremetal allocation",
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
			return runAllocationCreate(ctx, client, o, f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.resourceClass, flagResourceClass, "", "resource class to allocate a node from")
	fl.StringVar(&f.name, "name", "", "unique name for the allocation")
	fl.StringVar(&f.uuid, "uuid", "", "UUID for the allocation (default: generated by ironic)")
	fl.StringVar(&f.owner, "owner", "", "project that owns the allocation")
	fl.StringArrayVar(&f.traits, "trait", nil, "trait the allocated node must have (repeatable)")
	fl.StringArrayVar(&f.candidateNodes, "candidate-node", nil,
		"node to consider for the allocation (name or UUID, repeatable; default: all available nodes)")
	fl.StringArrayVar(&f.extra, "extra", nil, "arbitrary metadata key=value (repeatable)")
	fl.BoolVar(&f.wait, "wait", false, "wait until the allocation leaves the allocating state")
	fl.DurationVar(&f.waitTimeout, "wait-timeout", allocationPollTimeout, "maximum time to wait for --wait to complete")
	_ = cmd.MarkFlagRequired(flagResourceClass)
	return cmd
}

// allocationColumns are the columns an allocation write verb renders.
var allocationColumns = fieldNames(allocationShowFields(&allocation{}))

func runAllocationCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	f *allocationCreateFlags, w io.Writer,
) error {
	if err := o.CheckColumns(allocationColumns...); err != nil {
		return err
	}
	extra, err := parseStringKV(f.extra)
	if err != nil {
		return fmt.Errorf("parsing --extra: %w", err)
	}
	opts := bmapi.AllocationCreateOpts{
		CreateOpts: allocations.CreateOpts{
			ResourceClass:  f.resourceClass,
			Name:           f.name,
			UUID:           f.uuid,
			Traits:         f.traits,
			CandidateNodes: f.candidateNodes,
			Extra:          extra,
		},
		Owner: f.owner,
	}
	al, err := bmapi.CreateAllocation(ctx, client, opts)
	if err != nil {
		return err
	}
	if f.wait {
		if al, err = waitForAllocation(ctx, client, al.UUID, f.waitTimeout); err != nil {
			return err
		}
	}
	fields, values := allocationShowFields(al)
	return o.WriteSingle(w, fields, values)
}

// waitForAllocation polls until ironic settles the allocation. Creation is
// asynchronous: the POST returns immediately in the "allocating" state and a
// node is only attached once the scheduler finds one, so without this the
// node_uuid column would almost always be empty.
func waitForAllocation(ctx context.Context, client *gophercloud.ServiceClient, id string, timeout time.Duration) (*allocation, error) {
	if timeout <= 0 {
		timeout = allocationPollTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	return bmapi.WaitForAllocation(ctx, client, id, allocationPollInterval)
}

// --- set / unset ------------------------------------------------------------

// Ironic exposes allocation updates as a JSON patch (API 1.57) and gophercloud
// v2.13.0 ships no Update for this resource, so set/unset build the patch and
// PATCH it directly. The document shape is the same one nodes.Update uses.

func newAllocationSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var name string
	var extra []string
	cmd := &cobra.Command{
		Use:   "set <allocation>",
		Short: "Set baremetal allocation properties",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			if name == "" && len(extra) == 0 {
				return fmt.Errorf("baremetal allocation set requires at least one attribute flag")
			}
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runAllocationSet(ctx, client, o, args[0], name, extra, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&name, "name", "", "new name for the allocation")
	fl.StringArrayVar(&extra, "extra", nil, "set a metadata key=value (repeatable)")
	return cmd
}

func runAllocationSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	id, name string, extra []string, w io.Writer,
) error {
	var ops bmapi.Patch
	if name != "" {
		ops.Replace("/name", name)
	}
	for _, pair := range extra {
		k, v, err := parseKeyVal(pair)
		if err != nil {
			return fmt.Errorf("parsing --extra: %w", err)
		}
		ops.Add("/extra/"+escapeJSONPointer(k), v)
	}
	return patchAllocation(ctx, client, o, id, ops, w)
}

func newAllocationUnsetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var name bool
	var extra []string
	cmd := &cobra.Command{
		Use:   "unset <allocation>",
		Short: "Unset baremetal allocation properties",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			if !name && len(extra) == 0 {
				return fmt.Errorf("baremetal allocation unset requires at least one attribute flag")
			}
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runAllocationUnset(ctx, client, o, args[0], name, extra, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&name, "name", false, "clear the allocation's name")
	fl.StringArrayVar(&extra, "extra", nil, "metadata key to remove (repeatable)")
	return cmd
}

func runAllocationUnset(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	id string, name bool, extra []string, w io.Writer,
) error {
	var ops bmapi.Patch
	if name {
		ops.Remove("/name")
	}
	for _, k := range extra {
		ops.Remove("/extra/" + escapeJSONPointer(k))
	}
	return patchAllocation(ctx, client, o, id, ops, w)
}

func patchAllocation(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	id string, ops bmapi.Patch, w io.Writer,
) error {
	if err := o.CheckColumns(allocationColumns...); err != nil {
		return err
	}
	al, err := bmapi.UpdateAllocation(ctx, client, id, ops)
	if err != nil {
		return err
	}
	fields, values := allocationShowFields(al)
	return o.WriteSingle(w, fields, values)
}

// parseStringKV is parseKeyValMap for the string-valued maps ironic's
// allocation `extra` uses.
func parseStringKV(pairs []string) (map[string]string, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	m := make(map[string]string, len(pairs))
	for _, p := range pairs {
		k, v, err := parseKeyVal(p)
		if err != nil {
			return nil, err
		}
		m[k] = v
	}
	return m, nil
}

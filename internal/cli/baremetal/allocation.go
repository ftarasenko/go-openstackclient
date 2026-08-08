package baremetal

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/baremetal/v1/allocations"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/paging"
	"github.com/ftarasenko/go-openstackclient/internal/output"
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

func allocationShowFields(al *allocations.Allocation) ([]string, []any) {
	return []string{
			"uuid", "name", "state", "node_uuid", "resource_class", "traits",
			"candidate_nodes", "last_error", "extra", "created_at", "updated_at",
		}, []any{
			al.UUID, al.Name, al.State, al.NodeUUID, al.ResourceClass, al.Traits,
			al.CandidateNodes, al.LastError, al.Extra, al.CreatedAt, al.UpdatedAt,
		}
}

// --- list -------------------------------------------------------------------

type allocationListFlags struct {
	node          string
	resourceClass string
	state         string
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
	fl.StringVar(&f.resourceClass, "resource-class", "", "limit to allocations of this resource class")
	fl.StringVar(&f.state, "state", "", "limit to allocations in this state: allocating, active or error")
	fl.BoolVar(&f.long, "long", false, "list additional fields in output")
	fl.IntVar(&f.limit, "limit", 0, "maximum number of allocations to return")
	fl.StringVar(&f.marker, "marker", "", "UUID of the last allocation from the previous page")
	fl.StringVar(&f.sortKey, "sort-key", "", "sort output by this allocation attribute")
	fl.StringVar(&f.sortDir, "sort-dir", "", "sort direction: asc or desc")
	addFieldsAliases(cmd, o)
	return cmd
}

func runAllocationList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	f *allocationListFlags, w io.Writer,
) error {
	opts := allocations.ListOpts{
		Node:          f.node,
		ResourceClass: f.resourceClass,
		State:         allocations.AllocationState(f.state),
		Limit:         f.limit,
		Marker:        f.marker,
		SortKey:       f.sortKey,
		SortDir:       f.sortDir,
	}
	// Ironic treats "limit" as a page size, so it is also enforced as a hard
	// result cap — the same treatment node/port listings get.
	all, err := paging.Collect(ctx, allocations.List(client, opts), f.limit, allocations.ExtractAllocations)
	if err != nil {
		return fmt.Errorf("listing baremetal allocations: %w", err)
	}
	cols := []string{"UUID", "Name", "Resource Class", "State", "Node UUID"}
	if f.long {
		cols = append(cols, "Traits", "Candidate Nodes", "Last Error", "Extra")
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(all))}
	for _, al := range all {
		row := []any{al.UUID, al.Name, al.ResourceClass, al.State, al.NodeUUID}
		if f.long {
			row = append(row, al.Traits, al.CandidateNodes, al.LastError, al.Extra)
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
	al, err := allocations.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("showing baremetal allocation %s: %w", id, err)
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
	for _, id := range ids {
		if err := allocations.Delete(ctx, client, id).ExtractErr(); err != nil {
			return fmt.Errorf("deleting baremetal allocation %s: %w", id, err)
		}
		if _, err := fmt.Fprintf(w, "Deleted allocation %s\n", id); err != nil {
			return err
		}
	}
	return nil
}

// --- create -----------------------------------------------------------------

type allocationCreateFlags struct {
	resourceClass  string
	name           string
	uuid           string
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
	fl.StringVar(&f.resourceClass, "resource-class", "", "resource class to allocate a node from")
	fl.StringVar(&f.name, "name", "", "unique name for the allocation")
	fl.StringVar(&f.uuid, "uuid", "", "UUID for the allocation (default: generated by ironic)")
	fl.StringArrayVar(&f.traits, "trait", nil, "trait the allocated node must have (repeatable)")
	fl.StringArrayVar(&f.candidateNodes, "candidate-node", nil,
		"node to consider for the allocation (name or UUID, repeatable; default: all available nodes)")
	fl.StringArrayVar(&f.extra, "extra", nil, "arbitrary metadata key=value (repeatable)")
	fl.BoolVar(&f.wait, "wait", false, "wait until the allocation leaves the allocating state")
	fl.DurationVar(&f.waitTimeout, "wait-timeout", allocationPollTimeout, "maximum time to wait for --wait to complete")
	_ = cmd.MarkFlagRequired("resource-class")
	return cmd
}

func runAllocationCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	f *allocationCreateFlags, w io.Writer,
) error {
	extra, err := parseStringKV(f.extra)
	if err != nil {
		return fmt.Errorf("parsing --extra: %w", err)
	}
	opts := allocations.CreateOpts{
		ResourceClass:  f.resourceClass,
		Name:           f.name,
		UUID:           f.uuid,
		Traits:         f.traits,
		CandidateNodes: f.candidateNodes,
		Extra:          extra,
	}
	al, err := allocations.Create(ctx, client, opts).Extract()
	if err != nil {
		return fmt.Errorf("creating baremetal allocation: %w", err)
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
func waitForAllocation(ctx context.Context, client *gophercloud.ServiceClient, id string, timeout time.Duration) (*allocations.Allocation, error) {
	if timeout <= 0 {
		timeout = allocationPollTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(allocationPollInterval)
	defer ticker.Stop()

	for {
		al, err := allocations.Get(ctx, client, id).Extract()
		if err != nil {
			if ctx.Err() != nil {
				return nil, fmt.Errorf("waiting for allocation %s: %w", id, ctx.Err())
			}
			return nil, fmt.Errorf("polling allocation %s: %w", id, err)
		}
		switch al.State {
		case "active":
			return al, nil
		case "error":
			return nil, fmt.Errorf("allocation %s failed: %s", id, al.LastError)
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("waiting for allocation %s (last state %q): %w", id, al.State, ctx.Err())
		case <-ticker.C:
		}
	}
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
	var ops []allocationPatchOp
	if name != "" {
		ops = append(ops, allocationPatchOp{Op: "replace", Path: "/name", Value: name})
	}
	for _, pair := range extra {
		k, v, err := parseKeyVal(pair)
		if err != nil {
			return fmt.Errorf("parsing --extra: %w", err)
		}
		ops = append(ops, allocationPatchOp{Op: "add", Path: "/extra/" + escapeJSONPointer(k), Value: v})
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
	var ops []allocationPatchOp
	if name {
		ops = append(ops, allocationPatchOp{Op: "remove", Path: "/name"})
	}
	for _, k := range extra {
		ops = append(ops, allocationPatchOp{Op: "remove", Path: "/extra/" + escapeJSONPointer(k)})
	}
	return patchAllocation(ctx, client, o, id, ops, w)
}

// allocationPatchOp is one RFC 6902 operation. Value is omitted for "remove",
// which ironic rejects if it carries one.
type allocationPatchOp struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value,omitempty"`
}

func patchAllocation(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	id string, ops []allocationPatchOp, w io.Writer,
) error {
	var al allocations.Allocation
	resp, err := client.Patch(ctx, client.ServiceURL("allocations", id), ops, &al, &gophercloud.RequestOpts{
		OkCodes: []int{200},
	})
	if resp != nil && resp.Body != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if err != nil {
		return fmt.Errorf("updating baremetal allocation %s: %w", id, err)
	}
	fields, values := allocationShowFields(&al)
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

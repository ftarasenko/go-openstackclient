package baremetal

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Node history (API 1.78) and child nodes (1.83) — read-only sub-resources
// gophercloud's nodes package does not model.

var featureNodeChildren = ironicFeature{"baremetal node children list", "1.83", releaseBobcat}

// History columns are python-ironicclient's NODE_HISTORY_RESOURCE and its
// detailed form. ironic adds project at 1.113 and state, target state and
// duration at 1.115; on an older cloud those cells are empty.
var (
	nodeHistoryFields       = []string{"uuid", "created_at", "severity", "event"}
	nodeHistoryDetailFields = []string{
		"uuid", "created_at", "severity", "event_type", "event", "conductor", "user",
		"project", "state", "target_provision_state", "duration_seconds",
	}
)

// newNodeHistoryCommand builds "baremetal node history list|get".
func newNodeHistoryCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "history",
		Short: "Read a node's event history",
	}
	var long bool
	list := &cobra.Command{
		Use:   useListNode,
		Short: "List a node's history events",
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
			return runNodeHistoryList(ctx, client, o, args[0], long, cmd.OutOrStdout())
		},
	}
	list.Flags().BoolVar(&long, "long", false, "show detailed information about the events")
	get := &cobra.Command{
		Use:   "get <node> <event>",
		Short: "Show one history event of a node",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runNodeHistoryGet(ctx, client, o, args[0], args[1], cmd.OutOrStdout())
		},
	}
	cmd.AddCommand(list, get)
	return cmd
}

func runNodeHistoryList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, node string, long bool, w io.Writer) error {
	fields, q := nodeHistoryFields, map[string][]string{}
	if long {
		fields, q["detail"] = nodeHistoryDetailFields, []string{"True"}
	}
	events, err := ironicList(ctx, client, joinPath("nodes", node, "history"), "history", q, 0)
	if err != nil {
		return fmt.Errorf("listing history of baremetal node %s: %w", node, err)
	}
	return o.WriteList(w, listTable(events, fields))
}

func runNodeHistoryGet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, node, event string, w io.Writer) error {
	var body map[string]any
	if err := ironicGet(ctx, client, joinPath("nodes", node, "history", event), &body); err != nil {
		return fmt.Errorf("getting history event %s of baremetal node %s: %w", event, node, err)
	}
	fields, values := showFields(body, nodeHistoryDetailFields)
	return o.WriteSingle(w, fields, values)
}

// newNodeChildrenCommand builds "baremetal node children list".
func newNodeChildrenCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "children",
		Short: "Read a node's child nodes (requires ironic API 1.83, OpenStack 2023.2)",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   useListNode,
		Short: "List the child nodes of a node (requires ironic API 1.83, OpenStack 2023.2)",
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
			return runNodeChildrenList(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	})
	return cmd
}

func runNodeChildrenList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, node string, w io.Writer) error {
	var body struct {
		Children []string `json:"children"`
	}
	if err := ironicGet(ctx, client, joinPath("nodes", node, "children"), &body); err != nil {
		return fmt.Errorf("listing children of baremetal node %s: %w", node,
			explainMicroversion(ctx, client, featureNodeChildren, err))
	}
	t := output.Table{Columns: []string{label("children")}, Rows: make([][]any, 0, len(body.Children))}
	for _, c := range body.Children {
		t.Rows = append(t.Rows, []any{c})
	}
	return o.WriteList(w, t)
}

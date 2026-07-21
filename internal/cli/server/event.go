package server

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"sort"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// gophercloud v2 has no compute "instanceactions" package, so "server event"
// is implemented against the raw os-instance-actions endpoints (an
// AGENTS.md-sanctioned raw fallback), decoding into koc-owned DTOs. These
// endpoints record every user-visible action taken on a server (create, reboot,
// resize, …) and — per request — the individual events that made up the action.

// instanceAction is one entry from GET /servers/{id}/os-instance-actions.
// updated_at appears at nova microversion 2.58.
type instanceAction struct {
	Action       string `json:"action"`
	InstanceUUID string `json:"instance_uuid"`
	Message      string `json:"message"`
	ProjectID    string `json:"project_id"`
	RequestID    string `json:"request_id"`
	StartTime    string `json:"start_time"`
	UpdatedAt    string `json:"updated_at"`
	UserID       string `json:"user_id"`
}

type eventListFlags struct {
	long          bool
	marker        string
	limit         int
	changesSince  string
	changesBefore string
}

func newServerEventCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "event", Short: "Server action events (os-instance-actions)"}
	cmd.AddCommand(
		newServerEventListCommand(a, o),
		newServerEventShowCommand(a, o),
	)
	return cmd
}

func newServerEventListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &eventListFlags{}
	cmd := &cobra.Command{
		Use:   "list <server>",
		Short: "List recorded actions for a server",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newComputeClient(ctx, a)
			if err != nil {
				return err
			}
			return runServerEventList(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&f.long, "long", false, "list additional fields in output")
	fl.StringVar(&f.marker, "marker", "", "list events after this request ID (pagination marker, nova 2.58+)")
	fl.IntVar(&f.limit, "limit", 0, "maximum number of events to return")
	fl.StringVar(&f.changesSince, "changes-since", "", "only actions changed at/after this ISO-8601 time (nova 2.58+)")
	fl.StringVar(&f.changesBefore, "changes-before", "", "only actions changed at/before this ISO-8601 time (nova 2.66+)")
	return cmd
}

func runServerEventList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, serverRef string, f *eventListFlags, w io.Writer) error {
	id, err := resolveServerID(ctx, client, serverRef)
	if err != nil {
		return err
	}
	vals := url.Values{}
	for key, val := range map[string]string{
		"marker":         f.marker,
		"changes-since":  f.changesSince,
		"changes-before": f.changesBefore,
	} {
		if val != "" {
			vals.Set(key, val)
		}
	}
	// Nova treats limit only as a page size; ask for it as a hint but still
	// enforce --limit as a hard cap after decoding.
	if f.limit > 0 {
		vals.Set("limit", fmt.Sprintf("%d", f.limit))
	}
	u := client.ServiceURL("servers", id, "os-instance-actions")
	if q := vals.Encode(); q != "" {
		u += "?" + q
	}
	var resp struct {
		InstanceActions []instanceAction `json:"instanceActions"`
	}
	r, err := client.Get(ctx, u, &resp, &gophercloud.RequestOpts{OkCodes: []int{200}})
	if r != nil {
		_ = r.Body.Close()
	}
	if err != nil {
		return fmt.Errorf("listing events of server %q: %w", serverRef, err)
	}
	all := resp.InstanceActions
	if f.limit > 0 && len(all) > f.limit {
		all = all[:f.limit]
	}
	return o.WriteList(w, eventTable(all, f.long))
}

func eventTable(list []instanceAction, long bool) output.Table {
	cols := []string{"Request ID", "Server ID", "Action", "Start Time"}
	if long {
		cols = append(cols, "Message", "Project ID", "User ID")
	}
	t := output.Table{Columns: cols}
	for _, e := range list {
		row := []any{e.RequestID, e.InstanceUUID, e.Action, e.StartTime}
		if long {
			row = append(row, e.Message, e.ProjectID, e.UserID)
		}
		t.Rows = append(t.Rows, row)
	}
	return t
}

func newServerEventShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <server> <request-id>",
		Short: "Show a single server action and its events",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newComputeClient(ctx, a)
			if err != nil {
				return err
			}
			return runServerEventShow(ctx, client, o, args[0], args[1], cmd.OutOrStdout())
		},
	}
	return cmd
}

// runServerEventShow renders GET /servers/{id}/os-instance-actions/{request_id}.
// The response carries an "events" list of per-step dicts; text views
// (table/csv/value) flatten it one-event-per-line OSC-style, while json/yaml
// keep the raw structure so consumers can parse it.
func runServerEventShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, serverRef, requestID string, w io.Writer) error {
	id, err := resolveServerID(ctx, client, serverRef)
	if err != nil {
		return err
	}
	var resp struct {
		InstanceAction map[string]any `json:"instanceAction"`
	}
	u := client.ServiceURL("servers", id, "os-instance-actions", requestID)
	r, err := client.Get(ctx, u, &resp, &gophercloud.RequestOpts{OkCodes: []int{200}})
	if r != nil {
		_ = r.Body.Close()
	}
	if err != nil {
		return fmt.Errorf("showing event %s of server %q: %w", requestID, serverRef, err)
	}
	flatten := o.Format != output.FormatJSON && o.Format != output.FormatYAML
	fields, values := eventShowFields(resp.InstanceAction, flatten)
	return o.WriteSingle(w, fields, values)
}

// eventShowFields turns the raw instanceAction object into ASCII-sorted
// Field/Value pairs. When flatten is true the nested "events" list is collapsed
// to OSC-style strings; when false the raw structured value is preserved.
func eventShowFields(m map[string]any, flatten bool) ([]string, []any) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fields := make([]string, 0, len(keys))
	values := make([]any, 0, len(keys))
	for _, k := range keys {
		v := m[k]
		if flatten {
			v = flattenServerValue(v)
		}
		fields = append(fields, k)
		values = append(values, v)
	}
	return fields, values
}

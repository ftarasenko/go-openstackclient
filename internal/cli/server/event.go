package server

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"

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
	result        bool
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
	// The action list carries no outcome, so --result costs one extra GET per
	// listed action. --limit bounds it.
	fl.BoolVar(&f.result, "result", false,
		"add a Result column, read from each action's own events (one extra API call per action)")
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
	var results map[string]string
	if f.result {
		results = fetchEventResults(ctx, client, id, all)
	}
	return o.WriteList(w, eventTable(all, f.long, results))
}

// actionEvent is one entry of the "events" list on GET
// /servers/{id}/os-instance-actions/{request_id}. Only "result" is decoded:
// it is "Success" or "Error" once the step has finished and null while it is
// still running, which is the whole of what the Result column needs. The
// step's name and its admin-only traceback are left to "server event show".
type actionEvent struct {
	Result string `json:"result"`
}

// fetchEventResults reads each listed action's own events and reduces them to a
// single outcome, keyed by request ID.
//
// nova's action *list* has no outcome field — os-instance-actions returns the
// action's name, timestamps and, on failure, a message; the per-step "result"
// values live only on the detail view. So establishing that an action failed
// costs a "server event show" per action, which is precisely the loop this
// replaces.
//
// Best effort by design, like the migration counters: a detail call that fails
// leaves that row's Result blank rather than failing a listing that is already
// complete without it.
func fetchEventResults(ctx context.Context, client *gophercloud.ServiceClient,
	serverID string, list []instanceAction,
) map[string]string {
	results := make(map[string]string, len(list))
	for _, a := range list {
		if a.RequestID == "" {
			continue
		}
		var resp struct {
			InstanceAction struct {
				Events []actionEvent `json:"events"`
			} `json:"instanceAction"`
		}
		u := client.ServiceURL("servers", serverID, "os-instance-actions", a.RequestID)
		r, err := client.Get(ctx, u, &resp, &gophercloud.RequestOpts{OkCodes: []int{200}})
		if r != nil {
			_ = r.Body.Close()
		}
		if err != nil {
			continue
		}
		results[a.RequestID] = reduceEventResults(resp.InstanceAction.Events, a.Message)
	}
	return results
}

// reduceEventResults collapses an action's per-step results into one word.
//
// Any failed step fails the action, so "Error" wins outright. A step whose
// result is still null means nova has not finished that step, so the action is
// in progress — reporting it as a success would be wrong in the one case an
// operator is watching for. An action with no events at all falls back to the
// list's own message field, which nova only populates on failure.
func reduceEventResults(events []actionEvent, message string) string {
	if len(events) == 0 {
		if message != "" {
			return "Error"
		}
		return ""
	}
	pending := false
	for _, e := range events {
		switch {
		case strings.EqualFold(e.Result, "error"):
			return "Error"
		case e.Result == "":
			pending = true
		}
	}
	if pending {
		return "In Progress"
	}
	return "Success"
}

// eventTable renders the action listing. --long adds nova's own extra fields;
// results, when --result gathered them, adds the outcome column the list
// endpoint does not provide.
func eventTable(list []instanceAction, long bool, results map[string]string) output.Table {
	cols := []string{"Request ID", "Server ID", "Action", "Start Time"}
	if results != nil {
		cols = append(cols, "Result")
	}
	if long {
		cols = append(cols, "Message", "Updated At", "Project ID", "User ID")
	}
	t := output.Table{Columns: cols}
	for _, e := range list {
		row := []any{e.RequestID, e.InstanceUUID, e.Action, e.StartTime}
		if results != nil {
			row = append(row, results[e.RequestID])
		}
		if long {
			row = append(row, e.Message, e.UpdatedAt, e.ProjectID, e.UserID)
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

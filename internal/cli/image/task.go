package image

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/imagedata"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/tasks"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/paging"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// glance tasks (the async import/export jobs), the staging upload, and the
// multi-store listing.
//
// Flag names follow upstream OSC (`openstack image task list|show`,
// `image stage`, `image stores list`). UNVERIFIED against KeyStack docs
// (https://docs.keystack.ru/ returned HTTP 403 at implementation time); falls
// back to upstream OSC semantics.

// --- image task list/show ---------------------------------------------------

func newImageTaskCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "task", Short: "Inspect glance's asynchronous tasks"}
	cmd.AddCommand(newImageTaskListCommand(a, o), newImageTaskShowCommand(a, o))
	return cmd
}

type imageTaskListFlags struct {
	status  string
	typ     string
	limit   int
	marker  string
	sortKey string
	sortDir string
}

func newImageTaskListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &imageTaskListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List glance tasks",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newImageClient(ctx, a)
			if err != nil {
				return err
			}
			return runImageTaskList(ctx, client, o, f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.status, "status", "", "limit to tasks in this status: pending, processing, success or failure")
	fl.StringVar(&f.typ, "type", "", "limit to tasks of this type, e.g. import")
	fl.IntVar(&f.limit, "limit", 0, "maximum number of tasks to return")
	fl.StringVar(&f.marker, "marker", "", "ID of the last task from the previous page")
	fl.StringVar(&f.sortKey, "sort-key", "", "sort by created_at, expires_at, status, type or updated_at")
	fl.StringVar(&f.sortDir, "sort-dir", "", "sort direction: asc or desc")
	return cmd
}

func runImageTaskList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	f *imageTaskListFlags, w io.Writer,
) error {
	opts := taskListQuery{
		ListOpts: tasks.ListOpts{
			Status:  tasks.TaskStatus(f.status),
			Limit:   f.limit,
			Marker:  f.marker,
			SortKey: f.sortKey,
			SortDir: f.sortDir,
		},
		typ: f.typ,
	}
	all, err := paging.Collect(ctx, tasks.List(client, opts), f.limit, tasks.ExtractTasks)
	if err != nil {
		return fmt.Errorf("listing glance tasks: %w", err)
	}
	t := output.Table{
		Columns: []string{"ID", "Type", "Status", "Owner", "Expires At"},
		Rows:    make([][]any, 0, len(all)),
	}
	for _, task := range all {
		t.Rows = append(t.Rows, []any{task.ID, task.Type, task.Status, task.Owner, task.ExpiresAt})
	}
	return o.WriteList(w, t)
}

// taskListQuery adds the `type` filter that gophercloud's ListOpts declares
// with a `json:` tag instead of a `q:` one — so BuildQueryString ignores it and
// --type would be silently dropped.
type taskListQuery struct {
	tasks.ListOpts
	typ string
}

func (q taskListQuery) ToTaskListQuery() (string, error) {
	s, err := q.ListOpts.ToTaskListQuery()
	if err != nil {
		return "", err
	}
	if q.typ == "" {
		return s, nil
	}
	sep := "?"
	if len(s) > 0 && s[0] == '?' {
		sep = "&"
	}
	return s + sep + "type=" + q.typ, nil
}

func newImageTaskShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <task-id>",
		Short: "Show a glance task",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newImageClient(ctx, a)
			if err != nil {
				return err
			}
			return runImageTaskShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runImageTaskShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, id string, w io.Writer) error {
	task, err := tasks.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("showing glance task %s: %w", id, err)
	}
	return o.WriteSingle(w,
		[]string{"id", "type", "status", "owner", "message", "input", "result", "expires_at", "created_at", "updated_at"},
		[]any{task.ID, task.Type, task.Status, task.Owner, task.Message, task.Input, task.Result,
			task.ExpiresAt, task.CreatedAt, task.UpdatedAt})
}

// --- image stage ------------------------------------------------------------

func newImageStageCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "stage <image>",
		Short: "Upload image data to glance's staging area, ready for import",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newImageClient(ctx, a)
			if err != nil {
				return err
			}
			return runImageStage(ctx, client, args[0], file, cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "file to upload (default: standard input)")
	return cmd
}

// runImageStage uploads to /v2/images/<id>/stage. Unlike `image create --file`
// this leaves the image in "uploading" until `image import` moves it into a
// store, which is the point of the two-step flow.
func runImageStage(ctx context.Context, client *gophercloud.ServiceClient, ref, file string,
	stdin io.Reader, w io.Writer,
) error {
	id, err := resolveImageID(ctx, client, ref)
	if err != nil {
		return err
	}
	data := stdin
	if file != "" {
		f, oerr := os.Open(file) //nolint:gosec // G304: operator-supplied image path
		if oerr != nil {
			return fmt.Errorf("opening %q: %w", file, oerr)
		}
		defer func() { _ = f.Close() }()
		data = f
	}
	if err := imagedata.Stage(ctx, client, id, data).ExtractErr(); err != nil {
		return fmt.Errorf("staging data for image %q: %w", ref, err)
	}
	_, err = fmt.Fprintf(w, "Staged data for image %s\n", ref)
	return err
}

// --- image stores list ------------------------------------------------------

func newImageStoresCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var detail bool
	list := &cobra.Command{
		Use:   "list",
		Short: "List the backing stores glance is configured with",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newImageClient(ctx, a)
			if err != nil {
				return err
			}
			return runImageStoresList(ctx, client, o, detail, cmd.OutOrStdout())
		},
	}
	list.Flags().BoolVar(&detail, "detail", false, "include each store's properties (admin only; glance 2.15 or later)")

	cmd := &cobra.Command{Use: "stores", Short: "Inspect glance's backing stores"}
	cmd.AddCommand(list)
	return cmd
}

// runImageStoresList reads /v2/info/stores. gophercloud has no package for it,
// so this is a raw GET — a flat list with no pagination.
//
// The endpoint exists only when the operator enabled multi-store, and glance
// answers 404 otherwise; that is a configuration answer rather than a failure,
// so it is reported as one.
func runImageStoresList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	detail bool, w io.Writer,
) error {
	var doc struct {
		Stores []struct {
			ID          string         `json:"id"`
			Description string         `json:"description"`
			Default     bool           `json:"default"`
			Properties  map[string]any `json:"properties"`
		} `json:"stores"`
	}
	url := client.ServiceURL("info", "stores")
	if detail {
		url += "/detail"
	}
	resp, err := client.Get(ctx, url, &doc, &gophercloud.RequestOpts{OkCodes: []int{200}})
	if resp != nil && resp.Body != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if err != nil {
		if gophercloud.ResponseCodeIs(err, 404) {
			return fmt.Errorf("this glance has no multi-store support enabled: %w", err)
		}
		return fmt.Errorf("listing glance stores: %w", err)
	}

	cols := []string{"ID", "Description", "Default"}
	if detail {
		cols = append(cols, "Properties")
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(doc.Stores))}
	for _, store := range doc.Stores {
		row := []any{store.ID, store.Description, store.Default}
		if detail {
			row = append(row, store.Properties)
		}
		t.Rows = append(t.Rows, row)
	}
	return o.WriteList(w, t)
}

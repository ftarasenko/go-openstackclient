package s3cli

import (
	"context"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
	"github.com/ftarasenko/go-openstackclient/internal/s3"
)

// newObjectCommand builds the "object" child, giving "s3 object list|show".
func newObjectCommand(a *auth.Options, o *output.Options, f *connFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "object",
		Short: "Manage objects",
	}
	cmd.AddCommand(newObjectListCommand(a, o, f))
	cmd.AddCommand(newObjectShowCommand(a, o, f))
	return cmd
}

// objectListFlags holds the options accepted by "object list".
type objectListFlags struct {
	prefix string
	limit  int
}

const objectListLong = `List the objects in a bucket.

Sizes are exact bytes so they stay usable in scripts and in --format value/csv.
--limit is a hard cap on the result, not a page size: listing pages until the
cap is reached and stops there.`

func newObjectListCommand(a *auth.Options, o *output.Options, f *connFlags) *cobra.Command {
	lf := &objectListFlags{}
	cmd := &cobra.Command{
		Use:   "list <bucket>",
		Short: "List objects in a bucket",
		Long:  objectListLong,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			bucket, prefix, err := parseRef(args[0])
			if err != nil {
				return err
			}
			// "s3 object list db-backups/e2e-" is the natural way to type a
			// prefix, so accept it as one when --prefix was not given.
			if lf.prefix == "" {
				lf.prefix = prefix
			}
			ctx := cmd.Context()
			client, err := f.client(ctx, a)
			if err != nil {
				return err
			}
			return runObjectList(ctx, client, o, bucket, lf, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&lf.prefix, "prefix", "", "only list keys starting with this prefix")
	fl.IntVar(&lf.limit, "limit", 0, "maximum number of objects to return (0 = no limit)")
	return cmd
}

// runObjectList is the test seam for "object list".
func runObjectList(ctx context.Context, client *s3.Client, o *output.Options,
	bucket string, f *objectListFlags, w io.Writer) error {
	objects, err := client.ListObjects(ctx, bucket, f.prefix, f.limit)
	if err != nil {
		return fmt.Errorf("listing objects in %s: %w", bucket, err)
	}

	rows := make([][]any, len(objects))
	for i, obj := range objects {
		rows[i] = []any{obj.Key, obj.Size, formatTime(obj.LastModified), obj.ETag}
	}
	return o.WriteList(w, output.Table{
		Columns: []string{"Key", "Size", "Last Modified", "ETag"},
		Rows:    rows,
	})
}

const objectShowLong = `Show one object's metadata, without downloading it.

This is the equivalent of the "s3cmd info" call the backup pipeline makes to
verify an upload: it is a HEAD request, so the object's body is never
transferred.`

func newObjectShowCommand(a *auth.Options, o *output.Options, f *connFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "show <bucket>/<key>",
		Short: "Show an object's metadata",
		Long:  objectShowLong,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			bucket, key, err := parseObjectRef(args[0])
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := f.client(ctx, a)
			if err != nil {
				return err
			}
			return runObjectShow(ctx, client, o, bucket, key, cmd.OutOrStdout())
		},
	}
}

// runObjectShow is the test seam for "object show".
func runObjectShow(ctx context.Context, client *s3.Client, o *output.Options,
	bucket, key string, w io.Writer) error {
	info, err := client.HeadObject(ctx, bucket, key)
	if err != nil {
		if s3.IsNotFound(err) {
			return fmt.Errorf("no object %q in bucket %q", key, bucket)
		}
		return fmt.Errorf("reading %s/%s: %w", bucket, key, err)
	}

	fields := []string{"Bucket", "Key", "Size", "Last Modified", "ETag", "Content Type"}
	values := []any{info.Bucket, info.Key, info.Size, formatTime(info.LastModified), info.ETag, info.ContentType}

	metaKeys := make([]string, 0, len(info.Metadata))
	for k := range info.Metadata {
		metaKeys = append(metaKeys, k)
	}
	sort.Strings(metaKeys)
	for _, k := range metaKeys {
		fields = append(fields, "Meta "+k)
		values = append(values, info.Metadata[k])
	}
	return o.WriteSingle(w, fields, values)
}

// formatTime renders a timestamp the way the rest of koc's tables do, and an
// absent one as an empty cell rather than the zero year.
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02T15:04:05Z")
}

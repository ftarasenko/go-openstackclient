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
	cmd.AddCommand(newObjectDeleteCommand(a, o, f))
	return cmd
}

// objectListFlags holds the options accepted by "object list".
type objectListFlags struct {
	prefix      string
	delimiter   string
	limit       int
	human       bool
	allVersions bool
	filter      keyFilter
}

const objectListLong = `List the objects in a bucket.

Sizes are exact bytes so they stay usable in scripts and in --format value/csv;
--human renders them for a reader instead. --limit is a hard cap on the result,
not a page size: listing pages until the cap is reached and stops there.

A bucket is a flat keyspace, so a deep one lists every key at once.
--delimiter / collapses everything below each "directory" into a single entry
marked with a trailing separator, which is how to walk a big bucket one level
at a time:

  koc s3 object list db-backups --delimiter /
  koc s3 object list db-backups/2026/ --delimiter /

--include and --exclude filter the keys locally after the server has listed
them, so they cost no extra requests but do not reduce the listing itself.

--all-versions lists every version and delete marker rather than the current
object. Garage does not implement versioning — it reports every bucket as
unversioned — so this is for a koc pointed at AWS, Ceph RGW or MinIO.`

const objectListExample = `  # Everything, exact bytes
  koc s3 object list db-backups

  # One level, like a directory listing
  koc s3 object list db-backups --delimiter /

  # Just the compressed dumps, sizes for a human
  koc s3 object list db-backups --include "*.sql.gz" --human

  # Everything but the end-to-end test dumps
  koc s3 object list db-backups --exclude "e2e-*"`

func newObjectListCommand(a *auth.Options, o *output.Options, f *connFlags) *cobra.Command {
	lf := &objectListFlags{}
	cmd := &cobra.Command{
		Use:     "list <bucket>[/<prefix>]",
		Short:   "List objects in a bucket",
		Long:    objectListLong,
		Example: objectListExample,
		Args:    cobra.ExactArgs(1),
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
			if err := lf.filter.compile(); err != nil {
				return err
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
	fl.StringVar(&lf.delimiter, "delimiter", "",
		`collapse keys below each occurrence of this separator into one entry (usually "/")`)
	fl.IntVar(&lf.limit, "limit", 0, "maximum number of objects to return (0 = no limit)")
	fl.BoolVar(&lf.human, "human", false, "render sizes for a reader (KiB/MiB/GiB) instead of exact bytes")
	fl.BoolVar(&lf.allVersions, "all-versions", false,
		"list every version and delete marker, not just the current object")
	lf.filter.addTo(fl)
	return cmd
}

// runObjectList is the test seam for "object list".
func runObjectList(ctx context.Context, client *s3.Client, o *output.Options,
	bucket string, f *objectListFlags, w io.Writer) error {
	opts := s3.ListOptions{
		Prefix:    f.prefix,
		Delimiter: f.delimiter,
		Limit:     f.limit,
		Versions:  f.allVersions,
	}
	// A local filter has to be applied before --limit is counted, or a bucket
	// whose first 100 keys are all excluded would answer an empty list under
	// "--limit 10". So the cap moves here whenever filtering is on.
	if f.filter.active() {
		opts.Limit = 0
	}

	var rows [][]any
	err := client.ListObjectsFunc(ctx, bucket, opts, func(obj s3.Object) error {
		// A CommonPrefixes entry has no size or timestamp; it is a subtree.
		if !obj.IsPrefix && !f.filter.match(obj.Key) {
			return nil
		}
		rows = append(rows, objectRow(obj, f))
		if f.filter.active() && f.limit > 0 && len(rows) >= f.limit {
			return errStopListing
		}
		return nil
	})
	if err != nil && !isStopListing(err) {
		return fmt.Errorf("listing objects in %s: %w", bucket, err)
	}

	return o.WriteList(w, output.Table{Columns: objectListColumns(f), Rows: rows})
}

// objectListColumns is the header row, which grows by two under --all-versions
// so the extra fields are not silently dropped in --format value.
func objectListColumns(f *objectListFlags) []string {
	cols := []string{"Key", "Size", "Last Modified", "ETag"}
	if f.allVersions {
		cols = append(cols, "Version ID", "Latest")
	}
	return cols
}

// objectRow renders one listing entry.
func objectRow(obj s3.Object, f *objectListFlags) []any {
	key, size := obj.Key, any(obj.Size)
	switch {
	case obj.IsPrefix:
		// A subtree has no size of its own; saying "0" would read as an empty
		// object that is really there.
		size = ""
	case f.human:
		size = output.HumanBytes(obj.Size)
	}

	row := []any{key, size, formatTime(obj.LastModified), obj.ETag}
	if f.allVersions {
		version := obj.VersionID
		if obj.DeleteMarker {
			version += " (delete marker)"
		}
		row = append(row, version, obj.IsLatest)
	}
	return row
}

const objectShowLong = `Show one object's metadata, without downloading it.

This is the equivalent of the "s3cmd info" call the backup pipeline makes to
verify an upload: it is a HEAD request, so the object's body is never
transferred.

--version-id addresses one specific version on a store that has versioning
(Garage does not).`

func newObjectShowCommand(a *auth.Options, o *output.Options, f *connFlags) *cobra.Command {
	var versionID string
	var human bool
	cmd := &cobra.Command{
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
			return runObjectShow(ctx, client, o, bucket, key, versionID, human, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&versionID, "version-id", "", "address this version of the object instead of the current one")
	cmd.Flags().BoolVar(&human, "human", false, "render the size for a reader instead of exact bytes")
	return cmd
}

// runObjectShow is the test seam for "object show".
func runObjectShow(ctx context.Context, client *s3.Client, o *output.Options,
	bucket, key, versionID string, human bool, w io.Writer) error {
	info, err := client.HeadObject(ctx, bucket, key, versionID)
	if err != nil {
		if s3.IsNotFound(err) {
			return fmt.Errorf("no object %q in bucket %q", key, bucket)
		}
		return fmt.Errorf("reading %s/%s: %w", bucket, key, err)
	}

	size := any(info.Size)
	if human {
		size = output.HumanBytes(info.Size)
	}
	fields := []string{"Bucket", "Key", "Size", "Last Modified", "ETag", "Content Type"}
	values := []any{info.Bucket, info.Key, size, formatTime(info.LastModified), info.ETag, info.ContentType}
	if info.VersionID != "" {
		fields = append(fields, "Version ID")
		values = append(values, info.VersionID)
	}

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

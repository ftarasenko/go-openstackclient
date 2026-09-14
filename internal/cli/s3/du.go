package s3cli

import (
	"context"
	"fmt"
	"io"
	"sort"

	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
	"github.com/ftarasenko/go-openstackclient/internal/s3"
)

// duFlags holds the options accepted by "du".
type duFlags struct {
	group bool
}

const duLong = `Total the size of a bucket, or of one prefix in it.

S3 has no "how big is this" call, so this is a full listing folded into a sum:
the objects are never downloaded, but every key is walked, which costs one
request per 1000 of them. The listing streams, so the size of the bucket does
not become the size of koc's memory.

Sizes are exact bytes, like the rest of koc, so they stay usable in
--format value/csv and in arithmetic.

--group reports one row per storage class instead of one total, which is what
tells a mixed bucket's cheap tier from its expensive one. Objects a store
reports with no storage class are grouped as STANDARD, which is what it means.`

const duExample = `  # A whole bucket
  koc s3 du db-backups

  # Just the end-to-end test dumps
  koc s3 du db-backups/e2e-

  # Per storage class
  koc s3 du db-backups --group`

func newDuCommand(a *auth.Options, o *output.Options, f *connFlags) *cobra.Command {
	df := &duFlags{}
	cmd := &cobra.Command{
		Use:     "du <bucket>[/<prefix>]",
		Short:   "Total the size of a bucket or prefix",
		Long:    duLong,
		Example: duExample,
		Aliases: []string{"usage"},
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			bucket, prefix, err := parseRef(args[0])
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := f.client(ctx, a)
			if err != nil {
				return err
			}
			return runDu(ctx, client, o, bucket, prefix, df, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&df.group, "group", false, "one row per storage class instead of one total")
	return cmd
}

// usage is the running total for one storage class, or for the whole listing.
type usage struct {
	objects int64
	bytes   int64
}

// runDu is the test seam for "du".
func runDu(ctx context.Context, client *s3.Client, o *output.Options,
	bucket, prefix string, f *duFlags, w io.Writer) error {
	var total usage
	byClass := map[string]*usage{}

	err := client.ListObjectsFunc(ctx, bucket, prefix, 0, func(obj s3.Object) error {
		total.objects++
		total.bytes += obj.Size
		if f.group {
			class := obj.StorageClass
			if class == "" {
				class = "STANDARD"
			}
			u, ok := byClass[class]
			if !ok {
				u = &usage{}
				byClass[class] = u
			}
			u.objects++
			u.bytes += obj.Size
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("totalling %s/%s: %w", bucket, prefix, err)
	}

	if f.group {
		return writeUsageByClass(w, o, byClass)
	}
	return o.WriteSingle(w,
		[]string{"Bucket", "Prefix", "Objects", "Size"},
		[]any{bucket, prefix, total.objects, total.bytes})
}

// writeUsageByClass renders --group's per-storage-class rows, ordered by class
// name so two runs of the same bucket diff cleanly.
func writeUsageByClass(w io.Writer, o *output.Options, byClass map[string]*usage) error {
	classes := make([]string, 0, len(byClass))
	for class := range byClass {
		classes = append(classes, class)
	}
	sort.Strings(classes)

	rows := make([][]any, len(classes))
	for i, class := range classes {
		rows[i] = []any{class, byClass[class].objects, byClass[class].bytes}
	}
	return o.WriteList(w, output.Table{
		Columns: []string{"Storage Class", "Objects", "Size"},
		Rows:    rows,
	})
}

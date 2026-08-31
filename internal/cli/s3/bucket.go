package s3cli

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
	"github.com/ftarasenko/go-openstackclient/internal/s3"
)

// newBucketCommand builds the "bucket" child, giving the two-word command
// "s3 bucket list".
func newBucketCommand(a *auth.Options, o *output.Options, f *connFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bucket",
		Short: "Manage buckets",
	}
	cmd.AddCommand(newBucketListCommand(a, o, f))
	return cmd
}

const bucketListLong = `List the buckets the credentials can see.

The result is scoped to the access key, not to the store: Garage answers with
the buckets that key is granted, so a key made for one bucket lists exactly
that one. An empty list means the key exists but has been granted nothing.`

func newBucketListCommand(a *auth.Options, o *output.Options, f *connFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List buckets",
		Long:  bucketListLong,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := f.client(ctx, a)
			if err != nil {
				return err
			}
			return runBucketList(ctx, client, o, cmd.OutOrStdout())
		},
	}
}

// runBucketList is the test seam: it takes a built client and an io.Writer, so a
// test drives it against a mock endpoint with no credentials in play.
func runBucketList(ctx context.Context, client *s3.Client, o *output.Options, w io.Writer) error {
	buckets, err := client.ListBuckets(ctx)
	if err != nil {
		return fmt.Errorf("listing buckets on %s: %w", client.Endpoint(), err)
	}

	rows := make([][]any, len(buckets))
	for i, b := range buckets {
		rows[i] = []any{b.Name, formatTime(b.CreationDate)}
	}
	return o.WriteList(w, output.Table{Columns: []string{"Name", "Created"}, Rows: rows})
}

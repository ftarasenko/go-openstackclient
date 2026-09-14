package s3cli

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
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
	cmd.AddCommand(newBucketCreateCommand(a, o, f))
	cmd.AddCommand(newBucketDeleteCommand(a, o, f))
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

const bucketCreateLong = `Create a bucket.

The request states the bucket's location as the signing region (--s3-region,
default "garage"), which is the only value a store will accept: a request signed
for one region is not served by another. So there is no separate location flag —
set --s3-region and the bucket lands there.

Creating a bucket the credentials already own fails rather than succeeding
quietly, so a script can tell "I created this" from "it was already there". On
Garage the key that creates a bucket owns it and needs no further grant.`

const bucketCreateExample = `  koc s3 bucket create scratch
  koc s3 bucket create s3://scratch`

func newBucketCreateCommand(a *auth.Options, o *output.Options, f *connFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "create <bucket>",
		Short:   "Create a bucket",
		Long:    bucketCreateLong,
		Example: bucketCreateExample,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			bucket, err := parseBucketRef(args[0])
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := f.client(ctx, a)
			if err != nil {
				return err
			}
			return runBucketCreate(ctx, client, o, bucket, cmd.OutOrStdout())
		},
	}
}

// runBucketCreate is the test seam for "bucket create".
func runBucketCreate(ctx context.Context, client *s3.Client, o *output.Options,
	bucket string, w io.Writer) error {
	if err := client.CreateBucket(ctx, bucket); err != nil {
		switch s3.ErrorCode(err) {
		case "BucketAlreadyOwnedByYou":
			return fmt.Errorf("bucket %q already exists and is owned by these credentials", bucket)
		case "BucketAlreadyExists":
			return fmt.Errorf("bucket %q already exists and is owned by someone else", bucket)
		}
		return fmt.Errorf("creating bucket %q on %s: %w", bucket, client.Endpoint(), err)
	}
	return o.WriteSingle(w, []string{"Bucket", "Region"}, []any{bucket, client.Region()})
}

const bucketDeleteLong = `Delete one or more empty buckets.

A bucket that still holds objects is refused by the server — nothing is ever
removed implicitly. Empty it first:

  koc s3 object delete <bucket>/ --recursive

Every bucket named is attempted even if an earlier one fails, and the command
exits non-zero naming each failure.`

func newBucketDeleteCommand(a *auth.Options, o *output.Options, f *connFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "delete <bucket> [<bucket> ...]",
		Short:   "Delete empty buckets",
		Long:    bucketDeleteLong,
		Aliases: []string{"remove"},
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := f.client(ctx, a)
			if err != nil {
				return err
			}
			return runBucketDelete(ctx, client, args, cmd.OutOrStdout())
		},
	}
}

// runBucketDelete is the test seam for "bucket delete". It follows koc's batch
// contract: attempt every ref, join the failures.
func runBucketDelete(ctx context.Context, client *s3.Client, refs []string, w io.Writer) error {
	return batchdelete.Each(refs, func(ref string) error {
		bucket, err := parseBucketRef(ref)
		if err != nil {
			return err
		}
		if err := client.DeleteBucket(ctx, bucket); err != nil {
			if s3.ErrorCode(err) == "BucketNotEmpty" {
				return fmt.Errorf("bucket %q is not empty: delete its objects first "+
					"(koc s3 object delete %s/ --recursive)", bucket, bucket)
			}
			return fmt.Errorf("deleting bucket %q: %w", bucket, err)
		}
		_, err = fmt.Fprintf(w, "Deleted bucket: %s\n", bucket)
		return err
	})
}

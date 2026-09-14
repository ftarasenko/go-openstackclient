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

// objectDeleteFlags holds the options accepted by "object delete".
type objectDeleteFlags struct {
	recursive bool
	dryRun    bool
}

const objectDeleteLong = `Delete objects.

Each argument is one object: "<bucket>/<key>", or "s3://<bucket>/<key>". Every
argument is attempted even if an earlier one fails, and the command exits
non-zero naming each failure.

S3 answers a delete of a key that was never there with success, so deleting is
idempotent: a key that is already gone is reported deleted rather than as an
error. That is the protocol's behaviour, not a choice koc makes — do not use the
exit status to test whether a key existed, use "koc s3 object show".

--recursive turns each argument's key into a prefix and deletes every object
under it, which is also how a bucket is emptied before "koc s3 bucket delete":

  koc s3 object delete db-backups/e2e- --recursive    # one prefix
  koc s3 object delete db-backups/ --recursive        # the whole bucket

That is the one destructive shape in this group, so it is spelled out rather
than inferred from a trailing slash or a wildcard, and --dry-run prints exactly
the keys it would delete without touching any of them.`

func newObjectDeleteCommand(a *auth.Options, o *output.Options, f *connFlags) *cobra.Command {
	df := &objectDeleteFlags{}
	cmd := &cobra.Command{
		Use:     "delete <bucket>/<key> [<bucket>/<key> ...]",
		Short:   "Delete objects",
		Long:    objectDeleteLong,
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
			return runObjectDelete(ctx, client, args, df, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVarP(&df.recursive, "recursive", "r", false,
		"treat each key as a prefix and delete every object under it")
	fl.BoolVar(&df.dryRun, "dry-run", false,
		"print the objects that would be deleted and delete nothing")
	return cmd
}

// runObjectDelete is the test seam for "object delete".
func runObjectDelete(ctx context.Context, client *s3.Client, refs []string,
	f *objectDeleteFlags, w io.Writer) error {
	return batchdelete.Each(refs, func(ref string) error {
		if f.recursive {
			// A bare bucket is a legal prefix ref here — "<bucket>/" is how the
			// whole bucket is named — so parseRef, not parseObjectRef.
			bucket, prefix, err := parseRef(ref)
			if err != nil {
				return err
			}
			return deletePrefix(ctx, client, bucket, prefix, f.dryRun, w)
		}
		bucket, key, err := parseObjectRef(ref)
		if err != nil {
			return err
		}
		return deleteOne(ctx, client, bucket, key, f.dryRun, w)
	})
}

// deletePrefix deletes every object under prefix, one signed DELETE per key.
//
// Keys are deleted as the listing streams rather than after collecting it, so
// emptying a bucket costs one page of memory whatever its size. There is no
// batch DeleteObjects call: it would cut the request count, but it needs a
// Content-MD5 over a hand-built XML body and reports per-key failures in a
// 200 response, and the buckets koc is aimed at hold backups in the dozens.
func deletePrefix(ctx context.Context, client *s3.Client, bucket, prefix string,
	dryRun bool, w io.Writer) error {
	seen := 0
	err := client.ListObjectsFunc(ctx, bucket, prefix, 0, func(obj s3.Object) error {
		seen++
		return deleteOne(ctx, client, bucket, obj.Key, dryRun, w)
	})
	if err != nil {
		return fmt.Errorf("deleting %s/%s recursively: %w", bucket, prefix, err)
	}
	if seen == 0 {
		_, err = fmt.Fprintf(w, "No objects under %s/%s\n", bucket, prefix)
	}
	return err
}

// deleteOne deletes a single key, or says what it would have deleted.
func deleteOne(ctx context.Context, client *s3.Client, bucket, key string,
	dryRun bool, w io.Writer) error {
	if dryRun {
		_, err := fmt.Fprintf(w, "Would delete object: %s/%s\n", bucket, key)
		return err
	}
	if err := client.DeleteObject(ctx, bucket, key); err != nil {
		return fmt.Errorf("deleting %s/%s: %w", bucket, key, err)
	}
	_, err := fmt.Fprintf(w, "Deleted object: %s/%s\n", bucket, key)
	return err
}

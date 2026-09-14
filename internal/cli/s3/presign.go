package s3cli

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
	"github.com/ftarasenko/go-openstackclient/internal/s3"
)

const presignLong = `Print a URL that reads (or writes) one object, without a key.

The credential moves from the Authorization header into the query string, so the
whole request becomes a link anyone can fetch with curl or a browser until it
expires. It is how to hand a colleague one backup, or let a machine with no S3
credentials at all deliver a dump, without sharing the access key.

No request is made: this is local computation over the credentials koc already
has, so it works offline and leaves nothing on the store. That also means the URL
is not checked — presigning a key that does not exist succeeds and the URL 404s.

Treat the output like a password. Anyone holding it can read that object until
it expires, and it will appear in shell history, CI logs and any chat it is
pasted into. Keep --expire as short as the recipient needs.

--method put presigns an upload to the key instead of a download. SigV4 caps a
presigned URL's life at 7 days, and --s3-anonymous has nothing to sign with.`

const presignExample = `  # A link good for an hour
  koc s3 presign db-backups/dump-2026-09-13.sql.gz

  # A week, the protocol's maximum
  koc s3 presign db-backups/dump.sql.gz --expire 168h

  # Hand an upload slot to a machine with no credentials
  koc s3 presign db-backups/incoming.sql.gz --method put --expire 30m`

func newPresignCommand(a *auth.Options, o *output.Options, f *connFlags) *cobra.Command {
	var (
		expire time.Duration
		method string
		verID  string
	)
	cmd := &cobra.Command{
		Use:     "presign <bucket>/<key>",
		Short:   "Print a presigned URL for an object",
		Long:    presignLong,
		Example: presignExample,
		Args:    cobra.ExactArgs(1),
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
			return runPresign(client, o, bucket, key, verID, method, expire, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.DurationVar(&expire, "expire", time.Hour, "how long the URL stays valid (maximum 168h)")
	fl.StringVar(&method, "method", "get", "what the URL does: get or put")
	fl.StringVar(&verID, "version-id", "", "presign this version of the object instead of the current one")
	return cmd
}

// runPresign is the test seam for "presign". It takes no context: presigning
// makes no request.
func runPresign(client *s3.Client, o *output.Options, bucket, key, versionID, method string,
	expire time.Duration, w io.Writer) error {
	var (
		url string
		err error
	)
	switch method {
	case "get", "GET":
		url, err = client.PresignGetObject(bucket, key, versionID, expire)
	case "put", "PUT":
		if versionID != "" {
			return errors.New("--version-id addresses an existing object; it cannot be combined with --method put")
		}
		url, err = client.PresignPutObject(bucket, key, expire)
	default:
		return fmt.Errorf("--method must be get or put, got %q", method)
	}
	if err != nil {
		return fmt.Errorf("presigning %s/%s: %w", bucket, key, err)
	}

	return o.WriteSingle(w,
		[]string{"URL", "Method", "Expires"},
		[]any{url, method, expire.String()})
}

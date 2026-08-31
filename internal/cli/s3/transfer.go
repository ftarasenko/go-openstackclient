package s3cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
	"github.com/ftarasenko/go-openstackclient/internal/s3"
)

// downloadFlags holds the options accepted by "download".
type downloadFlags struct {
	force bool
}

const downloadLong = `Download an object to a file.

With no FILE the object's key basename is used. FILE "-" streams the object to
stdout as raw bytes and prints nothing else, so it pipes.

An existing file is never overwritten without --force: these are backups, and a
half-typed key that clobbers the local copy of one is not a recoverable
mistake. A transfer that fails partway removes the partial file rather than
leaving a truncated dump that looks complete.`

func newDownloadCommand(a *auth.Options, o *output.Options, f *connFlags) *cobra.Command {
	df := &downloadFlags{}
	cmd := &cobra.Command{
		Use:   "download <bucket>/<key> [file]",
		Short: "Download an object to a file",
		Long:  downloadLong,
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			bucket, key, err := parseObjectRef(args[0])
			if err != nil {
				return err
			}
			dest := ""
			if len(args) == 2 {
				dest = args[1]
			}
			ctx := cmd.Context()
			client, err := f.client(ctx, a)
			if err != nil {
				return err
			}
			return runDownload(ctx, client, o, bucket, key, dest, df, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&df.force, "force", false, "overwrite the destination file if it exists")
	return cmd
}

// runDownload is the test seam for "download". w receives either the object's
// raw bytes (dest "-") or the summary table.
func runDownload(ctx context.Context, client *s3.Client, o *output.Options,
	bucket, key, dest string, f *downloadFlags, w io.Writer) error {
	if dest == "-" {
		if _, err := client.GetObject(ctx, bucket, key, w); err != nil {
			return downloadError(bucket, key, err)
		}
		return nil
	}
	if dest == "" {
		dest = filepath.Base(key)
	}
	if info, err := os.Stat(dest); err == nil && info.IsDir() {
		dest = filepath.Join(dest, filepath.Base(key))
	}

	n, err := downloadToFile(ctx, client, bucket, key, dest, f.force)
	if err != nil {
		return err
	}
	return o.WriteSingle(w,
		[]string{"Bucket", "Key", "File", "Size"},
		[]any{bucket, key, dest, n})
}

// downloadToFile streams the object into dest, leaving nothing behind if it
// fails.
func downloadToFile(ctx context.Context, client *s3.Client, bucket, key, dest string, force bool) (n int64, err error) {
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if force {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	out, err := os.OpenFile(dest, flags, 0o600) //nolint:gosec // G304: operator-supplied output path
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return 0, fmt.Errorf("%s already exists (pass --force to overwrite)", dest)
		}
		return 0, fmt.Errorf("creating %q: %w", dest, err)
	}
	defer func() {
		closeErr := out.Close()
		if err == nil && closeErr != nil {
			err = fmt.Errorf("closing %q: %w", dest, closeErr)
		}
		if err != nil {
			_ = os.Remove(dest)
		}
	}()

	if n, err = client.GetObject(ctx, bucket, key, out); err != nil {
		return 0, downloadError(bucket, key, err)
	}
	return n, nil
}

// downloadError turns a missing key into the message an operator can act on.
func downloadError(bucket, key string, err error) error {
	if s3.IsNotFound(err) {
		return fmt.Errorf("no object %q in bucket %q", key, bucket)
	}
	return fmt.Errorf("downloading %s/%s: %w", bucket, key, err)
}

const uploadLong = `Upload a file to a bucket.

With no key, or a key ending in "/", the file's basename is used. The upload is
a single signed PUT — there is no multipart support, so the server's own
single-part ceiling (5 GiB on Garage and on AWS) applies.`

func newUploadCommand(a *auth.Options, o *output.Options, f *connFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "upload <file> <bucket>[/<key>]",
		Short: "Upload a file to a bucket",
		Long:  uploadLong,
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			bucket, key, err := parseRef(args[1])
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := f.client(ctx, a)
			if err != nil {
				return err
			}
			return runUpload(ctx, client, o, args[0], bucket, key, cmd.OutOrStdout())
		},
	}
	return cmd
}

// runUpload is the test seam for "upload".
func runUpload(ctx context.Context, client *s3.Client, o *output.Options,
	file, bucket, key string, w io.Writer) error {
	if key == "" || strings.HasSuffix(key, "/") {
		key += filepath.Base(file)
	}

	src, err := os.Open(file) //nolint:gosec // G304: operator-supplied upload path
	if err != nil {
		return fmt.Errorf("opening %q: %w", file, err)
	}
	defer func() { _ = src.Close() }()

	info, err := src.Stat()
	if err != nil {
		return fmt.Errorf("stat %q: %w", file, err)
	}
	if info.IsDir() {
		return fmt.Errorf("%q is a directory; upload takes a single file", file)
	}

	obj, err := client.PutObject(ctx, bucket, key, src, info.Size(), contentTypeFor(file))
	if err != nil {
		return fmt.Errorf("uploading %s to %s/%s: %w", file, bucket, key, err)
	}
	return o.WriteSingle(w,
		[]string{"Bucket", "Key", "File", "Size", "ETag"},
		[]any{bucket, key, file, obj.Size, obj.ETag})
}

// contentTypeFor guesses a Content-Type from the file extension, falling back to
// the S3 default. Getting this right matters for the objects koc uploads next to
// a backup — a .sha256 sibling should read as text in a browser, not download.
func contentTypeFor(file string) string {
	if ct := mime.TypeByExtension(filepath.Ext(file)); ct != "" {
		return ct
	}
	return "application/octet-stream"
}

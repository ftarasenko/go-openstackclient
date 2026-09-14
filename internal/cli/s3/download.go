package s3cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
	"github.com/ftarasenko/go-openstackclient/internal/s3"
)

// downloadFlags holds the options accepted by "download".
type downloadFlags struct {
	force       bool
	recursive   bool
	dryRun      bool
	flatten     bool
	versionID   string
	concurrency int
	filter      keyFilter
}

// downloadRequest is one "download" invocation: the object or prefix to fetch,
// where to put it, and the flags that govern it. The positional arguments travel
// with the flags because runDownload needs all of them and a parameter list that
// long says nothing about which is which (go:S107).
type downloadRequest struct {
	bucket string
	// key is one object's key, or — under --recursive or with a wildcard — the
	// prefix or glob selecting many.
	key string
	// dest is the destination path: "" means the key's basename, "-" means
	// stream to stdout. Under --recursive it is a directory.
	dest  string
	flags *downloadFlags
}

const downloadLong = `Download an object, a prefix, or a wildcard match.

With no FILE the object's key basename is used. FILE "-" streams the object to
stdout as raw bytes and prints nothing else, so it pipes.

An existing file is never overwritten without --force: these are backups, and a
half-typed key that clobbers the local copy of one is not a recoverable
mistake. A transfer that fails partway removes the partial file rather than
leaving a truncated dump that looks complete.

--recursive downloads every object under the key, treated as a prefix, into
FILE taken as a directory; a wildcard reference does the same without the flag:

  koc s3 download db-backups/2026/ ./restore --recursive
  koc s3 download "db-backups/2026/*.sql.gz" ./restore

Each object lands at its key's path relative to the prefix, so the layout is
preserved; --flatten puts every object straight in the directory instead, which
fails loudly rather than silently if two keys share a basename. In recursive
mode an existing file is *skipped* with a notice unless --force, so an
interrupted restore resumes by re-running it — unlike the single-object form,
where naming one key and doing nothing would hide the no-op.

--concurrency downloads that many objects at once, which is most of the reason
a bulk restore finishes: a thousand small objects are a thousand round trips
serially. Progress lines then appear in completion order, not listing order.

--include/--exclude narrow which keys are taken. --version-id fetches one
specific version of one object (Garage implements no versioning).`

func newDownloadCommand(a *auth.Options, o *output.Options, f *connFlags) *cobra.Command {
	df := &downloadFlags{}
	cmd := &cobra.Command{
		Use:   "download <bucket>/<key> [file]",
		Short: "Download an object, a prefix, or a wildcard match",
		Long:  downloadLong,
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			req, err := newDownloadRequest(args, df)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := f.client(ctx, a)
			if err != nil {
				return err
			}
			return runDownload(ctx, client, o, req, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&df.force, "force", false, "overwrite the destination file if it exists")
	fl.BoolVarP(&df.recursive, "recursive", "r", false,
		"treat the key as a prefix and download everything under it into a directory")
	fl.BoolVar(&df.dryRun, "dry-run", false, "print what would be downloaded and transfer nothing")
	fl.BoolVar(&df.flatten, "flatten", false,
		"put every object directly in the destination directory instead of mirroring its key path")
	fl.StringVar(&df.versionID, "version-id", "", "download this version of the object instead of the current one")
	fl.IntVar(&df.concurrency, "concurrency", s3.DefaultConcurrency,
		"objects to download at once under --recursive")
	df.filter.addTo(fl)
	return cmd
}

// newDownloadRequest parses the positional arguments and rejects the flag
// combinations that cannot mean anything.
func newDownloadRequest(args []string, f *downloadFlags) (downloadRequest, error) {
	bucket, key, err := parseRef(args[0])
	if err != nil {
		return downloadRequest{}, err
	}
	req := downloadRequest{bucket: bucket, key: key, flags: f}
	if len(args) == 2 {
		req.dest = args[1]
	}

	bulk := f.recursive || hasGlob(key)
	switch {
	case bulk && req.dest == "-":
		return req, errors.New("a recursive download needs a directory, not stdout")
	case bulk && f.versionID != "":
		return req, errors.New("--version-id names one object; it cannot be combined with --recursive")
	case !bulk && key == "":
		return req, fmt.Errorf("%q names no object: expected <bucket>/<key>", args[0])
	case !bulk && f.filter.active():
		return req, errors.New("--include/--exclude filter a listing; pass --recursive or a wildcard reference")
	case !bulk && f.flatten:
		return req, errors.New("--flatten only applies to a recursive download")
	}
	return req, f.filter.compile()
}

// runDownload is the test seam for "download". w receives either the object's
// raw bytes (dest "-"), the per-object progress lines of a recursive run, or the
// summary table of a single one.
func runDownload(ctx context.Context, client *s3.Client, o *output.Options,
	r downloadRequest, w io.Writer) error {
	if r.flags.recursive || hasGlob(r.key) {
		return runDownloadRecursive(ctx, client, r, w)
	}
	if r.dest == "-" {
		if r.flags.dryRun {
			_, err := fmt.Fprintf(w, "Would download %s/%s to stdout\n", r.bucket, r.key)
			return err
		}
		if _, err := client.GetObject(ctx, r.bucket, r.key, r.flags.versionID, w); err != nil {
			return downloadError(r.bucket, r.key, err)
		}
		return nil
	}

	dest := r.dest
	if dest == "" {
		dest = filepath.Base(r.key)
	}
	if info, err := os.Stat(dest); err == nil && info.IsDir() {
		dest = filepath.Join(dest, filepath.Base(r.key))
	}
	if r.flags.dryRun {
		_, err := fmt.Fprintf(w, "Would download %s/%s to %s\n", r.bucket, r.key, dest)
		return err
	}

	n, err := downloadToFile(ctx, client, r.bucket, r.key, r.flags.versionID, dest, r.flags.force)
	if err != nil {
		return err
	}
	return o.WriteSingle(w,
		[]string{"Bucket", "Key", "File", "Size"},
		[]any{r.bucket, r.key, dest, n})
}

// runDownloadRecursive downloads every matching object under a prefix.
func runDownloadRecursive(ctx context.Context, client *s3.Client, r downloadRequest, w io.Writer) error {
	dir := r.dest
	if dir == "" {
		dir = "."
	}
	prefix := globPrefix(r.key)
	var glob *regexp.Regexp
	if hasGlob(r.key) {
		var err error
		if glob, err = compileGlob(r.key); err != nil {
			return fmt.Errorf("%s/%s: %w", r.bucket, r.key, err)
		}
	}

	out := &syncWriter{w: w}
	p, workCtx := newPool(ctx, r.flags.concurrency)
	taken := 0

	err := client.ListObjectsFunc(ctx, r.bucket, s3.ListOptions{Prefix: prefix}, func(obj s3.Object) error {
		if strings.HasSuffix(obj.Key, "/") {
			// A "directory marker": a zero-length key ending in "/", which some
			// tools create. Mirroring it as a file would shadow the directory.
			return nil
		}
		if glob != nil && !glob.MatchString(obj.Key) {
			return nil
		}
		if !r.flags.filter.match(obj.Key) {
			return nil
		}
		dest, err := destForKey(dir, prefix, obj.Key, r.flags.flatten)
		if err != nil {
			return err
		}
		taken++
		if !p.run(workCtx, func() error {
			return downloadOne(workCtx, client, r.bucket, obj.Key, dest, r.flags, out)
		}) {
			return errStopListing
		}
		return nil
	})
	if waitErr := p.wait(); err == nil || isStopListing(err) {
		err = waitErr
	}
	if err != nil && !isStopListing(err) {
		return fmt.Errorf("downloading %s/%s: %w", r.bucket, r.key, err)
	}
	if taken == 0 {
		_, err = fmt.Fprintf(w, "No objects under %s/%s\n", r.bucket, r.key)
		return err
	}
	return nil
}

// downloadOne fetches one object of a recursive run, or says what it would have
// fetched.
func downloadOne(ctx context.Context, client *s3.Client, bucket, key, dest string,
	f *downloadFlags, w io.Writer) error {
	if f.dryRun {
		_, err := fmt.Fprintf(w, "Would download %s/%s to %s\n", bucket, key, dest)
		return err
	}
	// In bulk mode an existing file is a resumption point, not a mistake: a
	// restore that was interrupted is finished by re-running the same command.
	if !f.force {
		if _, err := os.Stat(dest); err == nil {
			_, err := fmt.Fprintf(w, "Skipped (exists): %s\n", dest)
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return fmt.Errorf("creating %q: %w", filepath.Dir(dest), err)
	}

	n, err := downloadToFile(ctx, client, bucket, key, "", dest, f.force)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "Downloaded: %s/%s -> %s (%d bytes)\n", bucket, key, dest, n)
	return err
}

// destForKey maps one key to its local path, and refuses a key that would
// escape the destination directory.
//
// A key is server-supplied data: "../../etc/cron.d/x", or an absolute one, is a
// perfectly legal S3 key, and a restore that wrote it where it says would let
// whoever can write to the bucket write anywhere koc can.
func destForKey(dir, prefix, key string, flatten bool) (string, error) {
	rel := key
	if flatten {
		rel = filepath.Base(key)
	} else if prefix != "" {
		rel = strings.TrimPrefix(key, prefix)
		// A prefix that is not a path boundary ("e2e-") leaves a bare file
		// name, which is what should land in the directory.
		rel = strings.TrimPrefix(rel, "/")
	}
	if rel == "" {
		rel = filepath.Base(key)
	}

	dest := filepath.Join(dir, filepath.FromSlash(rel))
	clean := filepath.Clean(dir)
	if rel := mustRel(clean, dest); rel == "" {
		return "", fmt.Errorf("object key %q would be written outside %s; refusing", key, clean)
	}
	return dest, nil
}

// mustRel returns the path of target under base, or "" if it is not under it.
func mustRel(base, target string) string {
	rel, err := filepath.Rel(base, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	return rel
}

// downloadToFile streams the object into dest, leaving nothing behind if it
// fails.
func downloadToFile(ctx context.Context, client *s3.Client, bucket, key, versionID, dest string,
	force bool) (n int64, err error) {
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

	if n, err = client.GetObject(ctx, bucket, key, versionID, out); err != nil {
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

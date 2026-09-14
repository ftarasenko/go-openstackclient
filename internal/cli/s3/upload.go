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

// uploadFlags holds the options accepted by "upload".
type uploadFlags struct {
	recursive   bool
	dryRun      bool
	flatten     bool
	noClobber   bool
	contentType string
	partSizeMiB int
	concurrency int
	filter      keyFilter
}

// uploadRequest is one "upload" invocation.
type uploadRequest struct {
	// src is a file, a directory (with --recursive), or "-" for stdin.
	src    string
	bucket string
	key    string
	flags  *uploadFlags
}

const uploadLong = `Upload a file, a directory, or standard input.

With no key, or a key ending in "/", the file's basename is used.

Objects larger than the part size go out as a multipart upload, so there is no
5 GiB single-PUT ceiling: a dump of any size uploads, in --part-size chunks,
--concurrency of them at a time. An upload that fails is aborted, so a
half-written object never becomes visible.

SRC "-" reads standard input, which is what makes a dump streamable without
staging it on disk first:

  mysqldump ... | gzip | koc s3 upload - db-backups/dump-$(date +%F).sql.gz

A stream's length is not known ahead of time, so it is always multipart; memory
stays at --part-size × --concurrency however long the stream turns out to be.

--recursive uploads a directory tree, mirroring each file's path under the key
as a prefix; --flatten puts every file at the top of that prefix instead.
--include/--exclude select which files are taken, matched against the *key* the
file would get. --no-clobber skips a key that already exists, so an interrupted
bulk upload resumes by re-running the same command.

Content types are guessed from the file extension, which is what makes a
".sha256" sibling read as text in a browser rather than download; --content-type
overrides that for every object in the run. Standard input has no extension, so
it gets the S3 default unless --content-type says otherwise.`

const uploadExample = `  # One file
  koc s3 upload ./dump.sql.gz db-backups/

  # Straight from a pipe, no staging on disk
  mysqldump --all-databases | gzip | koc s3 upload - db-backups/nightly.sql.gz

  # A tree, skipping what is already there
  koc s3 upload ./restore db-backups/2026/ --recursive --no-clobber

  # Bigger parts for a fat link
  koc s3 upload ./huge.tar db-backups/ --part-size 128 --concurrency 8`

func newUploadCommand(a *auth.Options, o *output.Options, f *connFlags) *cobra.Command {
	uf := &uploadFlags{}
	cmd := &cobra.Command{
		Use:     "upload <file|dir|-> <bucket>[/<key>]",
		Short:   "Upload a file, a directory, or standard input",
		Long:    uploadLong,
		Example: uploadExample,
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			bucket, key, err := parseRef(args[1])
			if err != nil {
				return err
			}
			req := uploadRequest{src: args[0], bucket: bucket, key: key, flags: uf}
			if err := uf.validate(req); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := f.client(ctx, a)
			if err != nil {
				return err
			}
			return runUpload(ctx, client, o, req, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVarP(&uf.recursive, "recursive", "r", false, "upload a directory tree")
	fl.BoolVar(&uf.dryRun, "dry-run", false, "print what would be uploaded and transfer nothing")
	fl.BoolVar(&uf.flatten, "flatten", false,
		"put every file at the top of the destination prefix instead of mirroring its path")
	fl.BoolVar(&uf.noClobber, "no-clobber", false, "skip a key that already exists")
	fl.StringVar(&uf.contentType, "content-type", "", "Content-Type for every object in this run")
	fl.IntVar(&uf.partSizeMiB, "part-size", s3.DefaultPartSize>>20,
		"multipart part size in MiB (minimum 5)")
	fl.IntVar(&uf.concurrency, "concurrency", s3.DefaultConcurrency,
		"parts — or, under --recursive, files — to upload at once")
	uf.filter.addTo(fl)
	return cmd
}

// validate rejects flag combinations that cannot mean anything.
func (f *uploadFlags) validate(r uploadRequest) error {
	switch {
	case f.recursive && r.src == "-":
		return errors.New("standard input is one object; it cannot be combined with --recursive")
	case !f.recursive && f.flatten:
		return errors.New("--flatten only applies to a recursive upload")
	case !f.recursive && f.filter.active():
		return errors.New("--include/--exclude select files from a tree; pass --recursive")
	}
	return f.filter.compile()
}

// uploadOptions renders the flags into the client's options.
func (f *uploadFlags) uploadOptions(size int64, contentType string) s3.UploadOptions {
	return s3.UploadOptions{
		ContentType: contentType,
		PartSize:    int64(f.partSizeMiB) << 20,
		Concurrency: f.concurrency,
		Size:        size,
	}
}

// runUpload is the test seam for "upload".
func runUpload(ctx context.Context, client *s3.Client, o *output.Options,
	r uploadRequest, w io.Writer) error {
	switch {
	case r.flags.recursive:
		return runUploadRecursive(ctx, client, r, w)
	case r.src == "-":
		return runUploadStdin(ctx, client, o, r, w)
	default:
		return runUploadFile(ctx, client, o, r, w)
	}
}

// runUploadStdin uploads standard input under the key given, which must be a
// full key: a stream has no basename to fall back on.
func runUploadStdin(ctx context.Context, client *s3.Client, o *output.Options,
	r uploadRequest, w io.Writer) error {
	if r.key == "" || strings.HasSuffix(r.key, "/") {
		return fmt.Errorf("uploading standard input needs a full key, got %q", r.bucket+"/"+r.key)
	}
	if r.flags.dryRun {
		_, err := fmt.Fprintf(w, "Would upload standard input to %s/%s\n", r.bucket, r.key)
		return err
	}
	if skip, err := checkClobber(ctx, client, r.bucket, r.key, r.flags, w); skip || err != nil {
		return err
	}

	// Size -1 says "unknown", which forces the multipart path: a pipe cannot be
	// rewound, and a single signed PUT has to hash its body before sending it.
	opts := r.flags.uploadOptions(-1, r.flags.contentType)
	obj, err := client.PutObjectStream(ctx, r.bucket, r.key, os.Stdin, opts)
	if err != nil {
		return fmt.Errorf("uploading standard input to %s/%s: %w", r.bucket, r.key, err)
	}
	return o.WriteSingle(w,
		[]string{"Bucket", "Key", "File", "Size", "ETag"},
		[]any{r.bucket, r.key, "-", obj.Size, obj.ETag})
}

// runUploadFile uploads one local file.
func runUploadFile(ctx context.Context, client *s3.Client, o *output.Options,
	r uploadRequest, w io.Writer) error {
	key := r.key
	if key == "" || strings.HasSuffix(key, "/") {
		key += filepath.Base(r.src)
	}

	src, err := os.Open(r.src)
	if err != nil {
		return fmt.Errorf("opening %q: %w", r.src, err)
	}
	defer func() { _ = src.Close() }()

	info, err := src.Stat()
	if err != nil {
		return fmt.Errorf("stat %q: %w", r.src, err)
	}
	if info.IsDir() {
		return fmt.Errorf("%q is a directory; pass --recursive to upload a tree", r.src)
	}
	if r.flags.dryRun {
		_, err := fmt.Fprintf(w, "Would upload %s to %s/%s\n", r.src, r.bucket, key)
		return err
	}
	if skip, err := checkClobber(ctx, client, r.bucket, key, r.flags, w); skip || err != nil {
		return err
	}

	opts := r.flags.uploadOptions(info.Size(), r.contentTypeFor(r.src))
	obj, err := client.PutObjectStream(ctx, r.bucket, key, src, opts)
	if err != nil {
		return fmt.Errorf("uploading %s to %s/%s: %w", r.src, r.bucket, key, err)
	}
	return o.WriteSingle(w,
		[]string{"Bucket", "Key", "File", "Size", "ETag"},
		[]any{r.bucket, key, r.src, obj.Size, obj.ETag})
}

// runUploadRecursive uploads every file under a directory.
func runUploadRecursive(ctx context.Context, client *s3.Client, r uploadRequest, w io.Writer) error {
	root, err := os.Stat(r.src)
	if err != nil {
		return fmt.Errorf("reading %q: %w", r.src, err)
	}
	if !root.IsDir() {
		return fmt.Errorf("%q is not a directory; drop --recursive to upload one file", r.src)
	}

	out := &syncWriter{w: w}
	// Under --recursive the workers are files, and each file's own multipart
	// then goes out a part at a time: the product of the two would put
	// concurrency² parts in flight and that many part buffers in memory.
	fileFlags := *r.flags
	fileFlags.concurrency = 1
	p, workCtx := newPool(ctx, r.flags.concurrency)

	taken := 0
	err = filepath.WalkDir(r.src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() {
			// Symlinks and devices are skipped rather than followed: a tree
			// walked from a backup directory should not chase a link out of it.
			return nil
		}
		key, err := keyForFile(r.key, r.src, path, r.flags.flatten)
		if err != nil {
			return err
		}
		if !r.flags.filter.match(key) {
			return nil
		}
		taken++
		if !p.run(workCtx, func() error {
			return uploadOne(workCtx, client, path, r.bucket, key, &fileFlags, out)
		}) {
			return filepath.SkipAll
		}
		return nil
	})
	if waitErr := p.wait(); err == nil {
		err = waitErr
	}
	if err != nil {
		return fmt.Errorf("uploading %s to %s/%s: %w", r.src, r.bucket, r.key, err)
	}
	if taken == 0 {
		_, err = fmt.Fprintf(w, "No files under %s\n", r.src)
		return err
	}
	return nil
}

// uploadOne uploads one file of a recursive run.
func uploadOne(ctx context.Context, client *s3.Client, path, bucket, key string,
	f *uploadFlags, w io.Writer) error {
	if f.dryRun {
		_, err := fmt.Fprintf(w, "Would upload %s to %s/%s\n", path, bucket, key)
		return err
	}
	if skip, err := checkClobber(ctx, client, bucket, key, f, w); skip || err != nil {
		return err
	}

	src, err := os.Open(path) //nolint:gosec // G304: path from the operator-named tree
	if err != nil {
		return fmt.Errorf("opening %q: %w", path, err)
	}
	defer func() { _ = src.Close() }()

	info, err := src.Stat()
	if err != nil {
		return fmt.Errorf("stat %q: %w", path, err)
	}

	contentType := f.contentType
	if contentType == "" {
		contentType = contentTypeFor(path)
	}
	obj, err := client.PutObjectStream(ctx, bucket, key, src, f.uploadOptions(info.Size(), contentType))
	if err != nil {
		return fmt.Errorf("uploading %s to %s/%s: %w", path, bucket, key, err)
	}
	_, err = fmt.Fprintf(w, "Uploaded: %s -> %s/%s (%d bytes)\n", path, bucket, key, obj.Size)
	return err
}

// checkClobber implements --no-clobber: one HEAD before the transfer, and the
// object is left alone if it is already there. It reports whether the caller
// should skip this object.
func checkClobber(ctx context.Context, client *s3.Client, bucket, key string,
	f *uploadFlags, w io.Writer) (skip bool, err error) {
	if !f.noClobber {
		return false, nil
	}
	if _, err := client.HeadObject(ctx, bucket, key, ""); err != nil {
		if s3.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("checking %s/%s: %w", bucket, key, err)
	}
	_, err = fmt.Fprintf(w, "Skipped (exists): %s/%s\n", bucket, key)
	return true, err
}

// keyForFile maps one local path to its object key, mirroring the tree under
// the destination prefix unless flattened.
func keyForFile(prefix, root, path string, flatten bool) (string, error) {
	rel := filepath.Base(path)
	if !flatten {
		var err error
		if rel, err = filepath.Rel(root, path); err != nil {
			return "", fmt.Errorf("resolving %q under %q: %w", path, root, err)
		}
	}
	// Object keys are slash-separated whatever the local separator is, so a
	// tree uploaded from Windows lands under the same keys as from Linux.
	rel = filepath.ToSlash(rel)

	if prefix == "" {
		return rel, nil
	}
	return strings.TrimSuffix(prefix, "/") + "/" + rel, nil
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

// contentTypeFor on the request prefers an explicit --content-type.
func (r uploadRequest) contentTypeFor(file string) string {
	if r.flags.contentType != "" {
		return r.flags.contentType
	}
	return contentTypeFor(file)
}

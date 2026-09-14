package s3cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
	"github.com/ftarasenko/go-openstackclient/internal/s3"
)

// copyFlags holds the options accepted by "copy" and "move".
type copyFlags struct {
	recursive   bool
	dryRun      bool
	flatten     bool
	versionID   string
	contentType string
	concurrency int
	filter      keyFilter
	// remove makes this a move: the source is deleted once the copy lands.
	remove bool
}

const copyLong = `Copy objects inside the store, without the bytes travelling through koc.

The source is named in a header, so the store does the copying: a 100 GiB
object is one small request, at no egress cost and no local disk. That is what
makes this the way to promote, rename or archive a backup — as opposed to a
download followed by an upload, which moves every byte twice through whatever
link koc is on.

--recursive copies every object under the source key, treated as a prefix, to
the destination key as a prefix; a wildcard source does the same without the
flag. Each object keeps its path relative to the source prefix, or goes straight
under the destination with --flatten.

--version-id copies one specific version of one object (Garage implements no
versioning).`

const copyExample = `  # Promote last night's dump to a stable name
  koc s3 copy db-backups/nightly-2026-09-13.sql.gz db-backups/latest.sql.gz

  # Archive a month into another bucket
  koc s3 copy db-backups/2026/08/ archive/2026-08/ --recursive

  # Everything matching a pattern
  koc s3 copy "db-backups/*.sha256" checksums/ --recursive`

const moveLong = `Move objects inside the store: a server-side copy, then a delete of the source.

S3 has no atomic move, so this is the two calls in order — the source is only
removed once the copy has landed, and a failure therefore leaves the source
intact rather than losing the object. A move interrupted between the two leaves
both copies, never neither.

Every flag behaves as for "koc s3 copy".`

func newCopyCommand(a *auth.Options, o *output.Options, f *connFlags) *cobra.Command {
	return newCopyLikeCommand(a, o, f, copySpec{
		use:     "copy <bucket>/<key> <bucket>/<key>",
		short:   "Copy objects inside the store (server-side)",
		long:    copyLong,
		example: copyExample,
		aliases: []string{"cp"},
	})
}

func newMoveCommand(a *auth.Options, o *output.Options, f *connFlags) *cobra.Command {
	return newCopyLikeCommand(a, o, f, copySpec{
		use:     "move <bucket>/<key> <bucket>/<key>",
		short:   "Move objects inside the store (server-side copy, then delete)",
		long:    moveLong,
		aliases: []string{"mv"},
		remove:  true,
	})
}

// copySpec is the per-verb text of the two commands copy.go builds, which are
// the same command bar the trailing delete.
type copySpec struct {
	use, short, long, example string
	aliases                   []string
	remove                    bool
}

func newCopyLikeCommand(a *auth.Options, o *output.Options, f *connFlags, spec copySpec) *cobra.Command {
	cf := &copyFlags{remove: spec.remove}
	cmd := &cobra.Command{
		Use:     spec.use,
		Short:   spec.short,
		Long:    spec.long,
		Example: spec.example,
		Aliases: spec.aliases,
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			src, dst, err := parseCopyRefs(args[0], args[1], cf)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := f.client(ctx, a)
			if err != nil {
				return err
			}
			return runCopy(ctx, client, o, src, dst, cf, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVarP(&cf.recursive, "recursive", "r", false,
		"treat the source key as a prefix and copy everything under it")
	fl.BoolVar(&cf.dryRun, "dry-run", false, "print what would be copied and copy nothing")
	fl.BoolVar(&cf.flatten, "flatten", false,
		"put every object directly under the destination prefix instead of mirroring its path")
	fl.StringVar(&cf.versionID, "version-id", "", "copy this version of the source instead of the current one")
	fl.StringVar(&cf.contentType, "content-type", "",
		"Content-Type for the destination; without it the source's metadata is carried over")
	fl.IntVar(&cf.concurrency, "concurrency", s3.DefaultConcurrency,
		"objects to copy at once under --recursive")
	cf.filter.addTo(fl)
	return cmd
}

// parseCopyRefs validates the two references and the flag combination.
func parseCopyRefs(srcRef, dstRef string, f *copyFlags) (src, dst s3.ObjectRef, err error) {
	srcBucket, srcKey, err := parseRef(srcRef)
	if err != nil {
		return src, dst, err
	}
	dstBucket, dstKey, err := parseRef(dstRef)
	if err != nil {
		return src, dst, err
	}
	src = s3.ObjectRef{Bucket: srcBucket, Key: srcKey}
	dst = s3.ObjectRef{Bucket: dstBucket, Key: dstKey}

	bulk := f.recursive || hasGlob(srcKey)
	switch {
	case bulk && f.versionID != "":
		return src, dst, errors.New("--version-id names one object; it cannot be combined with --recursive")
	case !bulk && srcKey == "":
		return src, dst, fmt.Errorf("%q names no object: expected <bucket>/<key>", srcRef)
	case !bulk && f.filter.active():
		return src, dst, errors.New("--include/--exclude filter a listing; pass --recursive or a wildcard source")
	case !bulk && f.flatten:
		return src, dst, errors.New("--flatten only applies to a recursive copy")
	case !bulk && src == dst:
		return src, dst, fmt.Errorf("source and destination are the same object (%s)", src)
	}
	return src, dst, f.filter.compile()
}

// runCopy is the test seam for "copy" and "move".
func runCopy(ctx context.Context, client *s3.Client, o *output.Options,
	src, dst s3.ObjectRef, f *copyFlags, w io.Writer) error {
	if f.recursive || hasGlob(src.Key) {
		return runCopyRecursive(ctx, client, src, dst, f, w)
	}

	// A destination ending in "/" — or naming only a bucket — takes the
	// source's basename, the same rule "upload" follows.
	if dst.Key == "" || strings.HasSuffix(dst.Key, "/") {
		dst.Key += keyBase(src.Key)
	}
	if f.dryRun {
		return sayCopy(w, src, dst, f.remove, true)
	}

	if err := copyOne(ctx, client, src, dst, f); err != nil {
		return err
	}
	// One object, one summary row — the per-object progress lines belong to the
	// recursive form, where there is more than one to follow.
	return o.WriteSingle(w, []string{"Source", "Destination"}, []any{src.String(), dst.String()})
}

// runCopyRecursive copies every matching object under a prefix.
func runCopyRecursive(ctx context.Context, client *s3.Client, src, dst s3.ObjectRef,
	f *copyFlags, w io.Writer) error {
	prefix := globPrefix(src.Key)
	var glob *regexp.Regexp
	if hasGlob(src.Key) {
		var err error
		if glob, err = compileGlob(src.Key); err != nil {
			return fmt.Errorf("%s: %w", src, err)
		}
	}

	out := &syncWriter{w: w}
	p, workCtx := newPool(ctx, f.concurrency)
	taken := 0

	err := client.ListObjectsFunc(ctx, src.Bucket, s3.ListOptions{Prefix: prefix}, func(obj s3.Object) error {
		if glob != nil && !glob.MatchString(obj.Key) {
			return nil
		}
		if !f.filter.match(obj.Key) {
			return nil
		}
		from := s3.ObjectRef{Bucket: src.Bucket, Key: obj.Key}
		to := s3.ObjectRef{Bucket: dst.Bucket, Key: copyDestKey(dst.Key, prefix, obj.Key, f.flatten)}
		if from == to {
			// Copying an object onto itself is refused by S3 unless metadata
			// changes, and in a recursive run it is a mistake, not a request.
			return nil
		}
		taken++
		if !p.run(workCtx, func() error {
			if f.dryRun {
				return sayCopy(out, from, to, f.remove, true)
			}
			if err := copyOne(workCtx, client, from, to, f); err != nil {
				return err
			}
			return sayCopy(out, from, to, f.remove, false)
		}) {
			return errStopListing
		}
		return nil
	})
	if waitErr := p.wait(); err == nil || isStopListing(err) {
		err = waitErr
	}
	if err != nil && !isStopListing(err) {
		return fmt.Errorf("copying %s: %w", src, err)
	}
	if taken == 0 {
		_, err = fmt.Fprintf(w, "No objects under %s\n", src)
		return err
	}
	return nil
}

// copyOne performs the copy and, for a move, the delete that follows it.
func copyOne(ctx context.Context, client *s3.Client, src, dst s3.ObjectRef, f *copyFlags) error {
	if _, err := client.CopyObject(ctx, src, dst, f.versionID, f.contentType); err != nil {
		return err
	}
	if !f.remove {
		return nil
	}
	// Only now: a delete before the copy has landed would lose the object.
	if err := client.DeleteObject(ctx, src.Bucket, src.Key, f.versionID); err != nil {
		return fmt.Errorf("copied %s to %s but could not delete the source: %w", src, dst, err)
	}
	return nil
}

// copyDestKey maps a source key to its destination key.
func copyDestKey(dstPrefix, srcPrefix, key string, flatten bool) string {
	rel := key
	if flatten {
		rel = keyBase(key)
	} else if srcPrefix != "" {
		rel = strings.TrimPrefix(strings.TrimPrefix(key, srcPrefix), "/")
	}
	if rel == "" {
		rel = keyBase(key)
	}
	if dstPrefix == "" {
		return rel
	}
	return strings.TrimSuffix(dstPrefix, "/") + "/" + rel
}

// keyBase is path.Base for an object key: the part after the last "/". It is
// not filepath.Base, which would split on "\\" on Windows and mangle a key that
// legitimately contains one.
func keyBase(key string) string {
	if i := strings.LastIndex(key, "/"); i >= 0 {
		return key[i+1:]
	}
	return key
}

// sayCopy prints one progress line, naming the operation the flags asked for.
func sayCopy(w io.Writer, src, dst s3.ObjectRef, remove, dryRun bool) error {
	var verb string
	switch {
	case dryRun && remove:
		verb = "Would move"
	case dryRun:
		verb = "Would copy"
	case remove:
		verb = "Moved"
	default:
		verb = "Copied"
	}
	_, err := fmt.Fprintf(w, "%s: %s -> %s\n", verb, src, dst)
	return err
}

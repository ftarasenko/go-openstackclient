package s3cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
	"github.com/ftarasenko/go-openstackclient/internal/s3"
)

// syncFlags holds the options accepted by "sync".
type syncFlags struct {
	del         bool
	sizeOnly    bool
	dryRun      bool
	force       bool
	concurrency int
	partSizeMiB int
	filter      keyFilter
}

const syncLong = `Make a destination match a source, transferring only what differs.

Both sides are named with the s3:// prefix for an S3 location and a plain path
for a local one:

  koc s3 sync ./restore s3://db-backups/2026/     # local  -> S3
  koc s3 sync s3://db-backups/2026/ ./restore     # S3     -> local
  koc s3 sync s3://db-backups/ s3://archive/      # S3     -> S3   (server-side)

sync is the one command in this group that *requires* the s3:// prefix. Every
other verb accepts a bare "<bucket>/<key>" because it has a single subject; sync
has two, and mistaking which one is local is how a --delete removes the wrong
side. A bare path is always local here.

An S3 side is a prefix, treated as a directory: a trailing "/" is added if it is
missing, and each object's key below it is what the two sides are matched on. A
local side is a directory tree; symlinks and devices are skipped rather than
followed.

An object is transferred when it is absent at the destination, when the sizes
differ, or when the source is newer. --size-only drops the timestamp test, which
is what you want for content that never changes in place (a dated backup) and
the cheapest correct rule there is.

The timestamp on the S3 side is the object's Last-Modified, i.e. when it was
uploaded — S3 keeps no record of the source file's own mtime, and a listing
would not return one if it did. That is correct in the steady state, because an
upload always lands after the file was written and a download always lands after
the object was: neither direction re-transfers what it just moved. The one seam
is a round trip — files restored with "download" carry the restore time, so
syncing that tree *back* re-uploads it once, after which it settles. Pass
--size-only to avoid even that.

--delete removes destination entries the source does not have, which is what
makes this a mirror rather than an overlay. It is refused when the source turned
out to be empty unless --force is also given: a mistyped source that lists
nothing would otherwise erase the destination. Entries excluded by
--include/--exclude are outside the sync and are never deleted. Local deletion
removes files, not the directories left behind.

--dry-run prints exactly the transfers and deletions it would perform.`

const syncExample = `  # Mirror a restore tree up, removing what is no longer local
  koc s3 sync ./restore s3://db-backups/2026/ --delete

  # Pull a month down, eight objects at a time
  koc s3 sync s3://db-backups/2026/08/ ./restore --concurrency 8

  # Dated dumps never change in place, so size is the whole test
  koc s3 sync s3://db-backups/ s3://archive/ --size-only

  # See what would move, before it moves
  koc s3 sync ./restore s3://db-backups/2026/ --delete --dry-run

  # Only the compressed dumps
  koc s3 sync s3://db-backups/ ./restore --include "*.sql.gz"`

func newSyncCommand(a *auth.Options, o *output.Options, f *connFlags) *cobra.Command {
	sf := &syncFlags{}
	cmd := &cobra.Command{
		Use:     "sync <source> <destination>",
		Short:   "Make a destination match a source, transferring only what differs",
		Long:    syncLong,
		Example: syncExample,
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			src, dst, err := parseSyncEndpoints(args[0], args[1])
			if err != nil {
				return err
			}
			if err := sf.filter.compile(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := f.client(ctx, a)
			if err != nil {
				return err
			}
			return runSync(ctx, client, src, dst, sf, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&sf.del, "delete", false,
		"remove destination entries the source does not have")
	fl.BoolVar(&sf.sizeOnly, "size-only", false,
		"compare only sizes, ignoring timestamps")
	fl.BoolVar(&sf.dryRun, "dry-run", false,
		"print the transfers and deletions that would happen, and perform none")
	fl.BoolVar(&sf.force, "force", false,
		"allow --delete even when the source turned out to be empty")
	fl.IntVar(&sf.concurrency, "concurrency", s3.DefaultConcurrency,
		"objects to transfer at once")
	fl.IntVar(&sf.partSizeMiB, "part-size", s3.DefaultPartSize>>20,
		"multipart part size in MiB for uploads (minimum 5)")
	sf.filter.addTo(fl)
	return cmd
}

// syncEndpoint is one end of a sync: a local directory, or a prefix in a
// bucket. Exactly one of dir / bucket is set.
type syncEndpoint struct {
	dir string

	bucket string
	// prefix always ends in "/" when non-empty, so a key below it trims
	// cleanly to the relative path the two sides are matched on.
	prefix string
}

func (e syncEndpoint) isLocal() bool { return e.bucket == "" }

func (e syncEndpoint) String() string {
	if e.isLocal() {
		return e.dir
	}
	return "s3://" + e.bucket + "/" + e.prefix
}

// join renders the full address of one relative key on this side.
func (e syncEndpoint) join(key string) string {
	if e.isLocal() {
		return filepath.Join(e.dir, filepath.FromSlash(key))
	}
	return e.prefix + key
}

// parseSyncEndpoints resolves both arguments and rejects the pairs that are not
// a sync.
func parseSyncEndpoints(srcRef, dstRef string) (src, dst syncEndpoint, err error) {
	if src, err = parseSyncEndpoint(srcRef); err != nil {
		return src, dst, err
	}
	if dst, err = parseSyncEndpoint(dstRef); err != nil {
		return src, dst, err
	}
	if src.isLocal() && dst.isLocal() {
		return src, dst, errors.New("at least one side must be an s3:// location; " +
			"two local paths are a job for cp or rsync")
	}
	if src == dst {
		return src, dst, fmt.Errorf("source and destination are the same location (%s)", src)
	}
	return src, dst, nil
}

// parseSyncEndpoint reads one side. The s3:// prefix is mandatory for an S3
// location here — see the command's Long for why sync alone insists on it.
func parseSyncEndpoint(ref string) (syncEndpoint, error) {
	rest, ok := strings.CutPrefix(ref, "s3://")
	if !ok {
		if ref == "" {
			return syncEndpoint{}, errors.New("a sync side cannot be empty")
		}
		return syncEndpoint{dir: ref}, nil
	}

	bucket, prefix, _ := strings.Cut(strings.TrimPrefix(rest, "/"), "/")
	if bucket == "" {
		return syncEndpoint{}, fmt.Errorf("%q names no bucket: expected s3://<bucket>[/<prefix>]", ref)
	}
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	return syncEndpoint{bucket: bucket, prefix: prefix}, nil
}

// checkLocalSides rejects a local side that is not a directory tree. A source
// that is a single file would otherwise walk to the relative path "." and
// upload itself under that key; a destination that is a file would fail later,
// halfway through the transfers.
func checkLocalSides(src, dst syncEndpoint) error {
	if src.isLocal() {
		info, err := os.Stat(src.dir)
		if err != nil {
			return fmt.Errorf("reading source %q: %w", src.dir, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("source %q is not a directory; sync mirrors trees, "+
				"use \"koc s3 upload\" for one file", src.dir)
		}
	}
	// A destination that does not exist yet is about to be created, so only an
	// existing non-directory is a problem.
	if dst.isLocal() {
		if info, err := os.Stat(dst.dir); err == nil && !info.IsDir() {
			return fmt.Errorf("destination %q is not a directory", dst.dir)
		}
	}
	return nil
}

// syncEntry is one item on either side, identified by its path relative to that
// side's root — which is what makes a local file and an object comparable.
type syncEntry struct {
	key   string
	size  int64
	mtime time.Time
	// path is the local file's path; empty for an object.
	path string
}

// runSync is the test seam for "sync".
func runSync(ctx context.Context, client *s3.Client, src, dst syncEndpoint,
	f *syncFlags, w io.Writer) error {
	if err := checkLocalSides(src, dst); err != nil {
		return err
	}

	// The destination is indexed rather than the source: a lookup per source
	// entry is what decides a transfer, and --delete then needs exactly the
	// entries nothing looked up. Memory is therefore the destination's size —
	// the two sides cannot be streamed against each other because S3 lists
	// keys lexicographically while a directory walk descends depth-first, so
	// "a.txt" and "a/b" come out in opposite orders.
	index, err := indexEndpoint(ctx, client, dst, f)
	if err != nil {
		return err
	}

	seen := make(map[string]bool, len(index))
	out := &syncWriter{w: w}
	p, workCtx := newPool(ctx, f.concurrency)
	source, transferred := 0, 0

	err = walkEndpoint(ctx, client, src, f, func(e syncEntry) error {
		source++
		seen[e.key] = true
		if at, ok := index[e.key]; ok && !f.needsTransfer(e, at) {
			return nil
		}
		transferred++
		if !p.run(workCtx, func() error { return syncOne(workCtx, client, src, dst, e, f, out) }) {
			return errStopListing
		}
		return nil
	})
	if waitErr := p.wait(); err == nil || isStopListing(err) {
		err = waitErr
	}
	if err != nil && !isStopListing(err) {
		return fmt.Errorf("syncing %s to %s: %w", src, dst, err)
	}

	removed, err := syncDelete(ctx, client, dst, index, seen, source, f, out)
	if err != nil {
		return err
	}
	if transferred == 0 && removed == 0 {
		_, err = fmt.Fprintf(w, "Already in sync: %s and %s (%d objects)\n", src, dst, source)
		return err
	}
	return nil
}

// needsTransfer is the comparison rule. Timestamps are truncated to the second
// because S3 reports Last-Modified at that resolution while a local mtime
// carries nanoseconds, and the difference alone would make every file look
// newer than its own copy.
func (f *syncFlags) needsTransfer(src, dst syncEntry) bool {
	if src.size != dst.size {
		return true
	}
	if f.sizeOnly {
		return false
	}
	return src.mtime.Truncate(time.Second).After(dst.mtime.Truncate(time.Second))
}

// indexEndpoint reads one side into a map keyed by relative path. A local
// destination that does not exist yet is an empty index, not an error — it is
// about to be created.
func indexEndpoint(ctx context.Context, client *s3.Client, e syncEndpoint,
	f *syncFlags) (map[string]syncEntry, error) {
	index := map[string]syncEntry{}
	err := walkEndpoint(ctx, client, e, f, func(entry syncEntry) error {
		index[entry.key] = entry
		return nil
	})
	if err != nil && e.isLocal() && errors.Is(err, os.ErrNotExist) {
		return index, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", e, err)
	}
	return index, nil
}

// walkEndpoint enumerates one side, applying --include/--exclude so that a
// filtered-out entry is invisible to both the comparison and --delete.
func walkEndpoint(ctx context.Context, client *s3.Client, e syncEndpoint,
	f *syncFlags, fn func(syncEntry) error) error {
	keep := func(entry syncEntry) error {
		if !f.filter.match(entry.key) {
			return nil
		}
		return fn(entry)
	}
	if e.isLocal() {
		return walkLocalTree(e.dir, keep)
	}
	return walkRemotePrefix(ctx, client, e, keep)
}

// walkLocalTree reports every regular file under root.
func walkLocalTree(root string, fn func(syncEntry) error) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Symlinks and devices are skipped rather than followed: a tree walked
		// from a backup directory should not chase a link out of it.
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("resolving %q under %q: %w", path, root, err)
		}
		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("stat %q: %w", path, err)
		}
		return fn(syncEntry{
			key:   filepath.ToSlash(rel),
			size:  info.Size(),
			mtime: info.ModTime(),
			path:  path,
		})
	})
}

// walkRemotePrefix reports every object below the endpoint's prefix.
func walkRemotePrefix(ctx context.Context, client *s3.Client, e syncEndpoint,
	fn func(syncEntry) error) error {
	return client.ListObjectsFunc(ctx, e.bucket, s3.ListOptions{Prefix: e.prefix},
		func(obj s3.Object) error {
			// A "directory marker" — a zero-length key ending in "/" that some
			// tools create — has no counterpart on a filesystem.
			if strings.HasSuffix(obj.Key, "/") {
				return nil
			}
			return fn(syncEntry{
				key:   strings.TrimPrefix(obj.Key, e.prefix),
				size:  obj.Size,
				mtime: obj.LastModified,
			})
		})
}

// syncOne transfers one entry from src to dst, by whichever of the three routes
// the pair of endpoints implies. A local-to-local pair was rejected at parse.
func syncOne(ctx context.Context, client *s3.Client, src, dst syncEndpoint,
	e syncEntry, f *syncFlags, w io.Writer) error {
	switch {
	case src.isLocal():
		return syncUpload(ctx, client, dst, e, f, w)
	case dst.isLocal():
		return syncDownload(ctx, client, src, dst, e, f, w)
	default:
		return syncCopy(ctx, client, src, dst, e, f, w)
	}
}

// syncUpload sends one local file to the destination prefix.
func syncUpload(ctx context.Context, client *s3.Client, dst syncEndpoint,
	e syncEntry, f *syncFlags, w io.Writer) error {
	key := dst.join(e.key)
	if f.dryRun {
		_, err := fmt.Fprintf(w, "Would upload %s to %s/%s\n", e.path, dst.bucket, key)
		return err
	}

	src, err := os.Open(e.path)
	if err != nil {
		return fmt.Errorf("opening %q: %w", e.path, err)
	}
	defer func() { _ = src.Close() }()

	// One part at a time: the pool's workers are already objects, and the
	// product of the two would put concurrency² part buffers in memory.
	opts := s3.UploadOptions{
		ContentType: contentTypeFor(e.path),
		PartSize:    int64(f.partSizeMiB) << 20,
		Concurrency: 1,
		Size:        e.size,
	}
	obj, err := client.PutObjectStream(ctx, dst.bucket, key, src, opts)
	if err != nil {
		return fmt.Errorf("uploading %s to %s/%s: %w", e.path, dst.bucket, key, err)
	}
	_, err = fmt.Fprintf(w, "Uploaded: %s -> %s/%s (%d bytes)\n", e.path, dst.bucket, key, obj.Size)
	return err
}

// syncDownload fetches one object into the destination tree.
func syncDownload(ctx context.Context, client *s3.Client, src, dst syncEndpoint,
	e syncEntry, f *syncFlags, w io.Writer) error {
	key := src.join(e.key)
	// A key is server-supplied data, and "../../etc/cron.d/x" is a legal one,
	// so the destination is checked to be inside the tree before anything is
	// created.
	path, err := destForKey(dst.dir, "", e.key, false)
	if err != nil {
		return err
	}
	if f.dryRun {
		_, err := fmt.Fprintf(w, "Would download %s/%s to %s\n", src.bucket, key, path)
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("creating %q: %w", filepath.Dir(path), err)
	}

	// force: replacing a stale copy is the whole point of a sync, unlike the
	// single-object "download" where an existing file means a mistyped key.
	n, err := downloadToFile(ctx, client, src.bucket, key, "", path, true)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "Downloaded: %s/%s -> %s (%d bytes)\n", src.bucket, key, path, n)
	return err
}

// syncCopy moves one object between two S3 locations, server-side.
func syncCopy(ctx context.Context, client *s3.Client, src, dst syncEndpoint,
	e syncEntry, f *syncFlags, w io.Writer) error {
	from := s3.ObjectRef{Bucket: src.bucket, Key: src.join(e.key)}
	to := s3.ObjectRef{Bucket: dst.bucket, Key: dst.join(e.key)}
	if f.dryRun {
		return sayCopy(w, from, to, false, true)
	}
	if _, err := client.CopyObject(ctx, from, to, "", ""); err != nil {
		return err
	}
	return sayCopy(w, from, to, false, false)
}

// syncDelete removes the destination entries the source did not have, and
// reports how many it took.
func syncDelete(ctx context.Context, client *s3.Client, dst syncEndpoint,
	index map[string]syncEntry, seen map[string]bool, source int,
	f *syncFlags, w io.Writer) (int, error) {
	if !f.del {
		return 0, nil
	}
	// A mistyped source that listed nothing would otherwise erase the
	// destination, and that is not a mistake anyone recovers from by re-running
	// the command.
	if source == 0 && !f.force {
		return 0, fmt.Errorf("the source is empty, so --delete would remove all %d "+
			"entries under %s; pass --force if that is what you meant", len(index), dst)
	}

	extra := make([]string, 0, len(index))
	for key := range index {
		if !seen[key] {
			extra = append(extra, key)
		}
	}
	// Sorted so a run is reproducible and its output diffs against the next.
	sort.Strings(extra)
	if len(extra) == 0 {
		return 0, nil
	}

	if dst.isLocal() {
		return len(extra), deleteLocalExtras(index, extra, f, w)
	}
	return len(extra), deleteRemoteExtras(ctx, client, dst, extra, f, w)
}

// deleteLocalExtras removes local files the source no longer has. The
// directories they leave behind are not removed — an empty directory is not
// something the source can be said to have deleted.
func deleteLocalExtras(index map[string]syncEntry, extra []string, f *syncFlags, w io.Writer) error {
	for _, key := range extra {
		path := index[key].path
		if f.dryRun {
			if _, err := fmt.Fprintf(w, "Would delete file: %s\n", path); err != nil {
				return err
			}
			continue
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("deleting %q: %w", path, err)
		}
		if _, err := fmt.Fprintf(w, "Deleted file: %s\n", path); err != nil {
			return err
		}
	}
	return nil
}

// deleteRemoteExtras removes objects the source no longer has, batched so a
// large mirror does not cost one request per key.
func deleteRemoteExtras(ctx context.Context, client *s3.Client, dst syncEndpoint,
	extra []string, f *syncFlags, w io.Writer) error {
	targets := make([]s3.DeleteTarget, 0, len(extra))
	for _, key := range extra {
		targets = append(targets, s3.DeleteTarget{Key: dst.join(key)})
	}
	for len(targets) > 0 {
		batch := targets
		if len(batch) > s3.MaxDeleteBatch {
			batch = batch[:s3.MaxDeleteBatch]
		}
		if err := deleteBatch(ctx, client, dst.bucket, batch, f.dryRun, w); err != nil {
			return err
		}
		targets = targets[len(batch):]
	}
	return nil
}

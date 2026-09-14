package s3cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"

	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
	"github.com/ftarasenko/go-openstackclient/internal/output"
	"github.com/ftarasenko/go-openstackclient/internal/s3"
)

// objectDeleteFlags holds the options accepted by "object delete".
type objectDeleteFlags struct {
	recursive   bool
	dryRun      bool
	versionID   string
	allVersions bool
	filter      keyFilter
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
than inferred from a trailing slash, and --dry-run prints exactly the keys it
would delete without touching any of them. A recursive delete also accepts a
wildcard instead of --recursive:

  koc s3 object delete "db-backups/2026/*.sql.gz"

A recursive delete goes out in batches of up to 1000 keys per request rather
than one request per key, so emptying a large bucket costs a thousandth of the
round trips. --include and --exclude narrow which keys it takes.

--version-id deletes one specific version; --all-versions removes every version
and delete marker under the prefix. Garage implements no versioning, so both are
for a koc pointed at AWS, Ceph RGW or MinIO.`

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
			if err := df.validate(args); err != nil {
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
	fl.StringVar(&df.versionID, "version-id", "",
		"delete this version of the object instead of the current one")
	fl.BoolVar(&df.allVersions, "all-versions", false,
		"delete every version and delete marker, not just the current object")
	df.filter.addTo(fl)
	return cmd
}

// validate rejects the flag combinations that cannot mean anything, and
// compiles the globs so a bad pattern fails before a single key is removed.
func (f *objectDeleteFlags) validate(refs []string) error {
	if f.versionID != "" {
		switch {
		case f.recursive:
			return errors.New("--version-id names one object; it cannot be combined with --recursive")
		case f.allVersions:
			return errors.New("--version-id and --all-versions are mutually exclusive")
		case len(refs) > 1:
			return errors.New("--version-id names one object; pass a single reference")
		}
	}
	if !f.recursive && !f.allVersions && f.filter.active() {
		for _, ref := range refs {
			if hasGlob(ref) {
				return nil
			}
		}
		return errors.New("--include/--exclude filter a listing; pass --recursive or a wildcard reference")
	}
	return f.filter.compile()
}

// runObjectDelete is the test seam for "object delete".
func runObjectDelete(ctx context.Context, client *s3.Client, refs []string,
	f *objectDeleteFlags, w io.Writer) error {
	return batchdelete.Each(refs, func(ref string) error {
		// A wildcard reference is a recursive delete that names its own
		// pattern, so "delete 'b/2026/*.gz'" needs no second flag to say so.
		if f.recursive || f.allVersions || hasGlob(ref) {
			// A bare bucket is a legal prefix ref here — "<bucket>/" is how the
			// whole bucket is named — so parseRef, not parseObjectRef.
			bucket, pattern, err := parseRef(ref)
			if err != nil {
				return err
			}
			return deletePrefix(ctx, client, bucket, pattern, f, w)
		}
		bucket, key, err := parseObjectRef(ref)
		if err != nil {
			return err
		}
		if f.dryRun {
			return sayWouldDelete(w, bucket, key, f.versionID)
		}
		if err := client.DeleteObject(ctx, bucket, key, f.versionID); err != nil {
			return fmt.Errorf("deleting %s: %w", describeTarget(bucket, key, f.versionID), err)
		}
		return sayDeleted(w, bucket, key, f.versionID)
	})
}

// deletePrefix deletes every object under a prefix (or matching a wildcard),
// batching the keys so a large bucket does not cost one round trip per object.
//
// Keys are collected a batch at a time as the listing streams rather than all
// at once, so emptying a bucket of a million objects costs one batch of memory
// rather than all of them.
func deletePrefix(ctx context.Context, client *s3.Client, bucket, pattern string,
	f *objectDeleteFlags, w io.Writer) error {
	var (
		pending []s3.DeleteTarget
		seen    int
	)
	// The literal head of a wildcard is what the server can filter on; the rest
	// of the pattern narrows the result here, against a matcher compiled once
	// rather than per key.
	prefix := globPrefix(pattern)
	var glob *regexp.Regexp
	if hasGlob(pattern) {
		var err error
		if glob, err = compileGlob(pattern); err != nil {
			return fmt.Errorf("%s/%s: %w", bucket, pattern, err)
		}
	}
	match := func(key string) bool {
		if glob != nil && !glob.MatchString(key) {
			return false
		}
		return f.filter.match(key)
	}

	flush := func() error {
		if len(pending) == 0 {
			return nil
		}
		err := deleteBatch(ctx, client, bucket, pending, f.dryRun, w)
		pending = pending[:0]
		return err
	}

	opts := s3.ListOptions{Prefix: prefix, Versions: f.allVersions}
	err := client.ListObjectsFunc(ctx, bucket, opts, func(obj s3.Object) error {
		if !match(obj.Key) {
			return nil
		}
		seen++
		pending = append(pending, s3.DeleteTarget{Key: obj.Key, VersionID: obj.VersionID})
		if len(pending) < s3.MaxDeleteBatch {
			return nil
		}
		return flush()
	})
	if err != nil {
		return fmt.Errorf("deleting %s/%s recursively: %w", bucket, pattern, err)
	}
	if err := flush(); err != nil {
		return err
	}
	if seen == 0 {
		_, err = fmt.Fprintf(w, "No objects under %s/%s\n", bucket, pattern)
		return err
	}
	return nil
}

// deleteBatch removes one batch of keys in a single request, or says what it
// would have removed.
func deleteBatch(ctx context.Context, client *s3.Client, bucket string,
	targets []s3.DeleteTarget, dryRun bool, w io.Writer) error {
	if dryRun {
		for _, t := range targets {
			if err := sayWouldDelete(w, bucket, t.Key, t.VersionID); err != nil {
				return err
			}
		}
		return nil
	}

	failures, err := client.DeleteObjects(ctx, bucket, targets)
	if err != nil {
		return err
	}

	// A batch delete reports per-key refusals inside a 200, so the successes
	// are the targets the server did not name.
	refused := make(map[string]bool, len(failures))
	for _, fail := range failures {
		refused[fail.Key+"\x00"+fail.VersionID] = true
	}
	for _, t := range targets {
		if refused[t.Key+"\x00"+t.VersionID] {
			continue
		}
		if err := sayDeleted(w, bucket, t.Key, t.VersionID); err != nil {
			return err
		}
	}

	errs := make([]error, 0, len(failures))
	for _, fail := range failures {
		errs = append(errs, fmt.Errorf("deleting %s/%s: %w", bucket, fail.Key, fail))
	}
	return errors.Join(errs...)
}

// describeTarget names one delete target for an error message.
func describeTarget(bucket, key, versionID string) string {
	if versionID == "" {
		return bucket + "/" + key
	}
	return fmt.Sprintf("%s/%s (version %s)", bucket, key, versionID)
}

func sayDeleted(w io.Writer, bucket, key, versionID string) error {
	_, err := fmt.Fprintf(w, "Deleted object: %s\n", describeTarget(bucket, key, versionID))
	return err
}

func sayWouldDelete(w io.Writer, bucket, key, versionID string) error {
	_, err := fmt.Fprintf(w, "Would delete object: %s\n", describeTarget(bucket, key, versionID))
	return err
}

package vaultcli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ftarasenko/go-openstackclient/internal/output"
	"github.com/ftarasenko/go-openstackclient/internal/vault"
)

// copyFlags are the flags of "vault kv copy" that are independent of the source
// Vault's connection settings.
type copyFlags struct {
	recursive    bool
	dryRun       bool
	skipExisting bool
	srcVersion   int
}

// copyOptions is the resolved input of the runKVCopy seam.
type copyOptions struct {
	copyFlags
	srcPath    string // resolved KV path within the source mount
	dstPath    string // resolved KV path within the destination mount
	srcDisplay string // the path as typed, for error messages
}

// copyPaths resolves the SRC and DST arguments into paths within their mounts.
// Each side has its own prefix, and both default to the global
// --vault-kv-prefix: the source's is --src-vault-kv-prefix, the destination's is
// --dst-vault-kv-prefix. Neither override touches the other side, so a copy
// between two prefixes of one Vault can name just the one that moves — which
// matters when the global prefix is auto-discovered from the cluster rather than
// typed.
//
// The mounts come from the built clients, not the flags, because auto-discovery
// may have filled them in.
func copyPaths(s *srcFlags, d *dstFlags, globalPrefix, srcMount, dstMount, srcArg, dstArg string) (srcPath, dstPath string) {
	srcPrefix := s.str("src-vault-kv-prefix", "VAULT_SRC_PREFIX", s.kvPrefix, globalPrefix)
	return vault.ResolvePath(srcPrefix, srcMount, srcArg),
		vault.ResolvePath(d.prefix(globalPrefix), dstMount, dstArg)
}

// copy statuses reported in the result table.
const (
	statusCopied  = "copied"
	statusSkipped = "skipped"
	statusWould   = "would copy"
)

// runKVCopy copies one secret, or a whole subtree with --recursive, from the
// source Vault to the destination. The plan is built first (walk, guard,
// skip-existing) so a --dry-run reports exactly the writes the real run would
// perform.
func runKVCopy(ctx context.Context, src, dst *vault.Client, o *output.Options, opts copyOptions, w io.Writer) error {
	srcMount, dstMount := src.KVMount(), dst.KVMount()
	if err := guardSelfCopy(src, dst, opts); err != nil {
		return err
	}

	rels, err := copyPlan(ctx, src, srcMount, opts)
	if err != nil {
		return err
	}
	if opts.srcVersion > 0 && len(rels) > 1 {
		return fmt.Errorf("--src-version applies to a single secret, but %d were selected under %q", len(rels), opts.srcDisplay)
	}

	rows := make([][]any, 0, len(rels))
	var skipped int
	for _, rel := range rels {
		// rel comes from the SOURCE Vault's listing, and it is about to be joined
		// onto the destination path: a key like "../../../prod/openrc" would put the
		// WRITE outside the subtree the operator named, which guardSelfCopy cannot
		// catch because it only sees the paths before the join.
		if err := vault.ValidateRelPath(rel); err != nil {
			return fmt.Errorf("source %q returned an unsafe secret path %q: %w", opts.srcDisplay, rel, err)
		}
		from, to := joinPath(opts.srcPath, rel), joinPath(opts.dstPath, rel)

		if opts.skipExisting {
			exists, err := dst.HasKV(ctx, dstMount, to)
			if err != nil {
				return fmt.Errorf("checking destination %q: %w", to, err)
			}
			if exists {
				skipped++
				rows = append(rows, []any{from, to, 0, statusSkipped})
				continue
			}
		}

		// The source is read even for --dry-run: it makes the reported key count
		// real and proves read access before anybody trusts the preview.
		data, err := src.ReadKVDataAt(ctx, srcMount, from, opts.srcVersion)
		if err != nil {
			return fmt.Errorf("reading source %q: %w", from, err)
		}
		if opts.dryRun {
			rows = append(rows, []any{from, to, len(data), statusWould})
			continue
		}
		if err := dst.WriteKVData(ctx, dstMount, to, data); err != nil {
			return fmt.Errorf("writing destination %q: %w", to, err)
		}
		rows = append(rows, []any{from, to, len(data), statusCopied})
	}

	// The summary goes to stderr so it never pollutes piped/structured output.
	fmt.Fprintf(os.Stderr, "%s: %d secret(s) %s, %d skipped\n",
		describeTarget(src, dst), len(rows)-skipped, copyVerb(opts.dryRun), skipped)

	return o.WriteList(w, output.Table{
		Columns: []string{"Source", "Destination", "Keys", "Status"},
		Rows:    rows,
	})
}

// copyPlan returns the secret paths to copy, relative to the source path: a
// single empty element for a leaf secret, or every leaf of the subtree with -r.
func copyPlan(ctx context.Context, src *vault.Client, srcMount string, opts copyOptions) ([]string, error) {
	if !opts.recursive {
		// Reject a folder early: without this, reading it as a secret fails with a
		// bare 404 that gives no hint about -r.
		keys, err := src.ListKV(ctx, srcMount, opts.srcPath)
		if err != nil && !errors.Is(err, vault.ErrNotFound) {
			return nil, fmt.Errorf("inspecting source %q: %w", opts.srcPath, err)
		}
		if len(keys) > 0 {
			return nil, fmt.Errorf("%q is a folder, not a secret; pass -r to copy the subtree beneath it", opts.srcDisplay)
		}
		return []string{""}, nil
	}

	rels, err := src.WalkKV(ctx, srcMount, opts.srcPath)
	if err != nil {
		return nil, fmt.Errorf("listing source %q: %w", opts.srcPath, err)
	}
	if len(rels) == 0 {
		// -r on a leaf secret: copy it, the way "cp -r file dst" does.
		return []string{""}, nil
	}
	return rels, nil
}

// guardSelfCopy rejects the two ways a copy can eat its own tail: writing a
// secret onto itself, and mirroring a subtree into a path inside itself.
func guardSelfCopy(src, dst *vault.Client, opts copyOptions) error {
	sameVault := src.Addr() == dst.Addr() &&
		src.Namespace() == dst.Namespace() &&
		src.KVMount() == dst.KVMount()
	if !sameVault {
		return nil
	}
	if opts.srcPath == opts.dstPath {
		return fmt.Errorf("source and destination are the same secret (%s in %s)", opts.srcPath, src.KVMount())
	}
	if opts.recursive && strings.HasPrefix(opts.dstPath+"/", opts.srcPath+"/") {
		return fmt.Errorf("destination %q is inside the source subtree %q", opts.dstPath, opts.srcPath)
	}
	return nil
}

// joinPath appends a relative secret path to a base path.
func joinPath(base, rel string) string {
	if rel == "" {
		return base
	}
	return strings.Trim(base, "/") + "/" + rel
}

func copyVerb(dryRun bool) string {
	if dryRun {
		return "to copy"
	}
	return "copied"
}

// describeTarget renders the "where to where" line of the summary, naming the
// source Vault only when it differs from the destination.
func describeTarget(src, dst *vault.Client) string {
	if src.Addr() == dst.Addr() && src.Namespace() == dst.Namespace() {
		return dst.Addr()
	}
	return src.Addr() + " -> " + dst.Addr()
}

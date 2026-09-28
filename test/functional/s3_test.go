//go:build functional

package functional

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// The S3 suite, against swift's s3api with the EC2 credentials up.sh minted:
// bucket lifecycle, single and multipart uploads, standard input, server-side
// copy and move, recursive transfers, sync both ways, du and a presigned URL
// fetched with nothing but net/http.

// s3Runner carries only the S3 credentials: koc s3 must not need Keystone.
func s3Runner(t *testing.T, c *cloud) runner {
	t.Helper()
	if len(c.S3) == 0 {
		t.Skip("functional.env has no S3 credentials")
	}
	return runner{env: append(baseEnv(), c.S3...)}
}

// newBucket creates a bucket and empties and deletes it at the end.
func newBucket(t *testing.T, r runner) string {
	t.Helper()
	b := uniq("bkt")
	r.ok(t, "s3", "bucket", "create", b)
	t.Cleanup(func() {
		r.run(t, "s3", "object", "delete", b+"/", "--recursive")
		r.run(t, "s3", "bucket", "delete", b)
	})
	return b
}

func keys(rows []map[string]any) []string { return column(rows, "key") }

// hasKey reports whether a listing has a key ending in suffix, so a check reads
// the same whether the listing prints keys whole or relative to its prefix.
func hasKey(keys []string, suffix string) bool {
	return slices.ContainsFunc(keys, func(k string) bool { return strings.HasSuffix(k, suffix) })
}

func TestS3Buckets(t *testing.T) {
	c := ft(t)
	requireFeature(t, "core")
	r := s3Runner(t, c)

	b := newBucket(t, r)
	if !slices.Contains(column(r.list(t, "s3", "bucket", "list"), "name"), b) {
		t.Errorf("s3 bucket list does not list %s", b)
	}
	if got := r.show(t, "s3", "bucket", "show", b); field(got, "bucket") != b {
		t.Errorf("s3 bucket show = %v", got)
	}
	// Versioning needs swift's object versioning, which devstack may leave off;
	// either way the answer is the store's.
	if res := r.run(t, "s3", "bucket", "set", b, "--versioning", "Suspended"); res.code != 0 {
		t.Logf("s3 bucket set --versioning: %s", strings.TrimSpace(res.stderr))
	}

	// A bucket with an object in it cannot go.
	r.withStdin("x").ok(t, "s3", "upload", "-", b+"/one")
	r.fails(t, "s3", "bucket", "delete", b)
	r.ok(t, "s3", "object", "delete", b+"/one")
	r.ok(t, "s3", "bucket", "delete", b)
	r.fails(t, "s3", "bucket", "show", b)
}

func TestS3Objects(t *testing.T) {
	c := ft(t)
	requireFeature(t, "core")
	r := s3Runner(t, c)
	b := newBucket(t, r)
	dir := t.TempDir()

	// Single-part, multipart (6 MiB in 5 MiB parts — s3api's floor) and stdin.
	small, smallBody := payload(t, 4096)
	big, bigBody := payload(t, 6<<20)
	r.ok(t, "s3", "upload", small, b+"/dir/small.bin", "--content-type", "application/x-ft")
	r.ok(t, "s3", "upload", big, b+"/dir/big.bin", "--part-size", "5")
	r.withStdin("from stdin\n").ok(t, "s3", "upload", "-", b+"/stdin.txt")
	// --no-clobber skips a key that is there: different bytes, same object.
	other, _ := payload(t, 100)
	r.ok(t, "s3", "upload", other, b+"/dir/small.bin", "--no-clobber")

	obj := r.show(t, "s3", "object", "show", b+"/dir/small.bin")
	if field(obj, "size") != "4096" || field(obj, "content type") != "application/x-ft" {
		t.Errorf("s3 object show = %v", obj)
	}
	if got := keys(r.list(t, "s3", "object", "list", b+"/dir/")); !hasKey(got, "big.bin") || !hasKey(got, "small.bin") {
		t.Errorf("s3 object list %s/dir/ = %v", b, got)
	}

	// Downloads: to a file, and to stdout.
	out := filepath.Join(dir, "big.bin")
	r.ok(t, "s3", "download", b+"/dir/big.bin", out)
	if got, err := os.ReadFile(out); err != nil || !bytes.Equal(got, bigBody) {
		t.Errorf("multipart round trip: %d bytes, err %v; want the %d uploaded", len(got), err, len(bigBody))
	}
	if got := r.ok(t, "s3", "download", b+"/stdin.txt", "-"); got != "from stdin\n" {
		t.Errorf("s3 download to stdout = %q", got)
	}

	// Server-side copy and move.
	r.ok(t, "s3", "copy", b+"/dir/small.bin", b+"/copied.bin")
	r.ok(t, "s3", "move", b+"/copied.bin", b+"/moved.bin")
	got := keys(r.list(t, "s3", "object", "list", b))
	if !hasKey(got, "moved.bin") || hasKey(got, "copied.bin") {
		t.Errorf("after copy and move: %v", got)
	}
	r.ok(t, "s3", "download", b+"/moved.bin", filepath.Join(dir, "moved.bin"))
	if moved, _ := os.ReadFile(filepath.Join(dir, "moved.bin")); !bytes.Equal(moved, smallBody) {
		t.Error("the moved object's bytes differ from the original")
	}

	// du totals the prefix.
	if du := r.show(t, "s3", "du", b+"/dir/"); field(du, "objects") != "2" {
		t.Errorf("s3 du %s/dir/ = %v", b, du)
	}

	// A presigned GET works with no credentials at all.
	ps := r.show(t, "s3", "presign", b+"/stdin.txt", "--expire", "5m")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, field(ps, "url"), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("fetching the presigned URL: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "from stdin\n" {
		t.Errorf("presigned GET: %d %q", resp.StatusCode, body)
	}

	// Recursive delete of a prefix leaves the rest.
	r.ok(t, "s3", "object", "delete", b+"/dir/", "--recursive")
	if got := keys(r.list(t, "s3", "object", "list", b)); len(got) != 2 {
		t.Errorf("after deleting dir/ recursively: %v, want moved.bin and stdin.txt", got)
	}
}

func TestS3SyncAndRecursive(t *testing.T) {
	c := ft(t)
	requireFeature(t, "core")
	r := s3Runner(t, c)
	b := newBucket(t, r)

	src := t.TempDir()
	writeFile(t, src, "a.txt", "a")
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(src, "sub"), "b.txt", "b")

	r.ok(t, "s3", "upload", src, b+"/up/", "--recursive")
	if got := keys(r.list(t, "s3", "object", "list", b+"/up/")); !hasKey(got, "a.txt") || !hasKey(got, "sub/b.txt") {
		t.Errorf("recursive upload: %v", got)
	}
	r.ok(t, "s3", "copy", b+"/up/", b+"/copy/", "--recursive")
	down := t.TempDir()
	r.ok(t, "s3", "download", b+"/copy/", down, "--recursive")
	if got, err := os.ReadFile(filepath.Join(down, "sub", "b.txt")); err != nil || string(got) != "b" {
		t.Errorf("recursive download: %q, %v", got, err)
	}

	// sync up, change the tree, sync again with --delete, then sync down.
	r.ok(t, "s3", "sync", src, "s3://"+b+"/sync/")
	writeFile(t, src, "c.txt", "c")
	if err := os.Remove(filepath.Join(src, "a.txt")); err != nil {
		t.Fatal(err)
	}
	r.ok(t, "s3", "sync", src, "s3://"+b+"/sync/", "--delete")
	got := keys(r.list(t, "s3", "object", "list", b+"/sync/"))
	if !hasKey(got, "c.txt") || hasKey(got, "a.txt") {
		t.Errorf("after sync --delete: %v", got)
	}
	back := t.TempDir()
	r.ok(t, "s3", "sync", "s3://"+b+"/sync/", back)
	if c, err := os.ReadFile(filepath.Join(back, "c.txt")); err != nil || string(c) != "c" {
		t.Errorf("sync down: %q, %v", c, err)
	}
}

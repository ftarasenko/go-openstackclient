package s3cli

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/ftarasenko/go-openstackclient/internal/s3"
)

func testCopyFlags() *copyFlags { return &copyFlags{concurrency: 1} }

func TestRunCopySingleObject(t *testing.T) {
	var gotPath, gotSource string
	client := newMockClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotSource = r.URL.Path, r.Header.Get("x-amz-copy-source")
		_, _ = fmt.Fprint(w, `<CopyObjectResult><ETag>"e"</ETag></CopyObjectResult>`)
	})

	var buf bytes.Buffer
	src := s3.ObjectRef{Bucket: "db-backups", Key: "nightly.sql.gz"}
	dst := s3.ObjectRef{Bucket: "db-backups", Key: "latest.sql.gz"}
	if err := runCopy(context.Background(), client, valueOpts(), src, dst, testCopyFlags(), &buf); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/db-backups/latest.sql.gz" {
		t.Errorf("path = %q, want the destination", gotPath)
	}
	if gotSource != "/db-backups/nightly.sql.gz" {
		t.Errorf("copy source = %q", gotSource)
	}
	if got, want := buf.String(), "db-backups/nightly.sql.gz\ndb-backups/latest.sql.gz\n"; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

// A destination ending in "/" — or naming only a bucket — takes the source's
// basename, the same rule "upload" follows.
func TestRunCopyDestinationPrefixTakesTheBasename(t *testing.T) {
	var gotPath string
	client := newMockClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = fmt.Fprint(w, `<CopyObjectResult><ETag>"e"</ETag></CopyObjectResult>`)
	})

	var buf bytes.Buffer
	src := s3.ObjectRef{Bucket: "db-backups", Key: "2026/08/a.sql.gz"}
	dst := s3.ObjectRef{Bucket: "archive", Key: "restored/"}
	if err := runCopy(context.Background(), client, valueOpts(), src, dst, testCopyFlags(), &buf); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/archive/restored/a.sql.gz" {
		t.Errorf("path = %q, want the basename appended", gotPath)
	}
}

// A move is the copy plus a delete of the source, in that order: the source is
// only removed once the copy has landed.
func TestRunMoveCopiesThenDeletes(t *testing.T) {
	var order []string
	client := newMockClient(t, func(w http.ResponseWriter, r *http.Request) {
		order = append(order, r.Method+" "+r.URL.Path)
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = fmt.Fprint(w, `<CopyObjectResult><ETag>"e"</ETag></CopyObjectResult>`)
	})

	f := testCopyFlags()
	f.remove = true
	var buf bytes.Buffer
	src := s3.ObjectRef{Bucket: "db-backups", Key: "a.sql.gz"}
	dst := s3.ObjectRef{Bucket: "archive", Key: "a.sql.gz"}
	if err := runCopy(context.Background(), client, valueOpts(), src, dst, f, &buf); err != nil {
		t.Fatal(err)
	}
	want := "PUT /archive/a.sql.gz,DELETE /db-backups/a.sql.gz"
	if got := strings.Join(order, ","); got != want {
		t.Errorf("requests = %q, want %q", got, want)
	}
}

// A failed copy must leave the source alone: losing the object is the one
// outcome a move must never have.
func TestRunMoveKeepsTheSourceWhenTheCopyFails(t *testing.T) {
	var deleted bool
	client := newMockClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleted = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprint(w, `<Error><Code>AccessDenied</Code></Error>`)
	})

	f := testCopyFlags()
	f.remove = true
	var buf bytes.Buffer
	src := s3.ObjectRef{Bucket: "db-backups", Key: "a.sql.gz"}
	dst := s3.ObjectRef{Bucket: "archive", Key: "a.sql.gz"}
	if err := runCopy(context.Background(), client, valueOpts(), src, dst, f, &buf); err == nil {
		t.Fatal("a refused copy was reported as a successful move")
	}
	if deleted {
		t.Fatal("the source was deleted even though the copy failed")
	}
}

// A recursive copy mirrors each key's path below the source prefix.
func TestRunCopyRecursive(t *testing.T) {
	var copies []string
	client := newMockClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("list-type") {
			_, _ = fmt.Fprint(w, `<ListBucketResult><IsTruncated>false</IsTruncated>
				<Contents><Key>2026/08/a.sql.gz</Key></Contents>
				<Contents><Key>2026/09/b.sql.gz</Key></Contents>
				</ListBucketResult>`)
			return
		}
		copies = append(copies, r.Header.Get("x-amz-copy-source")+" -> "+r.URL.Path)
		_, _ = fmt.Fprint(w, `<CopyObjectResult><ETag>"e"</ETag></CopyObjectResult>`)
	})

	f := testCopyFlags()
	f.recursive = true
	var buf bytes.Buffer
	src := s3.ObjectRef{Bucket: "db-backups", Key: "2026/"}
	dst := s3.ObjectRef{Bucket: "archive", Key: "old/"}
	if err := runCopy(context.Background(), client, valueOpts(), src, dst, f, &buf); err != nil {
		t.Fatal(err)
	}
	sort.Strings(copies)
	want := []string{
		"/db-backups/2026/08/a.sql.gz -> /archive/old/08/a.sql.gz",
		"/db-backups/2026/09/b.sql.gz -> /archive/old/09/b.sql.gz",
	}
	if got := strings.Join(copies, "|"); got != strings.Join(want, "|") {
		t.Errorf("copies = %q, want %q", got, want)
	}
}

// A wildcard source is its own recursion.
func TestRunCopyWildcardSource(t *testing.T) {
	var copies []string
	client := newMockClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("list-type") {
			_, _ = fmt.Fprint(w, `<ListBucketResult><IsTruncated>false</IsTruncated>
				<Contents><Key>a.sql.gz</Key></Contents>
				<Contents><Key>a.sha256</Key></Contents>
				</ListBucketResult>`)
			return
		}
		copies = append(copies, r.URL.Path)
		_, _ = fmt.Fprint(w, `<CopyObjectResult><ETag>"e"</ETag></CopyObjectResult>`)
	})

	var buf bytes.Buffer
	src := s3.ObjectRef{Bucket: "db-backups", Key: "*.sha256"}
	dst := s3.ObjectRef{Bucket: "checksums", Key: ""}
	if err := runCopy(context.Background(), client, valueOpts(), src, dst, testCopyFlags(), &buf); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(copies, ","), "/checksums/a.sha256"; got != want {
		t.Errorf("copies = %q, want only the pattern match", got)
	}
}

// Copying an object onto itself is refused by S3 unless metadata changes, and
// in a recursive run over the same prefix it is a mistake, not a request.
func TestRunCopyRecursiveSkipsIdentityCopies(t *testing.T) {
	var copies int
	client := newMockClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("list-type") {
			_, _ = fmt.Fprint(w, `<ListBucketResult><IsTruncated>false</IsTruncated>
				<Contents><Key>a.sql.gz</Key></Contents></ListBucketResult>`)
			return
		}
		copies++
		_, _ = fmt.Fprint(w, `<CopyObjectResult><ETag>"e"</ETag></CopyObjectResult>`)
	})

	f := testCopyFlags()
	f.recursive = true
	var buf bytes.Buffer
	ref := s3.ObjectRef{Bucket: "db-backups", Key: ""}
	if err := runCopy(context.Background(), client, valueOpts(), ref, ref, f, &buf); err != nil {
		t.Fatal(err)
	}
	if copies != 0 {
		t.Errorf("copies = %d, want none — every key maps onto itself", copies)
	}
	if !strings.Contains(buf.String(), "No objects under") {
		t.Errorf("output = %q, want it to say nothing was copied", buf.String())
	}
}

func TestRunCopyRecursiveDryRun(t *testing.T) {
	client := newMockClient(t, func(w http.ResponseWriter, r *http.Request) {
		if !r.URL.Query().Has("list-type") {
			t.Errorf("--dry-run sent %s %s", r.Method, r.URL.Path)
		}
		_, _ = fmt.Fprint(w, `<ListBucketResult><IsTruncated>false</IsTruncated>
			<Contents><Key>a.sql.gz</Key></Contents></ListBucketResult>`)
	})

	f := testCopyFlags()
	f.recursive, f.dryRun, f.remove = true, true, true
	var buf bytes.Buffer
	src := s3.ObjectRef{Bucket: "db-backups", Key: ""}
	dst := s3.ObjectRef{Bucket: "archive", Key: ""}
	if err := runCopy(context.Background(), client, valueOpts(), src, dst, f, &buf); err != nil {
		t.Fatal(err)
	}
	want := "Would move: db-backups/a.sql.gz -> archive/a.sql.gz\n"
	if got := buf.String(); got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

func TestParseCopyRefsValidation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		src, dst string
		flags    copyFlags
		want     string
	}{
		{"no source key", "b", "b/k", copyFlags{}, "names no object"},
		{"same object", "b/k", "b/k", copyFlags{}, "the same object"},
		{"version with recursive", "b/p", "b/q", copyFlags{recursive: true, versionID: "v"}, "--recursive"},
		{"filters without recursion", "b/k", "b/j",
			copyFlags{filter: keyFilter{include: []string{"*"}}}, "--recursive"},
		{"flatten without recursion", "b/k", "b/j", copyFlags{flatten: true}, "--flatten"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			flags := tc.flags
			if _, _, err := parseCopyRefs(tc.src, tc.dst, &flags); err == nil ||
				!strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}

	// The s3:// spelling is accepted on both sides.
	src, dst, err := parseCopyRefs("s3://a/k", "s3://b/j", &copyFlags{})
	if err != nil {
		t.Fatal(err)
	}
	if src.Bucket != "a" || dst.Bucket != "b" {
		t.Errorf("refs = %s, %s", src, dst)
	}
}

func TestCopyDestKey(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		dstPrefix, srcPrefix, key string
		flatten                   bool
		want                      string
	}{
		{"mirrors below the prefix", "old/", "2026/", "2026/08/a.gz", false, "old/08/a.gz"},
		{"flattened", "old/", "2026/", "2026/08/a.gz", true, "old/a.gz"},
		{"no destination prefix", "", "2026/", "2026/a.gz", false, "a.gz"},
		{"destination without a slash", "old", "", "a.gz", false, "old/a.gz"},
		{"prefix is not a path boundary", "old/", "nightly-", "nightly-a.gz", false, "old/a.gz"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := copyDestKey(tc.dstPrefix, tc.srcPrefix, tc.key, tc.flatten); got != tc.want {
				t.Errorf("copyDestKey = %q, want %q", got, tc.want)
			}
		})
	}
}

// keyBase must split on "/" only: a backslash is a legal character in an S3
// key, and filepath.Base would mangle it on Windows.
func TestKeyBase(t *testing.T) {
	for _, tc := range []struct{ key, want string }{
		{"a/b/c.gz", "c.gz"},
		{"c.gz", "c.gz"},
		{`weird\name.gz`, `weird\name.gz`},
	} {
		if got := keyBase(tc.key); got != tc.want {
			t.Errorf("keyBase(%q) = %q, want %q", tc.key, got, tc.want)
		}
	}
}

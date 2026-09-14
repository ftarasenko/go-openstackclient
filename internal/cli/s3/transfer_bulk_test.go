package s3cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/ftarasenko/go-openstackclient/internal/s3"
)

// listingHandler serves one page of the given <Contents> and the bytes "data"
// for every object GET — enough to drive a recursive transfer.
func listingHandler(t *testing.T, contents string, seen *[]string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("list-type") {
			_, _ = fmt.Fprint(w, `<ListBucketResult><IsTruncated>false</IsTruncated>`+contents+`</ListBucketResult>`)
			return
		}
		if seen != nil {
			*seen = append(*seen, r.URL.Path)
		}
		_, _ = w.Write([]byte("data"))
	}
}

// sortedLines makes an assertion independent of completion order, which with
// more than one worker is not the listing order.
func sortedLines(s string) []string {
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	sort.Strings(lines)
	return lines
}

// A recursive download mirrors each key's path below the prefix, so a restore
// reproduces the layout rather than a flat pile of basenames.
func TestRunDownloadRecursiveMirrorsKeyPaths(t *testing.T) {
	client := newMockClient(t, listingHandler(t,
		`<Contents><Key>2026/08/a.sql.gz</Key><Size>4</Size></Contents>`+
			`<Contents><Key>2026/09/b.sql.gz</Key><Size>4</Size></Contents>`, nil))

	dir := t.TempDir()
	var buf bytes.Buffer
	f := testDownloadFlags()
	f.recursive = true
	err := runDownload(context.Background(), client, valueOpts(),
		downloadRequest{bucket: "db-backups", key: "2026/", dest: dir, flags: f}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"08/a.sql.gz", "09/b.sql.gz"} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			t.Errorf("%s was not written: %v", rel, err)
		}
	}
}

// --flatten puts every object straight in the destination.
func TestRunDownloadRecursiveFlatten(t *testing.T) {
	client := newMockClient(t, listingHandler(t,
		`<Contents><Key>2026/08/a.sql.gz</Key><Size>4</Size></Contents>`, nil))

	dir := t.TempDir()
	var buf bytes.Buffer
	f := testDownloadFlags()
	f.recursive, f.flatten = true, true
	err := runDownload(context.Background(), client, valueOpts(),
		downloadRequest{bucket: "db-backups", key: "2026/", dest: dir, flags: f}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.sql.gz")); err != nil {
		t.Errorf("the object was not flattened into the destination: %v", err)
	}
}

// A wildcard reference is its own recursion, and narrows what the prefix took.
func TestRunDownloadWildcard(t *testing.T) {
	var fetched []string
	client := newMockClient(t, listingHandler(t,
		`<Contents><Key>2026/a.sql.gz</Key><Size>4</Size></Contents>`+
			`<Contents><Key>2026/a.sha256</Key><Size>4</Size></Contents>`, &fetched))

	dir := t.TempDir()
	var buf bytes.Buffer
	err := runDownload(context.Background(), client, valueOpts(),
		downloadRequest{bucket: "db-backups", key: "2026/*.sql.gz", dest: dir, flags: testDownloadFlags()}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(fetched, ","), "/db-backups/2026/a.sql.gz"; got != want {
		t.Errorf("fetched = %q, want only the pattern match", got)
	}
}

// In bulk mode an existing file is a resumption point, not a failure: a restore
// that was interrupted is finished by re-running the same command.
func TestRunDownloadRecursiveSkipsExistingFiles(t *testing.T) {
	var fetched []string
	client := newMockClient(t, listingHandler(t,
		`<Contents><Key>a.sql.gz</Key><Size>4</Size></Contents>`+
			`<Contents><Key>b.sql.gz</Key><Size>4</Size></Contents>`, &fetched))

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.sql.gz"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	f := testDownloadFlags()
	f.recursive = true
	err := runDownload(context.Background(), client, valueOpts(),
		downloadRequest{bucket: "db-backups", key: "", dest: dir, flags: f}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(fetched, ","), "/db-backups/b.sql.gz"; got != want {
		t.Errorf("fetched = %q, want only the missing object", got)
	}
	if !strings.Contains(buf.String(), "Skipped (exists)") {
		t.Errorf("output %q does not report the skip", buf.String())
	}
	// The existing file must be untouched, not truncated.
	if body, _ := os.ReadFile(filepath.Join(dir, "a.sql.gz")); string(body) != "old" {
		t.Errorf("the existing file was overwritten: %q", body)
	}
}

// An object key is server-supplied data: "../../etc/x" is a legal S3 key, and a
// restore that wrote it where it says would let whoever can write to the bucket
// write anywhere koc can.
func TestRunDownloadRecursiveRefusesEscapingKeys(t *testing.T) {
	client := newMockClient(t, listingHandler(t,
		`<Contents><Key>../escaped.sh</Key><Size>4</Size></Contents>`, nil))

	dir := filepath.Join(t.TempDir(), "restore")
	var buf bytes.Buffer
	f := testDownloadFlags()
	f.recursive = true
	err := runDownload(context.Background(), client, valueOpts(),
		downloadRequest{bucket: "db-backups", key: "", dest: dir, flags: f}, &buf)
	if err == nil {
		t.Fatal("a key escaping the destination was accepted")
	}
	if !strings.Contains(err.Error(), "outside") {
		t.Errorf("error %q does not say what was refused", err)
	}
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(dir), "escaped.sh")); statErr == nil {
		t.Fatal("the escaping key was written")
	}
}

// A "directory marker" — a zero-length key ending in "/" — must not become a
// file, or it would shadow the directory its siblings need.
func TestRunDownloadRecursiveSkipsDirectoryMarkers(t *testing.T) {
	var fetched []string
	client := newMockClient(t, listingHandler(t,
		`<Contents><Key>2026/</Key><Size>0</Size></Contents>`+
			`<Contents><Key>2026/a.sql.gz</Key><Size>4</Size></Contents>`, &fetched))

	dir := t.TempDir()
	var buf bytes.Buffer
	f := testDownloadFlags()
	f.recursive = true
	err := runDownload(context.Background(), client, valueOpts(),
		downloadRequest{bucket: "db-backups", key: "", dest: dir, flags: f}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(fetched, ","), "/db-backups/2026/a.sql.gz"; got != want {
		t.Errorf("fetched = %q, want the marker skipped", got)
	}
}

func TestRunDownloadRecursiveNoMatches(t *testing.T) {
	client := newMockClient(t, listingHandler(t, "", nil))

	var buf bytes.Buffer
	f := testDownloadFlags()
	f.recursive = true
	err := runDownload(context.Background(), client, valueOpts(),
		downloadRequest{bucket: "db-backups", key: "gone/", dest: t.TempDir(), flags: f}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := buf.String(), "No objects under db-backups/gone/\n"; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

// The combinations that cannot mean anything must be refused when the arguments
// are parsed, before a client is even built.
func TestNewDownloadRequestValidation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  []string
		flags downloadFlags
		want  string
	}{
		{"recursive to stdout", []string{"b/k", "-"}, downloadFlags{recursive: true}, "not stdout"},
		{"recursive with a version", []string{"b/k", "d"}, downloadFlags{recursive: true, versionID: "v"}, "--recursive"},
		{"no key", []string{"b"}, downloadFlags{}, "names no object"},
		{"filters without recursion", []string{"b/k"},
			downloadFlags{filter: keyFilter{include: []string{"*"}}}, "--recursive"},
		{"flatten without recursion", []string{"b/k"}, downloadFlags{flatten: true}, "--flatten"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			flags := tc.flags
			_, err := newDownloadRequest(tc.args, &flags)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// A recursive upload mirrors the local tree under the destination prefix.
func TestRunUploadRecursive(t *testing.T) {
	var puts []string
	client := newMockClient(t, func(w http.ResponseWriter, r *http.Request) {
		puts = append(puts, r.URL.Path)
		w.Header().Set("ETag", `"e"`)
		w.WriteHeader(http.StatusOK)
	})

	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "a.sql.gz"), "one")
	mustWrite(t, filepath.Join(root, "nested", "b.sql.gz"), "two")

	var buf bytes.Buffer
	f := testUploadFlags()
	f.recursive = true
	err := runUpload(context.Background(), client, valueOpts(),
		uploadRequest{src: root, bucket: "db-backups", key: "2026/", flags: f}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(puts)
	want := "/db-backups/2026/a.sql.gz,/db-backups/2026/nested/b.sql.gz"
	if got := strings.Join(puts, ","); got != want {
		t.Errorf("uploaded = %q, want %q", got, want)
	}
}

// --include selects files by the key they would get, so a filter reads the same
// way as it does on a listing.
func TestRunUploadRecursiveFilters(t *testing.T) {
	var puts []string
	client := newMockClient(t, func(w http.ResponseWriter, r *http.Request) {
		puts = append(puts, r.URL.Path)
		w.Header().Set("ETag", `"e"`)
		w.WriteHeader(http.StatusOK)
	})

	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "a.sql.gz"), "one")
	mustWrite(t, filepath.Join(root, "a.sha256"), "two")

	var buf bytes.Buffer
	f := testUploadFlags()
	f.recursive = true
	f.filter = keyFilter{include: []string{"*.sql.gz"}}
	if err := f.filter.compile(); err != nil {
		t.Fatal(err)
	}
	err := runUpload(context.Background(), client, valueOpts(),
		uploadRequest{src: root, bucket: "db-backups", key: "", flags: f}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(puts, ","), "/db-backups/a.sql.gz"; got != want {
		t.Errorf("uploaded = %q, want %q", got, want)
	}
}

// --no-clobber costs one HEAD and leaves an object that is already there alone,
// which is how an interrupted bulk upload resumes.
func TestRunUploadNoClobberSkipsExisting(t *testing.T) {
	var methods []string
	client := newMockClient(t, func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method+" "+r.URL.Path)
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", "3")
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("ETag", `"e"`)
		w.WriteHeader(http.StatusOK)
	})

	src := filepath.Join(t.TempDir(), "dump.sql.gz")
	mustWrite(t, src, "payload")

	var buf bytes.Buffer
	f := testUploadFlags()
	f.noClobber = true
	err := runUpload(context.Background(), client, valueOpts(),
		uploadRequest{src: src, bucket: "db-backups", key: "dump.sql.gz", flags: f}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(methods, ","), "HEAD /db-backups/dump.sql.gz"; got != want {
		t.Errorf("requests = %q, want only the existence check", got)
	}
	if !strings.Contains(buf.String(), "Skipped (exists)") {
		t.Errorf("output %q does not report the skip", buf.String())
	}
}

// Standard input has no length, so it must take the multipart path — and it
// needs a full key, since a stream has no basename to fall back on.
func TestRunUploadStdinNeedsAFullKey(t *testing.T) {
	client := newMockClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("no request should be made without a key")
		w.WriteHeader(http.StatusOK)
	})

	var buf bytes.Buffer
	err := runUpload(context.Background(), client, valueOpts(),
		uploadRequest{src: "-", bucket: "db-backups", key: "", flags: testUploadFlags()}, &buf)
	if err == nil || !strings.Contains(err.Error(), "full key") {
		t.Errorf("err = %v, want it to ask for a full key", err)
	}
}

func TestUploadFlagValidation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		req   uploadRequest
		flags uploadFlags
		want  string
	}{
		{"stdin with recursive", uploadRequest{src: "-"}, uploadFlags{recursive: true}, "--recursive"},
		{"flatten without recursion", uploadRequest{src: "x"}, uploadFlags{flatten: true}, "--flatten"},
		{"filters without recursion", uploadRequest{src: "x"},
			uploadFlags{filter: keyFilter{exclude: []string{"*"}}}, "--recursive"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			flags := tc.flags
			if err := flags.validate(tc.req); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// An object past the part size has to go out as a multipart upload, or the
// 5 GiB single-PUT ceiling would apply.
func TestRunUploadUsesMultipartPastThePartSize(t *testing.T) {
	var started bool
	var parts int
	client := newMockClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case q.Has("uploads"):
			started = true
			_, _ = fmt.Fprint(w, `<InitiateMultipartUploadResult><UploadId>u</UploadId></InitiateMultipartUploadResult>`)
		case q.Has("partNumber"):
			parts++
			_, _ = io.Copy(io.Discard, r.Body)
			w.Header().Set("ETag", fmt.Sprintf(`"p%s"`, q.Get("partNumber")))
			w.WriteHeader(http.StatusOK)
		default:
			_, _ = fmt.Fprint(w, `<CompleteMultipartUploadResult><ETag>"final"</ETag></CompleteMultipartUploadResult>`)
		}
	})

	src := filepath.Join(t.TempDir(), "big.bin")
	mustWrite(t, src, strings.Repeat("x", s3.MinPartSize+1024))

	var buf bytes.Buffer
	f := testUploadFlags()
	f.partSizeMiB = s3.MinPartSize >> 20 // 5 MiB, the protocol minimum
	err := runUpload(context.Background(), client, valueOpts(),
		uploadRequest{src: src, bucket: "db-backups", key: "big.bin", flags: f}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if !started {
		t.Error("an object past the part size was sent as a single PUT")
	}
	if parts != 2 {
		t.Errorf("parts = %d, want 2", parts)
	}
	if !strings.Contains(buf.String(), "final") {
		t.Errorf("output %q does not carry the completed ETag", buf.String())
	}
}

// --content-type overrides the extension guess for the whole run.
func TestRunUploadContentTypeOverride(t *testing.T) {
	var gotType string
	client := newMockClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotType = r.Header.Get("Content-Type")
		w.Header().Set("ETag", `"e"`)
		w.WriteHeader(http.StatusOK)
	})

	src := filepath.Join(t.TempDir(), "dump.txt")
	mustWrite(t, src, "payload")

	var buf bytes.Buffer
	f := testUploadFlags()
	f.contentType = "application/x-custom"
	err := runUpload(context.Background(), client, valueOpts(),
		uploadRequest{src: src, bucket: "b", key: "k", flags: f}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if gotType != "application/x-custom" {
		t.Errorf("Content-Type = %q, want the override", gotType)
	}
}

// --dry-run must name every transfer it would make and make none.
func TestRunUploadRecursiveDryRun(t *testing.T) {
	client := newMockClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("--dry-run sent %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusOK)
	})

	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "a.sql.gz"), "one")

	var buf bytes.Buffer
	f := testUploadFlags()
	f.recursive, f.dryRun = true, true
	err := runUpload(context.Background(), client, valueOpts(),
		uploadRequest{src: root, bucket: "db-backups", key: "p/", flags: f}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if got := sortedLines(buf.String()); len(got) != 1 || !strings.Contains(got[0], "Would upload") {
		t.Errorf("output = %q, want one 'Would upload' line", buf.String())
	}
}

func TestKeyForFile(t *testing.T) {
	root := filepath.Join("tmp", "src")
	for _, tc := range []struct {
		name, prefix, path string
		flatten            bool
		want               string
	}{
		{"mirrors the tree", "2026/", filepath.Join(root, "a", "b.gz"), false, "2026/a/b.gz"},
		{"no prefix", "", filepath.Join(root, "a", "b.gz"), false, "a/b.gz"},
		{"flattened", "2026/", filepath.Join(root, "a", "b.gz"), true, "2026/b.gz"},
		{"prefix without a slash", "2026", filepath.Join(root, "b.gz"), false, "2026/b.gz"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := keyForFile(tc.prefix, root, tc.path, tc.flatten)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("keyForFile = %q, want %q", got, tc.want)
			}
		})
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

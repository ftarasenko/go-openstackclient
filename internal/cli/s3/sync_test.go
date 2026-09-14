package s3cli

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ftarasenko/go-openstackclient/internal/s3"
)

func testSyncFlags() *syncFlags {
	return &syncFlags{concurrency: 1, partSizeMiB: s3.DefaultPartSize >> 20}
}

// syncMock serves a listing whose objects carry a size and a Last-Modified, and
// records every write it is sent.
type syncMock struct {
	objects map[string]syncObject
	puts    []string
	deletes []string
	copies  []string
	gets    []string
}

type syncObject struct {
	size  int64
	mtime string
}

func (m *syncMock) handler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case q.Has("list-type"):
			prefix := q.Get("prefix")
			var rows string
			for key, o := range m.objects {
				if !strings.HasPrefix(key, prefix) {
					continue
				}
				rows += fmt.Sprintf(`<Contents><Key>%s</Key><Size>%d</Size>`+
					`<LastModified>%s</LastModified><ETag>"e"</ETag></Contents>`, key, o.size, o.mtime)
			}
			_, _ = fmt.Fprintf(w, `<ListBucketResult><IsTruncated>false</IsTruncated>%s</ListBucketResult>`, rows)
		case r.Method == http.MethodPost && q.Has("delete"):
			body := readAll(r)
			m.deletes = append(m.deletes, keysInBatch(body)...)
			_, _ = fmt.Fprint(w, `<DeleteResult></DeleteResult>`)
		case r.Method == http.MethodPut && r.Header.Get("x-amz-copy-source") != "":
			m.copies = append(m.copies, r.Header.Get("x-amz-copy-source")+" -> "+r.URL.Path)
			_, _ = fmt.Fprint(w, `<CopyObjectResult><ETag>"e"</ETag></CopyObjectResult>`)
		case r.Method == http.MethodPut:
			m.puts = append(m.puts, r.URL.Path)
			w.Header().Set("ETag", `"e"`)
			w.WriteHeader(http.StatusOK)
		default:
			m.gets = append(m.gets, r.URL.Path)
			_, _ = w.Write([]byte("data"))
		}
	}
}

func readAll(r *http.Request) string {
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r.Body)
	return buf.String()
}

func localSide(dir string) syncEndpoint { return syncEndpoint{dir: dir} }
func s3Side(b, p string) syncEndpoint   { return syncEndpoint{bucket: b, prefix: p} }

// The s3:// prefix is mandatory on this command alone, because mistaking which
// side is local is how a --delete removes the wrong one.
func TestParseSyncEndpoints(t *testing.T) {
	src, dst, err := parseSyncEndpoints("./restore", "s3://db-backups/2026")
	if err != nil {
		t.Fatal(err)
	}
	if !src.isLocal() || src.dir != "./restore" {
		t.Errorf("source = %+v, want the local path", src)
	}
	// A prefix is a directory, so the trailing slash is supplied.
	if dst.bucket != "db-backups" || dst.prefix != "2026/" {
		t.Errorf("destination = %+v, want prefix 2026/", dst)
	}
	// A bare bucket has an empty prefix rather than "/".
	remote, err := parseSyncEndpoint("s3://db-backups")
	if err != nil {
		t.Fatal(err)
	}
	if remote.prefix != "" {
		t.Errorf("prefix = %q, want empty", remote.prefix)
	}
}

func TestParseSyncEndpointsRejectsBadPairs(t *testing.T) {
	for _, tc := range []struct{ name, src, dst, want string }{
		{"two local paths", "./a", "./b", "at least one side"},
		{"same location", "s3://b/p", "s3://b/p/", "the same location"},
		{"no bucket", "s3://", "./a", "names no bucket"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := parseSyncEndpoints(tc.src, tc.dst); err == nil ||
				!strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// The comparison rule: absent, a different size, or a newer source.
func TestNeedsTransfer(t *testing.T) {
	old := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	recent := old.Add(time.Hour)

	for _, tc := range []struct {
		name     string
		src, dst syncEntry
		sizeOnly bool
		want     bool
	}{
		{"same size and time", syncEntry{size: 10, mtime: old}, syncEntry{size: 10, mtime: old}, false, false},
		{"different size", syncEntry{size: 11, mtime: old}, syncEntry{size: 10, mtime: old}, false, true},
		{"source newer", syncEntry{size: 10, mtime: recent}, syncEntry{size: 10, mtime: old}, false, true},
		{"destination newer", syncEntry{size: 10, mtime: old}, syncEntry{size: 10, mtime: recent}, false, false},
		{"--size-only ignores a newer source", syncEntry{size: 10, mtime: recent}, syncEntry{size: 10, mtime: old}, true, false},
		{"--size-only still sees a size change", syncEntry{size: 11, mtime: old}, syncEntry{size: 10, mtime: old}, true, true},
		// S3 reports Last-Modified to the second while a local mtime carries
		// nanoseconds; without truncation every file looks newer than its copy.
		{"sub-second difference is not newer",
			syncEntry{size: 10, mtime: old.Add(400 * time.Millisecond)}, syncEntry{size: 10, mtime: old}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &syncFlags{sizeOnly: tc.sizeOnly}
			if got := f.needsTransfer(tc.src, tc.dst); got != tc.want {
				t.Errorf("needsTransfer = %v, want %v", got, tc.want)
			}
		})
	}
}

// Only what differs moves: a matching object is left alone, so a re-run of a
// finished sync costs one listing per side and no transfers.
func TestRunSyncLocalToS3TransfersOnlyDifferences(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "same.txt"), "1234")
	mustWrite(t, filepath.Join(dir, "changed.txt"), "1234567")
	mustWrite(t, filepath.Join(dir, "new.txt"), "12")
	// Both existing objects are newer than the files, so only the size
	// difference should move.
	future := time.Now().Add(time.Hour).UTC().Format("2006-01-02T15:04:05.000Z")
	m := &syncMock{objects: map[string]syncObject{
		"2026/same.txt":    {size: 4, mtime: future},
		"2026/changed.txt": {size: 99, mtime: future},
	}}
	client := newMockClient(t, m.handler(t))

	var buf bytes.Buffer
	err := runSync(context.Background(), client, localSide(dir), s3Side("db-backups", "2026/"),
		testSyncFlags(), &buf)
	if err != nil {
		t.Fatal(err)
	}
	want := "/db-backups/2026/changed.txt,/db-backups/2026/new.txt"
	if got := strings.Join(sortedStrings(m.puts), ","); got != want {
		t.Errorf("uploaded = %q, want %q", got, want)
	}
}

// A finished sync says so, rather than printing nothing — which would be
// indistinguishable from "the source matched nothing".
func TestRunSyncAlreadyInSync(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a.txt"), "1234")
	future := time.Now().Add(time.Hour).UTC().Format("2006-01-02T15:04:05.000Z")
	m := &syncMock{objects: map[string]syncObject{"a.txt": {size: 4, mtime: future}}}
	client := newMockClient(t, m.handler(t))

	var buf bytes.Buffer
	err := runSync(context.Background(), client, localSide(dir), s3Side("db-backups", ""),
		testSyncFlags(), &buf)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.puts) != 0 {
		t.Errorf("uploaded %v, want nothing", m.puts)
	}
	if !strings.Contains(buf.String(), "Already in sync") {
		t.Errorf("output = %q, want it to say the sides match", buf.String())
	}
}

// S3 -> local: the tree is created and each key lands at its path below the
// prefix.
func TestRunSyncS3ToLocal(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "restore")
	m := &syncMock{objects: map[string]syncObject{
		"2026/08/a.sql.gz": {size: 4, mtime: "2026-09-01T10:00:00.000Z"},
		"2026/09/b.sql.gz": {size: 4, mtime: "2026-09-01T10:00:00.000Z"},
		"2025/old.sql.gz":  {size: 4, mtime: "2026-09-01T10:00:00.000Z"},
	}}
	client := newMockClient(t, m.handler(t))

	var buf bytes.Buffer
	err := runSync(context.Background(), client, s3Side("db-backups", "2026/"), localSide(dir),
		testSyncFlags(), &buf)
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"08/a.sql.gz", "09/b.sql.gz"} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			t.Errorf("%s was not written: %v", rel, err)
		}
	}
	// The prefix is the scope: 2025/ is outside it.
	if _, err := os.Stat(filepath.Join(dir, "..", "old.sql.gz")); err == nil {
		t.Error("an object outside the prefix was transferred")
	}
}

// A stale local copy is replaced, unlike the single-object "download" where an
// existing file means a mistyped key.
func TestRunSyncS3ToLocalReplacesStaleFiles(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, "a.txt")
	mustWrite(t, stale, "old")
	m := &syncMock{objects: map[string]syncObject{
		"a.txt": {size: 4, mtime: time.Now().Add(time.Hour).UTC().Format("2006-01-02T15:04:05.000Z")},
	}}
	client := newMockClient(t, m.handler(t))

	var buf bytes.Buffer
	err := runSync(context.Background(), client, s3Side("b", ""), localSide(dir), testSyncFlags(), &buf)
	if err != nil {
		t.Fatal(err)
	}
	if body, _ := os.ReadFile(stale); string(body) != "data" {
		t.Errorf("the stale file was not replaced: %q", body)
	}
}

// S3 -> S3 is server-side: the bytes never travel through koc.
func TestRunSyncS3ToS3IsServerSide(t *testing.T) {
	m := &syncMock{objects: map[string]syncObject{
		"2026/a.sql.gz": {size: 4, mtime: "2026-09-01T10:00:00.000Z"},
	}}
	client := newMockClient(t, m.handler(t))

	var buf bytes.Buffer
	err := runSync(context.Background(), client, s3Side("db-backups", "2026/"), s3Side("archive", "old/"),
		testSyncFlags(), &buf)
	if err != nil {
		t.Fatal(err)
	}
	want := "/db-backups/2026/a.sql.gz -> /archive/old/a.sql.gz"
	if got := strings.Join(m.copies, ","); got != want {
		t.Errorf("copies = %q, want %q", got, want)
	}
	if len(m.gets) != 0 {
		t.Errorf("objects were fetched (%v); a server-side copy transfers nothing", m.gets)
	}
}

// --delete is what makes this a mirror: a destination entry the source does not
// have goes away, batched into one request.
func TestRunSyncDeleteRemovesExtraObjects(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "keep.txt"), "1234")
	future := time.Now().Add(time.Hour).UTC().Format("2006-01-02T15:04:05.000Z")
	m := &syncMock{objects: map[string]syncObject{
		"keep.txt":  {size: 4, mtime: future},
		"stale.txt": {size: 4, mtime: future},
	}}
	client := newMockClient(t, m.handler(t))

	f := testSyncFlags()
	f.del = true
	var buf bytes.Buffer
	if err := runSync(context.Background(), client, localSide(dir), s3Side("b", ""), f, &buf); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(m.deletes, ","), "stale.txt"; got != want {
		t.Errorf("deleted = %q, want %q", got, want)
	}
	if len(m.puts) != 0 {
		t.Errorf("uploaded %v, want nothing — only the extra object changed", m.puts)
	}
}

func TestRunSyncDeleteRemovesExtraLocalFiles(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, "sub", "stale.txt")
	mustWrite(t, stale, "old")
	m := &syncMock{objects: map[string]syncObject{}}
	client := newMockClient(t, m.handler(t))

	f := testSyncFlags()
	f.del, f.force = true, true // the source is empty on purpose here
	var buf bytes.Buffer
	if err := runSync(context.Background(), client, s3Side("b", ""), localSide(dir), f, &buf); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); err == nil {
		t.Error("the extra local file was not deleted")
	}
	// The directory it was in stays: an empty directory is not something the
	// source can be said to have deleted.
	if _, err := os.Stat(filepath.Dir(stale)); err != nil {
		t.Errorf("the containing directory was removed: %v", err)
	}
}

// The sharp edge: a mistyped source that lists nothing must not erase the
// destination.
func TestRunSyncDeleteRefusesAnEmptySource(t *testing.T) {
	dir := t.TempDir() // empty
	m := &syncMock{objects: map[string]syncObject{
		"a.txt": {size: 4, mtime: "2026-09-01T10:00:00.000Z"},
		"b.txt": {size: 4, mtime: "2026-09-01T10:00:00.000Z"},
	}}
	client := newMockClient(t, m.handler(t))

	f := testSyncFlags()
	f.del = true
	var buf bytes.Buffer
	err := runSync(context.Background(), client, localSide(dir), s3Side("b", ""), f, &buf)
	if err == nil {
		t.Fatal("an empty source was allowed to delete the whole destination")
	}
	if !strings.Contains(err.Error(), "--force") || !strings.Contains(err.Error(), "2 entries") {
		t.Errorf("error %q does not say what it refused and how to override", err)
	}
	if len(m.deletes) != 0 {
		t.Errorf("deleted %v despite refusing", m.deletes)
	}

	// With --force it goes ahead, because that is now an explicit instruction.
	f.force = true
	buf.Reset()
	if err := runSync(context.Background(), client, localSide(dir), s3Side("b", ""), f, &buf); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(sortedStrings(m.deletes), ","), "a.txt,b.txt"; got != want {
		t.Errorf("deleted = %q, want %q", got, want)
	}
}

// An entry the filter excludes is outside the sync altogether: it is neither
// transferred nor deleted.
func TestRunSyncFilterExcludesFromBothSides(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a.sql.gz"), "1234")
	mustWrite(t, filepath.Join(dir, "a.sha256"), "12")
	m := &syncMock{objects: map[string]syncObject{
		"other.sha256": {size: 9, mtime: "2026-09-01T10:00:00.000Z"},
	}}
	client := newMockClient(t, m.handler(t))

	f := testSyncFlags()
	f.del = true
	f.filter = keyFilter{include: []string{"*.sql.gz"}}
	if err := f.filter.compile(); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := runSync(context.Background(), client, localSide(dir), s3Side("b", ""), f, &buf); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(m.puts, ","), "/b/a.sql.gz"; got != want {
		t.Errorf("uploaded = %q, want only the included file", got)
	}
	if len(m.deletes) != 0 {
		t.Errorf("deleted %v — an excluded destination entry is not the sync's to remove", m.deletes)
	}
}

// --dry-run must name every transfer and deletion it would perform, and perform
// none of them.
func TestRunSyncDryRun(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "new.txt"), "12")
	m := &syncMock{objects: map[string]syncObject{
		"stale.txt": {size: 4, mtime: "2026-09-01T10:00:00.000Z"},
	}}
	client := newMockClient(t, m.handler(t))

	f := testSyncFlags()
	f.del, f.dryRun = true, true
	var buf bytes.Buffer
	if err := runSync(context.Background(), client, localSide(dir), s3Side("b", ""), f, &buf); err != nil {
		t.Fatal(err)
	}
	if len(m.puts)+len(m.deletes)+len(m.copies) != 0 {
		t.Errorf("--dry-run wrote: puts=%v deletes=%v copies=%v", m.puts, m.deletes, m.copies)
	}
	out := buf.String()
	if !strings.Contains(out, "Would upload") || !strings.Contains(out, "Would delete object: b/stale.txt") {
		t.Errorf("output = %q, want both planned actions named", out)
	}
}

// A local side that is a single file, or an existing non-directory destination,
// is a mistake worth naming before any transfer.
func TestRunSyncChecksLocalSides(t *testing.T) {
	client := newMockClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("a request was made despite an invalid local side: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusOK)
	})

	file := filepath.Join(t.TempDir(), "one.txt")
	mustWrite(t, file, "x")

	var buf bytes.Buffer
	err := runSync(context.Background(), client, localSide(file), s3Side("b", ""), testSyncFlags(), &buf)
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("err = %v, want it to reject a file as the source", err)
	}

	err = runSync(context.Background(), client, s3Side("b", ""), localSide(file), testSyncFlags(), &buf)
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("err = %v, want it to reject a file as the destination", err)
	}

	err = runSync(context.Background(), client, localSide(filepath.Join(t.TempDir(), "absent")),
		s3Side("b", ""), testSyncFlags(), &buf)
	if err == nil || !strings.Contains(err.Error(), "reading source") {
		t.Errorf("err = %v, want it to name the missing source", err)
	}
}

// A local destination that does not exist yet is an empty index, not an error:
// it is about to be created.
func TestRunSyncCreatesAMissingLocalDestination(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "new", "deep")
	m := &syncMock{objects: map[string]syncObject{
		"a.txt": {size: 4, mtime: "2026-09-01T10:00:00.000Z"},
	}}
	client := newMockClient(t, m.handler(t))

	var buf bytes.Buffer
	if err := runSync(context.Background(), client, s3Side("b", ""), localSide(dir), testSyncFlags(), &buf); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.txt")); err != nil {
		t.Errorf("the destination tree was not created: %v", err)
	}
}

// A directory marker — a zero-length key ending in "/" that some tools leave
// behind — has no counterpart on a filesystem and must not become a file.
func TestRunSyncSkipsDirectoryMarkers(t *testing.T) {
	dir := t.TempDir()
	m := &syncMock{objects: map[string]syncObject{
		"2026/":      {size: 0, mtime: "2026-09-01T10:00:00.000Z"},
		"2026/a.txt": {size: 4, mtime: "2026-09-01T10:00:00.000Z"},
	}}
	client := newMockClient(t, m.handler(t))

	var buf bytes.Buffer
	if err := runSync(context.Background(), client, s3Side("b", ""), localSide(dir), testSyncFlags(), &buf); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(sortedStrings(m.gets), ","); got != "/b/2026/a.txt" {
		t.Errorf("fetched = %q, want the marker skipped", got)
	}
}

// sortedStrings makes an assertion independent of completion order, which with
// more than one worker is not the listing order.
func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

package s3cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestRunObjectDelete(t *testing.T) {
	var paths []string
	client := newMockClient(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	})

	var buf bytes.Buffer
	refs := []string{"db-backups/a.sql.gz", "s3://db-backups/b.sql.gz"}
	if err := runObjectDelete(context.Background(), client, refs, &objectDeleteFlags{}, &buf); err != nil {
		t.Fatal(err)
	}
	want := "DELETE /db-backups/a.sql.gz,DELETE /db-backups/b.sql.gz"
	if got := strings.Join(paths, ","); got != want {
		t.Errorf("requests = %q, want %q", got, want)
	}
	wantOut := "Deleted object: db-backups/a.sql.gz\nDeleted object: db-backups/b.sql.gz\n"
	if got := buf.String(); got != wantOut {
		t.Errorf("output = %q, want %q", got, wantOut)
	}
}

// Without --recursive a bare bucket is not a target: it would be a one-word
// typo away from emptying the bucket.
func TestRunObjectDeleteRequiresKey(t *testing.T) {
	client := newMockClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("no request should be made for a keyless ref")
		w.WriteHeader(http.StatusNoContent)
	})

	var buf bytes.Buffer
	err := runObjectDelete(context.Background(), client, []string{"db-backups"}, &objectDeleteFlags{}, &buf)
	if err == nil {
		t.Fatal("expected a keyless ref to be rejected")
	}
}

func TestRunObjectDeleteRecursive(t *testing.T) {
	var deleted []string
	client := newMockClient(t, func(w http.ResponseWriter, r *http.Request) {
		// A recursive delete goes out as one batched POST ?delete, not one
		// DELETE per key: that is what makes emptying a large bucket finish.
		if r.Method == http.MethodPost && r.URL.Query().Has("delete") {
			body, _ := io.ReadAll(r.Body)
			deleted = append(deleted, keysInBatch(string(body))...)
			_, _ = fmt.Fprint(w, `<DeleteResult></DeleteResult>`)
			return
		}
		if r.Method == http.MethodDelete {
			t.Errorf("a per-key DELETE was sent for %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("prefix"); got != "e2e-" {
			t.Errorf("prefix = %q, want e2e-", got)
		}
		_, _ = fmt.Fprint(w, `<ListBucketResult><IsTruncated>false</IsTruncated>
			<Contents><Key>e2e-a.sql.gz</Key><Size>1</Size></Contents>
			<Contents><Key>e2e-b.sql.gz</Key><Size>2</Size></Contents>
			</ListBucketResult>`)
	})

	var buf bytes.Buffer
	err := runObjectDelete(context.Background(), client, []string{"db-backups/e2e-"},
		&objectDeleteFlags{recursive: true}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(deleted, ","), "e2e-a.sql.gz,e2e-b.sql.gz"; got != want {
		t.Errorf("deleted = %q, want %q", got, want)
	}
	want := "Deleted object: db-backups/e2e-a.sql.gz\nDeleted object: db-backups/e2e-b.sql.gz\n"
	if got := buf.String(); got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

// A wildcard reference is itself the instruction to go recursive, and the
// pattern narrows the listing the prefix could not.
func TestRunObjectDeleteWildcardRef(t *testing.T) {
	var deleted []string
	client := newMockClient(t, batchDeleteHandler(t, &deleted,
		`<Contents><Key>2026/a.sql.gz</Key></Contents>`+
			`<Contents><Key>2026/a.sha256</Key></Contents>`))

	var buf bytes.Buffer
	err := runObjectDelete(context.Background(), client, []string{"db-backups/2026/*.sql.gz"},
		&objectDeleteFlags{}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(deleted, ","), "2026/a.sql.gz"; got != want {
		t.Errorf("deleted = %q, want only the pattern match", got)
	}
}

// --exclude removes a key the prefix would otherwise have taken.
func TestRunObjectDeleteExclude(t *testing.T) {
	var deleted []string
	client := newMockClient(t, batchDeleteHandler(t, &deleted,
		`<Contents><Key>a.sql.gz</Key></Contents><Contents><Key>keep.sql.gz</Key></Contents>`))

	f := &objectDeleteFlags{recursive: true, filter: keyFilter{exclude: []string{"keep*"}}}
	if err := f.filter.compile(); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := runObjectDelete(context.Background(), client, []string{"db-backups/"}, f, &buf); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(deleted, ","), "a.sql.gz"; got != want {
		t.Errorf("deleted = %q, want %q", got, want)
	}
}

// A key the server refuses inside the 200 must be reported as a failure, while
// the rest of the batch is still reported as deleted.
func TestRunObjectDeleteReportsPerKeyFailures(t *testing.T) {
	client := newMockClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_, _ = fmt.Fprint(w, `<DeleteResult>
				<Error><Key>locked</Key><Code>AccessDenied</Code><Message>no</Message></Error>
				</DeleteResult>`)
			return
		}
		_, _ = fmt.Fprint(w, `<ListBucketResult><IsTruncated>false</IsTruncated>
			<Contents><Key>fine</Key></Contents><Contents><Key>locked</Key></Contents>
			</ListBucketResult>`)
	})

	var buf bytes.Buffer
	err := runObjectDelete(context.Background(), client, []string{"db-backups/"},
		&objectDeleteFlags{recursive: true}, &buf)
	if err == nil {
		t.Fatal("a refused key was reported as success")
	}
	if !strings.Contains(err.Error(), "locked") {
		t.Errorf("error %q does not name the refused key", err)
	}
	if got, want := buf.String(), "Deleted object: db-backups/fine\n"; got != want {
		t.Errorf("output = %q, want only the key that was deleted", got)
	}
}

// --dry-run must list the same keys it would remove and issue no delete at all.
func TestRunObjectDeleteDryRun(t *testing.T) {
	client := newMockClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("--dry-run issued %s %s", r.Method, r.URL.Path)
		}
		_, _ = fmt.Fprint(w, `<ListBucketResult><IsTruncated>false</IsTruncated>
			<Contents><Key>a.sql.gz</Key></Contents>
			</ListBucketResult>`)
	})

	var buf bytes.Buffer
	err := runObjectDelete(context.Background(), client, []string{"db-backups/"},
		&objectDeleteFlags{recursive: true, dryRun: true}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := buf.String(), "Would delete object: db-backups/a.sql.gz\n"; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

// An empty prefix must say so rather than reporting a silent success that reads
// like "everything was already deleted".
func TestRunObjectDeleteRecursiveNoMatches(t *testing.T) {
	client := newMockClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `<ListBucketResult><IsTruncated>false</IsTruncated></ListBucketResult>`)
	})

	var buf bytes.Buffer
	err := runObjectDelete(context.Background(), client, []string{"db-backups/gone-"},
		&objectDeleteFlags{recursive: true}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := buf.String(), "No objects under db-backups/gone-\n"; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

// --version-id names exactly one object, so every shape that means "many" is a
// mistake worth refusing before anything is removed.
func TestObjectDeleteFlagValidation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags objectDeleteFlags
		refs  []string
		want  string
	}{
		{"version with recursive", objectDeleteFlags{versionID: "v1", recursive: true},
			[]string{"b/k"}, "--recursive"},
		{"version with all-versions", objectDeleteFlags{versionID: "v1", allVersions: true},
			[]string{"b/k"}, "mutually exclusive"},
		{"version with two refs", objectDeleteFlags{versionID: "v1"},
			[]string{"b/k", "b/j"}, "a single reference"},
		{"filters without recursion", objectDeleteFlags{filter: keyFilter{include: []string{"*"}}},
			[]string{"b/k"}, "--recursive"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.flags.validate(tc.refs)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}

	// A wildcard reference is its own recursion, so filters are legal with one.
	f := objectDeleteFlags{filter: keyFilter{include: []string{"*.gz"}}}
	if err := f.validate([]string{"b/2026/*"}); err != nil {
		t.Errorf("filters with a wildcard ref were refused: %v", err)
	}
}

// A version-scoped delete addresses the one version and says which.
func TestRunObjectDeleteVersion(t *testing.T) {
	var gotVersion string
	client := newMockClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotVersion = r.URL.Query().Get("versionId")
		w.WriteHeader(http.StatusNoContent)
	})

	var buf bytes.Buffer
	err := runObjectDelete(context.Background(), client, []string{"db-backups/dump.sql.gz"},
		&objectDeleteFlags{versionID: "v3"}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if gotVersion != "v3" {
		t.Errorf("versionId = %q, want v3", gotVersion)
	}
	want := "Deleted object: db-backups/dump.sql.gz (version v3)\n"
	if got := buf.String(); got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

// batchDeleteHandler serves one listing page and records the keys of every
// batch delete it is sent.
func batchDeleteHandler(t *testing.T, deleted *[]string, contents string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Query().Has("delete") {
			body, _ := io.ReadAll(r.Body)
			*deleted = append(*deleted, keysInBatch(string(body))...)
			_, _ = fmt.Fprint(w, `<DeleteResult></DeleteResult>`)
			return
		}
		_, _ = fmt.Fprint(w, `<ListBucketResult><IsTruncated>false</IsTruncated>`+contents+`</ListBucketResult>`)
	}
}

// keysInBatch pulls the keys out of a DeleteObjects request body.
func keysInBatch(body string) []string {
	var keys []string
	for rest := body; ; {
		i := strings.Index(rest, "<Key>")
		if i < 0 {
			return keys
		}
		rest = rest[i+len("<Key>"):]
		j := strings.Index(rest, "</Key>")
		if j < 0 {
			return keys
		}
		keys = append(keys, rest[:j])
		rest = rest[j:]
	}
}

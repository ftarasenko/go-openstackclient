package s3cli

import (
	"bytes"
	"context"
	"fmt"
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
		if r.Method == http.MethodDelete {
			deleted = append(deleted, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
			return
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
	want := "/db-backups/e2e-a.sql.gz,/db-backups/e2e-b.sql.gz"
	if got := strings.Join(deleted, ","); got != want {
		t.Errorf("deleted = %q, want %q", got, want)
	}
}

// --dry-run must list the same keys it would remove and issue no DELETE at all.
func TestRunObjectDeleteDryRun(t *testing.T) {
	client := newMockClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			t.Errorf("--dry-run issued DELETE %s", r.URL.Path)
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

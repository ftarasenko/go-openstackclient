package s3cli

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestRunBucketCreate(t *testing.T) {
	var gotMethod, gotPath string
	client := newMockClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusOK)
	})

	var buf bytes.Buffer
	if err := runBucketCreate(context.Background(), client, valueOpts(), "scratch", &buf); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPut || gotPath != "/scratch" {
		t.Errorf("request = %s %s, want PUT /scratch", gotMethod, gotPath)
	}
	// WriteSingle in value format is one field per line.
	if got, want := buf.String(), "scratch\ngarage\n"; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

// An existing bucket must produce advice, not a raw 409: the two S3 codes mean
// different things to the operator ("yours" vs "someone else's").
func TestRunBucketCreateAlreadyExists(t *testing.T) {
	for _, tc := range []struct{ code, want string }{
		{"BucketAlreadyOwnedByYou", "owned by these credentials"},
		{"BucketAlreadyExists", "owned by someone else"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			client := newMockClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusConflict)
				_, _ = fmt.Fprintf(w, `<Error><Code>%s</Code><Message>x</Message></Error>`, tc.code)
			})

			var buf bytes.Buffer
			err := runBucketCreate(context.Background(), client, valueOpts(), "scratch", &buf)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to mention %q", err, tc.want)
			}
			if buf.Len() != 0 {
				t.Errorf("wrote %q on failure, want nothing", buf.String())
			}
		})
	}
}

func TestRunBucketDelete(t *testing.T) {
	var paths []string
	client := newMockClient(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	})

	var buf bytes.Buffer
	err := runBucketDelete(context.Background(), client, []string{"scratch", "s3://spare"}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(paths, ","), "DELETE /scratch,DELETE /spare"; got != want {
		t.Errorf("requests = %q, want %q", got, want)
	}
	want := "Deleted bucket: scratch\nDeleted bucket: spare\n"
	if got := buf.String(); got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

// A non-empty bucket is the expected failure, and the message must name the
// command that fixes it rather than leaving the operator with "409 Conflict".
func TestRunBucketDeleteNotEmptyExplains(t *testing.T) {
	client := newMockClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = fmt.Fprint(w, `<Error><Code>BucketNotEmpty</Code><Message>x</Message></Error>`)
	})

	var buf bytes.Buffer
	err := runBucketDelete(context.Background(), client, []string{"db-backups"}, &buf)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "koc s3 object delete db-backups/ --recursive") {
		t.Errorf("error = %q, want it to suggest the recursive delete", err)
	}
}

// The batch contract: a failing ref must not skip the ones after it.
func TestRunBucketDeleteAttemptsEveryRef(t *testing.T) {
	client := newMockClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/locked" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	var buf bytes.Buffer
	err := runBucketDelete(context.Background(), client, []string{"locked", "scratch"}, &buf)
	if err == nil {
		t.Fatal("expected the failing ref to be reported")
	}
	if got, want := buf.String(), "Deleted bucket: scratch\n"; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

// A bucket ref carrying a key is a typo, and on a create it would silently make
// a bucket the operator did not name.
func TestParseBucketRefRejectsKey(t *testing.T) {
	if _, err := parseBucketRef("scratch/keys"); err == nil {
		t.Error("expected scratch/keys to be rejected as a bucket ref")
	}
	for _, ref := range []string{"scratch", "s3://scratch", "scratch/"} {
		got, err := parseBucketRef(ref)
		if err != nil {
			t.Errorf("parseBucketRef(%q) = %v", ref, err)
			continue
		}
		if got != "scratch" {
			t.Errorf("parseBucketRef(%q) = %q, want scratch", ref, got)
		}
	}
}

package s3cli

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// With no FILE the object's key basename is used, so a download lands next to
// the operator rather than at a path they have to spell out.
func TestRunDownload_DefaultsToTheKeyBasename(t *testing.T) {
	client := newMockClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("dump"))
	})

	dir := t.TempDir()
	t.Chdir(dir)

	var buf bytes.Buffer
	err := runDownload(context.Background(), client, valueOpts(),
		downloadRequest{bucket: "db-backups", key: "nested/e2e-mariadb.sql.gz"}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "e2e-mariadb.sql.gz")); err != nil {
		t.Fatalf("the key basename was not used: %v", err)
	}
}

// A directory as FILE means "put it in here", the way cp does.
func TestRunDownload_DirectoryDestination(t *testing.T) {
	client := newMockClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("dump"))
	})

	dir := t.TempDir()
	var buf bytes.Buffer
	err := runDownload(context.Background(), client, valueOpts(),
		downloadRequest{bucket: "db-backups", key: "e2e-mariadb.sql.gz", dest: dir}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "e2e-mariadb.sql.gz")); err != nil {
		t.Fatalf("the object was not placed inside the directory: %v", err)
	}
}

// A destination that cannot be created must be reported as such, naming the
// path, rather than as a download failure.
func TestRunDownload_UncreatableDestination(t *testing.T) {
	client := newMockClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("dump"))
	})

	// A path under a regular file cannot be created.
	file := filepath.Join(t.TempDir(), "notadir")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	err := runDownload(context.Background(), client, valueOpts(),
		downloadRequest{bucket: "db-backups", key: "key", dest: filepath.Join(file, "dump")}, &buf)
	if err == nil {
		t.Fatal("an uncreatable destination was reported as success")
	}
	if !strings.Contains(err.Error(), "creating") {
		t.Errorf("error %q does not say what failed", err)
	}
}

// dest "-" streams to the writer, so its failure path is the download error too.
func TestRunDownload_StdoutMissingKey(t *testing.T) {
	client := newMockClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	var buf bytes.Buffer
	err := runDownload(context.Background(), client, valueOpts(),
		downloadRequest{bucket: "db-backups", key: "missing", dest: "-"}, &buf)
	if err == nil {
		t.Fatal("a 404 streaming to stdout was reported as success")
	}
	if !strings.Contains(err.Error(), `no object "missing"`) {
		t.Errorf("error %q is not the not-found message", err)
	}
}

// A failure that is not a missing key keeps the underlying cause, since that is
// what an operator needs to tell a 403 from a broken endpoint.
func TestRunDownload_NonNotFoundErrorIsWrapped(t *testing.T) {
	client := newMockClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprint(w, `<Error><Code>AccessDenied</Code><Message>no</Message></Error>`)
	})

	var buf bytes.Buffer
	err := runDownload(context.Background(), client, valueOpts(),
		downloadRequest{bucket: "db-backups", key: "key", dest: filepath.Join(t.TempDir(), "dump")}, &buf)
	if err == nil {
		t.Fatal("a 403 was reported as success")
	}
	if !strings.Contains(err.Error(), "downloading db-backups/key") {
		t.Errorf("error %q does not name the object", err)
	}
	if !strings.Contains(err.Error(), "AccessDenied") {
		t.Errorf("error %q lost the S3 cause", err)
	}
}

func TestRunUpload_MissingSourceFile(t *testing.T) {
	client := newMockClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	var buf bytes.Buffer
	err := runUpload(context.Background(), client, valueOpts(),
		filepath.Join(t.TempDir(), "absent"), "db-backups", "key", &buf)
	if err == nil {
		t.Fatal("a missing source file was reported as success")
	}
	if !strings.Contains(err.Error(), "opening") {
		t.Errorf("error %q does not say what failed", err)
	}
}

// "upload" takes a single file; a directory is a mistake worth naming rather
// than a zero-byte object.
func TestRunUpload_RejectsADirectory(t *testing.T) {
	called := false
	client := newMockClient(t, func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	var buf bytes.Buffer
	err := runUpload(context.Background(), client, valueOpts(), t.TempDir(), "db-backups", "", &buf)
	if err == nil {
		t.Fatal("a directory was accepted as an upload source")
	}
	if !strings.Contains(err.Error(), "directory") {
		t.Errorf("error %q does not name the problem", err)
	}
	if called {
		t.Error("a request was sent for a directory")
	}
}

// A key ending in "/" means "into this prefix", so the basename is appended.
func TestRunUpload_PrefixKeyAppendsBasename(t *testing.T) {
	var gotPath string
	client := newMockClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("ETag", `"abc"`)
		w.WriteHeader(http.StatusOK)
	})

	src := filepath.Join(t.TempDir(), "dump.sql.gz")
	if err := os.WriteFile(src, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := runUpload(context.Background(), client, valueOpts(), src, "db-backups", "daily/", &buf); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/db-backups/daily/dump.sql.gz" {
		t.Errorf("path = %q, want the basename appended to the prefix", gotPath)
	}
}

func TestRunUpload_APIErrorIsWrapped(t *testing.T) {
	client := newMockClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprint(w, `<Error><Code>AccessDenied</Code></Error>`)
	})

	src := filepath.Join(t.TempDir(), "dump.sql.gz")
	if err := os.WriteFile(src, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	err := runUpload(context.Background(), client, valueOpts(), src, "db-backups", "key", &buf)
	if err == nil {
		t.Fatal("a 403 was reported as success")
	}
	if !strings.Contains(err.Error(), "uploading") {
		t.Errorf("error %q does not name the operation", err)
	}
}

// contentTypeFor is what makes a .sha256 sibling read as text in a browser
// instead of downloading, so the fallback matters as much as the hit.
func TestContentTypeFor(t *testing.T) {
	if got := contentTypeFor("dump.txt"); !strings.HasPrefix(got, "text/plain") {
		t.Errorf("contentTypeFor(.txt) = %q", got)
	}
	if got := contentTypeFor("dump.unknown-ext"); got != "application/octet-stream" {
		t.Errorf("contentTypeFor(unknown) = %q, want the S3 default", got)
	}
}

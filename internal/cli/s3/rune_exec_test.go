package s3cli

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// The tests in this file execute commands through cobra — Execute() → RunE →
// connFlags.client → runXxx — rather than calling a runXxx seam directly. That
// covers the glue the seam tests skip by construction, and it is the only layer
// that proves a flag registered by newXxxCommand actually reaches the seam that
// reads it. A wiring typo (flag bound to the wrong field, a verb wired to the
// wrong seam) is invisible to a seam test and fails here.
//
// Unlike the other command groups this needs no auth test seam: "koc s3" never
// authenticates against Keystone, so pointing --s3-endpoint at an httptest
// server is enough to drive the real credential path, signing included.

// execS3 builds the real `s3` command group, points it at srv, and runs argv.
// It returns the command's stdout and the error Execute produced.
func execS3(t *testing.T, srv *httptest.Server, format string, argv ...string) (string, error) {
	t.Helper()

	root := &cobra.Command{Use: "koc"}
	root.AddCommand(NewCommand(&auth.Options{}, &output.Options{Format: format}))

	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SilenceUsage = true
	root.SilenceErrors = true

	full := []string{"s3"}
	if srv != nil {
		full = append(full,
			"--s3-endpoint", srv.URL,
			"--s3-access-key", "GKtest",
			"--s3-secret-key", "secret",
		)
	}
	root.SetArgs(append(full, argv...))

	err := root.ExecuteContext(context.Background())
	return buf.String(), err
}

// mockS3 serves h and cleans itself up. Every connection flag the group needs is
// supplied by execS3.
func mockS3(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// The credential flags must survive the trip through connFlags.client into a
// signed request, and the rendered row must reach stdout.
func TestExec_BucketList_RendersThroughRunE(t *testing.T) {
	var gotAuth string
	srv := mockS3(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = fmt.Fprint(w, `<ListAllMyBucketsResult><Buckets>
			<Bucket><Name>db-backups</Name><CreationDate>2026-08-21T05:48:02.364Z</CreationDate></Bucket>
			</Buckets></ListAllMyBucketsResult>`)
	})

	out, err := execS3(t, srv, output.FormatValue, "bucket", "list")
	if err != nil {
		t.Fatalf("bucket list: %v (output %q)", err, out)
	}
	if !strings.Contains(out, "db-backups") {
		t.Fatalf("bucket list output missing the row:\n%s", out)
	}
	if !strings.HasPrefix(gotAuth, "AWS4-HMAC-SHA256 Credential=GKtest/") {
		t.Fatalf("--s3-access-key did not reach the signature; Authorization was %q", gotAuth)
	}
}

// A flag registered on the command must reach the seam that turns it into a
// query parameter. The seam test cannot catch a mis-binding: it is handed the
// parsed value.
func TestExec_ObjectList_FlagsReachTheQuery(t *testing.T) {
	var gotQuery string
	srv := mockS3(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = fmt.Fprint(w, `<ListBucketResult><IsTruncated>false</IsTruncated>
			<Contents><Key>e2e-mariadb.sql.gz</Key><Size>14200000</Size>
			<LastModified>2026-08-21T06:00:00.000Z</LastModified><ETag>"abc"</ETag></Contents>
			</ListBucketResult>`)
	})

	out, err := execS3(t, srv, output.FormatValue,
		"object", "list", "db-backups", "--prefix", "e2e-", "--limit", "5")
	if err != nil {
		t.Fatalf("object list: %v (output %q)", err, out)
	}
	if !strings.Contains(gotQuery, "prefix=e2e-") {
		t.Fatalf("--prefix did not reach the request; query was %q", gotQuery)
	}
	// --limit is a hard result cap, applied by turning the last page request
	// into a max-keys of what is still wanted.
	if !strings.Contains(gotQuery, "max-keys=5") {
		t.Fatalf("--limit did not reach the request; query was %q", gotQuery)
	}
	if !strings.Contains(out, "e2e-mariadb.sql.gz") {
		t.Fatalf("object list output missing the row:\n%s", out)
	}
}

// "s3 object list db-backups/e2e-" is the natural way to type a prefix, so the
// positional form must reach the same query parameter as --prefix.
func TestExec_ObjectList_PositionalPrefix(t *testing.T) {
	var gotQuery string
	srv := mockS3(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = fmt.Fprint(w, `<ListBucketResult><IsTruncated>false</IsTruncated></ListBucketResult>`)
	})

	if _, err := execS3(t, srv, output.FormatValue, "object", "list", "db-backups/e2e-"); err != nil {
		t.Fatalf("object list db-backups/e2e-: %v", err)
	}
	if !strings.Contains(gotQuery, "prefix=e2e-") {
		t.Fatalf("the positional prefix did not reach the request; query was %q", gotQuery)
	}
}

// "object show" takes a <bucket>/<key> ref, so the ref parser is part of the
// wiring under test.
func TestExec_ObjectShow_RendersThroughRunE(t *testing.T) {
	srv := mockS3(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			t.Errorf("method = %s, want HEAD", r.Method)
		}
		if r.URL.Path != "/db-backups/e2e-mariadb.sql.gz" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Length", "14200000")
		w.Header().Set("ETag", `"abc"`)
		w.Header().Set("Last-Modified", "Thu, 21 Aug 2026 06:00:00 GMT")
		w.WriteHeader(http.StatusOK)
	})

	out, err := execS3(t, srv, output.FormatValue, "object", "show", "db-backups/e2e-mariadb.sql.gz")
	if err != nil {
		t.Fatalf("object show: %v (output %q)", err, out)
	}
	if !strings.Contains(out, "14200000") {
		t.Fatalf("object show output missing the size:\n%s", out)
	}
}

// A ref with no key cannot name an object; the error must come from RunE before
// any request is made.
func TestExec_ObjectShow_RejectsBucketOnlyRef(t *testing.T) {
	called := false
	srv := mockS3(t, func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	if _, err := execS3(t, srv, output.FormatValue, "object", "show", "db-backups"); err == nil {
		t.Fatal("a bucket-only ref exited 0; want an error")
	}
	if called {
		t.Fatal("a bucket-only ref still issued the request")
	}
}

func TestExec_Download_WritesTheFile(t *testing.T) {
	srv := mockS3(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("dump"))
	})

	dest := filepath.Join(t.TempDir(), "dump.sql.gz")
	if _, err := execS3(t, srv, output.FormatValue,
		"download", "db-backups/e2e-mariadb.sql.gz", dest); err != nil {
		t.Fatalf("download: %v", err)
	}
	b, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "dump" {
		t.Fatalf("file = %q, want %q", b, "dump")
	}
}

// --force is the one flag "download" has, and the refusal it overrides is the
// guard that protects a local copy of a backup.
func TestExec_Download_ForceOverwrites(t *testing.T) {
	srv := mockS3(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("new"))
	})

	dest := filepath.Join(t.TempDir(), "dump.sql.gz")
	if err := os.WriteFile(dest, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := execS3(t, srv, output.FormatValue,
		"download", "db-backups/key", dest); err == nil {
		t.Fatal("download over an existing file exited 0 without --force")
	}

	if _, err := execS3(t, srv, output.FormatValue,
		"download", "db-backups/key", dest, "--force"); err != nil {
		t.Fatalf("download --force: %v", err)
	}
	b, _ := os.ReadFile(dest)
	if string(b) != "new" {
		t.Fatalf("file = %q, want %q; --force did not reach the seam", b, "new")
	}
}

// dest "-" streams raw bytes to the command's stdout and prints nothing else,
// so it pipes.
func TestExec_Download_ToStdoutIsRawBytes(t *testing.T) {
	srv := mockS3(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("abc123  dump.sql.gz\n"))
	})

	out, err := execS3(t, srv, output.FormatValue, "download", "db-backups/key.sha256", "-")
	if err != nil {
		t.Fatalf("download -: %v", err)
	}
	if out != "abc123  dump.sql.gz\n" {
		t.Fatalf("stdout = %q, want the object's bytes verbatim", out)
	}
}

// A missing key must exit non-zero with the message naming bucket and key,
// rather than leaving an empty file behind.
func TestExec_Download_MissingKeyIsAnError(t *testing.T) {
	srv := mockS3(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	dest := filepath.Join(t.TempDir(), "dump")
	_, err := execS3(t, srv, output.FormatValue, "download", "db-backups/missing", dest)
	if err == nil {
		t.Fatal("a 404 exited 0; want an error")
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Fatalf("error %q does not name the key", err)
	}
	if _, statErr := os.Stat(dest); statErr == nil {
		t.Fatal("a failed download left the file behind")
	}
}

func TestExec_Upload_PutsTheFile(t *testing.T) {
	var gotBody, gotPath, gotType string
	srv := mockS3(t, func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(b)
		gotBody, gotPath, gotType = string(b), r.URL.Path, r.Header.Get("Content-Type")
		w.Header().Set("ETag", `"deadbeef"`)
		w.WriteHeader(http.StatusOK)
	})

	src := filepath.Join(t.TempDir(), "dump.sql.gz")
	if err := os.WriteFile(src, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := execS3(t, srv, output.FormatValue, "upload", src, "db-backups")
	if err != nil {
		t.Fatalf("upload: %v (output %q)", err, out)
	}
	if gotBody != "payload" {
		t.Fatalf("uploaded body = %q, want %q", gotBody, "payload")
	}
	// With no key the file's basename is used.
	if gotPath != "/db-backups/dump.sql.gz" {
		t.Fatalf("path = %q, want /db-backups/dump.sql.gz", gotPath)
	}
	if gotType == "" {
		t.Fatal("no Content-Type was sent")
	}
	if !strings.Contains(out, "deadbeef") {
		t.Fatalf("upload output missing the ETag:\n%s", out)
	}
}

// An API failure must surface as a non-zero exit, not a rendered empty table.
func TestExec_BucketList_APIErrorIsAnError(t *testing.T) {
	srv := mockS3(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprint(w, `<Error><Code>AccessDenied</Code><Message>no</Message></Error>`)
	})

	_, err := execS3(t, srv, output.FormatValue, "bucket", "list")
	if err == nil {
		t.Fatal("a 403 from the API exited 0; want an error")
	}
	if !strings.Contains(err.Error(), "AccessDenied") {
		t.Fatalf("error %q does not carry the S3 error code", err)
	}
}

// o.Validate() is the first statement of every RunE, so a bad --format must
// fail before any request is made.
func TestExec_InvalidFormatFailsBeforeRequest(t *testing.T) {
	called := false
	srv := mockS3(t, func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	if _, err := execS3(t, srv, "bogus", "bucket", "list"); err == nil {
		t.Fatal("an invalid --format exited 0; want an error")
	}
	if called {
		t.Fatal("an invalid --format still issued the request")
	}
}

// Missing credentials must be refused by the client builder, before a request
// goes out unsigned.
func TestExec_MissingCredentialsIsAnError(t *testing.T) {
	// No --s3-endpoint / keys at all: srv nil, and the env is cleared so a
	// developer's own AWS_* cannot make this pass.
	for _, k := range []string{
		"AWS_ENDPOINT_URL", "S3_ENDPOINT", "s3_host",
		"AWS_ACCESS_KEY_ID", "S3_ACCESS_KEY", "s3_access_key",
		"AWS_SECRET_ACCESS_KEY", "S3_SECRET_KEY", "s3_secret_key",
	} {
		t.Setenv(k, "")
	}

	if _, err := execS3(t, nil, output.FormatValue, "bucket", "list"); err == nil {
		t.Fatal("no credentials exited 0; want an error")
	}
}

// --s3-cacert is read by connFlags.client; an unreadable path must fail there
// rather than silently falling back to the system roots.
func TestExec_UnreadableCACertIsAnError(t *testing.T) {
	srv := mockS3(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	missing := filepath.Join(t.TempDir(), "absent.pem")
	_, err := execS3(t, srv, output.FormatValue, "bucket", "list", "--s3-cacert", missing)
	if err == nil {
		t.Fatal("an unreadable --s3-cacert exited 0; want an error")
	}
	if !strings.Contains(err.Error(), "s3-cacert") {
		t.Fatalf("error %q does not name the flag", err)
	}
}

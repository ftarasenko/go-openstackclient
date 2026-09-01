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

	"github.com/ftarasenko/go-openstackclient/internal/output"
	"github.com/ftarasenko/go-openstackclient/internal/s3"
)

// newMockClient builds a real client against a mock endpoint, so the runXxx
// seams are exercised end to end (signing included) with no credentials at play.
func newMockClient(t *testing.T, h http.HandlerFunc) *s3.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	c, err := s3.New(s3.Config{
		Endpoint:  srv.URL,
		AccessKey: "GKtest",
		SecretKey: "secret",
		PathStyle: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func valueOpts() *output.Options { return &output.Options{Format: output.FormatValue} }

func TestRunBucketList(t *testing.T) {
	client := newMockClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `<ListAllMyBucketsResult><Buckets>
			<Bucket><Name>db-backups</Name><CreationDate>2026-08-21T05:48:02.364Z</CreationDate></Bucket>
			</Buckets></ListAllMyBucketsResult>`)
	})

	var buf bytes.Buffer
	if err := runBucketList(context.Background(), client, valueOpts(), &buf); err != nil {
		t.Fatal(err)
	}
	if got, want := buf.String(), "db-backups\t2026-08-21T05:48:02Z\n"; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

func TestRunObjectList(t *testing.T) {
	client := newMockClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("prefix"); got != "e2e-" {
			t.Errorf("prefix = %q, want e2e-", got)
		}
		_, _ = fmt.Fprint(w, `<ListBucketResult><IsTruncated>false</IsTruncated>
			<Contents><Key>e2e-mariadb.sql.gz</Key><Size>14200000</Size>
			<LastModified>2026-08-21T06:00:00.000Z</LastModified><ETag>"abc"</ETag></Contents>
			</ListBucketResult>`)
	})

	var buf bytes.Buffer
	err := runObjectList(context.Background(), client, valueOpts(), "db-backups",
		&objectListFlags{prefix: "e2e-"}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := buf.String(), "e2e-mariadb.sql.gz\t14200000\t2026-08-21T06:00:00Z\tabc\n"; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

func TestRunObjectShow(t *testing.T) {
	client := newMockClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/db-backups/missing" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Length", "96")
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("ETag", `"abc"`)
		w.WriteHeader(http.StatusOK)
	})

	var buf bytes.Buffer
	err := runObjectShow(context.Background(), client, &output.Options{Format: output.FormatJSON},
		"db-backups", "e2e-mariadb.sql.gz.sha256", &buf)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"db-backups", "e2e-mariadb.sql.gz.sha256", "96", "text/plain", "abc"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q:\n%s", want, buf.String())
		}
	}

	err = runObjectShow(context.Background(), client, valueOpts(), "db-backups", "missing", &buf)
	if err == nil || !strings.Contains(err.Error(), `no object "missing" in bucket "db-backups"`) {
		t.Errorf("err = %v, want a friendly not-found message", err)
	}
}

// TestRunDownloadToFile covers the default destination (the key's basename), the
// summary output, and the file's contents.
func TestRunDownloadToFile(t *testing.T) {
	client := newMockClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("dump bytes"))
	})
	dir := t.TempDir()

	var buf bytes.Buffer
	dest := filepath.Join(dir, "out.gz")
	err := runDownload(context.Background(), client, valueOpts(),
		downloadRequest{bucket: "db-backups", key: "e2e-mariadb.sql.gz", dest: dest}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "dump bytes" {
		t.Errorf("file contents = %q", got)
	}
	if !strings.Contains(buf.String(), "10") {
		t.Errorf("summary %q does not report the size", buf.String())
	}

	// A second run must refuse rather than clobber the local copy of a backup.
	err = runDownload(context.Background(), client, valueOpts(),
		downloadRequest{bucket: "db-backups", key: "e2e-mariadb.sql.gz", dest: dest}, &buf)
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Errorf("err = %v, want a refusal pointing at --force", err)
	}
	if err := runDownload(context.Background(), client, valueOpts(),
		downloadRequest{bucket: "db-backups", key: "e2e-mariadb.sql.gz", dest: dest, force: true}, &buf); err != nil {
		t.Errorf("--force download failed: %v", err)
	}
}

// TestRunDownloadStdout pins the "-" destination: raw bytes only, so it pipes.
func TestRunDownloadStdout(t *testing.T) {
	client := newMockClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("abc123  dump.sql.gz\n"))
	})

	var buf bytes.Buffer
	err := runDownload(context.Background(), client, valueOpts(),
		downloadRequest{bucket: "db-backups", key: "e2e-mariadb.sql.gz.sha256", dest: "-"}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := buf.String(), "abc123  dump.sql.gz\n"; got != want {
		t.Errorf("output = %q, want exactly the object's bytes %q", got, want)
	}
}

// TestRunDownloadFailureLeavesNoFile guards against a truncated dump that looks
// like a complete one.
func TestRunDownloadFailureLeavesNoFile(t *testing.T) {
	client := newMockClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`<Error><Code>NoSuchKey</Code><Message>gone</Message></Error>`))
	})
	dest := filepath.Join(t.TempDir(), "out.gz")

	var buf bytes.Buffer
	err := runDownload(context.Background(), client, valueOpts(),
		downloadRequest{bucket: "db-backups", key: "missing", dest: dest}, &buf)
	if err == nil {
		t.Fatal("expected an error")
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Errorf("partial file %s survived a failed download", dest)
	}
}

func TestRunUpload(t *testing.T) {
	var gotPath, gotType, gotBody string
	client := newMockClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotType = r.URL.Path, r.Header.Get("Content-Type")
		var b bytes.Buffer
		_, _ = b.ReadFrom(r.Body)
		gotBody = b.String()
		w.Header().Set("ETag", `"put-etag"`)
		w.WriteHeader(http.StatusOK)
	})

	file := filepath.Join(t.TempDir(), "dump.sql.gz")
	if err := os.WriteFile(file, []byte("backup"), 0o600); err != nil {
		t.Fatal(err)
	}

	// No key given: the file's basename is used.
	var buf bytes.Buffer
	if err := runUpload(context.Background(), client, valueOpts(), file, "db-backups", "", &buf); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/db-backups/dump.sql.gz" {
		t.Errorf("path = %q, want the basename as the key", gotPath)
	}
	if gotBody != "backup" {
		t.Errorf("body = %q", gotBody)
	}
	if gotType != "application/gzip" && gotType != "application/x-gzip" {
		t.Errorf("content type = %q, want it guessed from the extension", gotType)
	}
	if !strings.Contains(buf.String(), "put-etag") {
		t.Errorf("summary %q does not report the ETag", buf.String())
	}

	// A key ending in "/" is a prefix, not a key.
	if err := runUpload(context.Background(), client, valueOpts(), file, "db-backups", "2026/", &buf); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/db-backups/2026/dump.sql.gz" {
		t.Errorf("path = %q, want the basename appended to the prefix", gotPath)
	}

	if err := runUpload(context.Background(), client, valueOpts(), filepath.Dir(file), "db-backups", "", &buf); err == nil {
		t.Error("uploading a directory was accepted")
	}
}

func TestParseRef(t *testing.T) {
	tests := []struct {
		in         string
		bucket     string
		key        string
		wantErr    bool
		keyIsError bool
	}{
		{in: "db-backups", bucket: "db-backups", keyIsError: true},
		{in: "db-backups/e2e-a.sql.gz", bucket: "db-backups", key: "e2e-a.sql.gz"},
		{in: "s3://db-backups/dir/a b.gz", bucket: "db-backups", key: "dir/a b.gz"},
		{in: "s3://", wantErr: true},
		{in: "", wantErr: true},
	}
	for _, tt := range tests {
		bucket, key, err := parseRef(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("parseRef(%q) err = %v, wantErr %v", tt.in, err, tt.wantErr)
			continue
		}
		if err != nil {
			continue
		}
		if bucket != tt.bucket || key != tt.key {
			t.Errorf("parseRef(%q) = %q, %q; want %q, %q", tt.in, bucket, key, tt.bucket, tt.key)
		}
		if _, _, err := parseObjectRef(tt.in); (err != nil) != tt.keyIsError {
			t.Errorf("parseObjectRef(%q) err = %v, want an error: %v", tt.in, err, tt.keyIsError)
		}
	}
}

func TestParseCredsRef(t *testing.T) {
	tests := []struct{ in, ns, name, key string }{
		{"lcm-gitlab", "lcm-gitlab", defaultCredsSecret, ""},
		{"lcm-gitlab/gitlab-object-storage", "lcm-gitlab", "gitlab-object-storage", ""},
		{"lcm-gitlab/my-secret:config", "lcm-gitlab", "my-secret", "config"},
	}
	for _, tt := range tests {
		ns, name, key := parseCredsRef(tt.in)
		if ns != tt.ns || name != tt.name || key != tt.key {
			t.Errorf("parseCredsRef(%q) = %q, %q, %q; want %q, %q, %q",
				tt.in, ns, name, key, tt.ns, tt.name, tt.key)
		}
	}
}

// TestCredsFromSecret covers the three Secret shapes S3 credentials actually
// appear in on a cluster. The gitlabConfig case is the important one: GitLab's
// object-storage Secret holds the whole connection as a single YAML value, which
// is why the parser looks inside values as well as at their key names.
func TestCredsFromSecret(t *testing.T) {
	const gitlabConfig = `provider: AWS
region: garage
aws_access_key_id: GK8cbef8e574865609c90ac3bb
aws_secret_access_key: 0123456789abcdef
endpoint: https://s3.example.com
path_style: true
`
	tests := []struct {
		name    string
		data    map[string][]byte
		wantKey string
		creds   secretCreds
	}{
		{
			name: "discrete keys",
			data: map[string][]byte{
				"access_key": []byte("GKdiscrete"),
				"secret_key": []byte("s3cr3t"),
			},
			creds: secretCreds{accessKey: "GKdiscrete", secretKey: "s3cr3t"},
		},
		{
			name: "aws env spelling",
			data: map[string][]byte{
				"AWS_ACCESS_KEY_ID":     []byte("GKenv"),
				"AWS_SECRET_ACCESS_KEY": []byte("s3cr3t"),
				"AWS_ENDPOINT_URL":      []byte("https://s3.example.com"),
			},
			creds: secretCreds{accessKey: "GKenv", secretKey: "s3cr3t", endpoint: "https://s3.example.com"},
		},
		{
			name:    "gitlab config blob",
			data:    map[string][]byte{"config": []byte(gitlabConfig)},
			wantKey: "config",
			creds: secretCreds{
				accessKey: "GK8cbef8e574865609c90ac3bb",
				secretKey: "0123456789abcdef",
				endpoint:  "https://s3.example.com",
				region:    "garage",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := credsFromSecret(tt.data, tt.wantKey)
			if err != nil {
				t.Fatal(err)
			}
			if got.accessKey != tt.creds.accessKey || got.secretKey != tt.creds.secretKey {
				t.Errorf("keys = %q/%q, want %q/%q", got.accessKey, got.secretKey, tt.creds.accessKey, tt.creds.secretKey)
			}
			if got.endpoint != tt.creds.endpoint {
				t.Errorf("endpoint = %q, want %q", got.endpoint, tt.creds.endpoint)
			}
			if got.region != tt.creds.region {
				t.Errorf("region = %q, want %q", got.region, tt.creds.region)
			}
		})
	}

	// path_style: true must survive the blob parser, since an endpoint URL's own
	// colon sits after the separator on that same line format.
	got, err := credsFromSecret(map[string][]byte{"config": []byte(gitlabConfig)}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.pathStyle == nil || !*got.pathStyle {
		t.Errorf("path_style = %v, want true", got.pathStyle)
	}

	if _, err := credsFromSecret(map[string][]byte{"unrelated": []byte("x")}, ""); err == nil {
		t.Error("a Secret with no credentials was accepted")
	}
	if _, err := credsFromSecret(map[string][]byte{"config": []byte(gitlabConfig)}, "nope"); err == nil {
		t.Error("a missing --s3-creds-from-ns key was accepted")
	}
}

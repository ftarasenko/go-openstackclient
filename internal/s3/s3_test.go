package s3

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestClient points a client at a mock endpoint, path-style, with the
// credentials every test in this file signs with.
func newTestClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	c, err := New(Config{
		Endpoint:  srv.URL,
		Region:    "garage",
		AccessKey: "GKtest",
		SecretKey: "secret",
		PathStyle: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// assertSigned checks the properties every koc S3 request must have, whatever
// the verb: a SigV4 Authorization header naming the key and the scope, and the
// payload hash header it covers.
func assertSigned(t *testing.T, r *http.Request) {
	t.Helper()
	auth := r.Header.Get("Authorization")
	for _, want := range []string{
		"AWS4-HMAC-SHA256 ",
		"Credential=GKtest/",
		"/garage/s3/aws4_request",
		"Signature=",
	} {
		if !strings.Contains(auth, want) {
			t.Errorf("Authorization %q missing %q", auth, want)
		}
	}
	// host is always signed; where it sorts depends on which other headers the
	// verb sets, so assert membership rather than position.
	for _, want := range []string{"host", "x-amz-content-sha256", "x-amz-date"} {
		if !strings.Contains(auth, "SignedHeaders=") || !signsHeader(auth, want) {
			t.Errorf("Authorization %q does not sign %q", auth, want)
		}
	}
	if r.Header.Get("X-Amz-Content-Sha256") == "" {
		t.Error("missing X-Amz-Content-Sha256")
	}
	if r.Header.Get("X-Amz-Date") == "" {
		t.Error("missing X-Amz-Date")
	}
}

const listBucketsBody = `<?xml version="1.0" encoding="UTF-8"?>
<ListAllMyBucketsResult>
  <Owner><ID>owner</ID></Owner>
  <Buckets>
    <Bucket><Name>db-backups</Name><CreationDate>2026-08-21T05:48:02.364Z</CreationDate></Bucket>
    <Bucket><Name>gitlab-artifacts</Name><CreationDate>2026-08-21T05:47:00.000Z</CreationDate></Bucket>
  </Buckets>
</ListAllMyBucketsResult>`

func TestListBuckets(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assertSigned(t, r)
		if r.Method != http.MethodGet || r.URL.Path != "/" {
			t.Errorf("got %s %s, want GET /", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(listBucketsBody))
	})

	buckets, err := c.ListBuckets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 2 {
		t.Fatalf("got %d buckets, want 2", len(buckets))
	}
	if buckets[0].Name != "db-backups" {
		t.Errorf("name = %q", buckets[0].Name)
	}
	if got := buckets[0].CreationDate.Format("2006-01-02T15:04:05"); got != "2026-08-21T05:48:02" {
		t.Errorf("creation date = %q", got)
	}
}

// TestListObjectsPaging checks that continuation tokens are followed, that the
// prefix reaches the wire, and that the ETag's quotes are stripped.
func TestListObjectsPaging(t *testing.T) {
	var paths []string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assertSigned(t, r)
		paths = append(paths, r.URL.RequestURI())
		if r.URL.Path != "/db-backups" {
			t.Errorf("path = %q, want /db-backups", r.URL.Path)
		}
		if got := r.URL.Query().Get("prefix"); got != "e2e-" {
			t.Errorf("prefix = %q", got)
		}
		if r.URL.Query().Get("continuation-token") == "" {
			_, _ = fmt.Fprint(w, `<ListBucketResult><IsTruncated>true</IsTruncated>
				<NextContinuationToken>tok2</NextContinuationToken>
				<Contents><Key>e2e-a.sql.gz</Key><Size>14000000</Size>
				<LastModified>2026-08-21T06:00:00.000Z</LastModified><ETag>"abc"</ETag></Contents>
				</ListBucketResult>`)
			return
		}
		_, _ = fmt.Fprint(w, `<ListBucketResult><IsTruncated>false</IsTruncated>
			<Contents><Key>e2e-a.sql.gz.sha256</Key><Size>96</Size>
			<LastModified>2026-08-21T06:00:01.000Z</LastModified><ETag>"def"</ETag></Contents>
			</ListBucketResult>`)
	})

	objs, err := c.ListObjects(context.Background(), "db-backups", "e2e-", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 2 {
		t.Fatalf("got %d objects, want 2 across both pages", len(objs))
	}
	if objs[0].ETag != "abc" {
		t.Errorf("ETag = %q, want the quotes stripped", objs[0].ETag)
	}
	if objs[0].Size != 14000000 {
		t.Errorf("size = %d", objs[0].Size)
	}
	if len(paths) != 2 || !strings.Contains(paths[1], "continuation-token=tok2") {
		t.Errorf("requests = %v, want a second page keyed on the token", paths)
	}
	if !strings.Contains(paths[0], "list-type=2") {
		t.Errorf("first request %q is not a ListObjectsV2", paths[0])
	}
}

// TestListObjectsLimit pins --limit as a hard result cap: the request asks for
// only as many keys as are still wanted, and paging stops at the cap even
// though the server says there is more.
func TestListObjectsLimit(t *testing.T) {
	calls := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if got := r.URL.Query().Get("max-keys"); got != "1" {
			t.Errorf("max-keys = %q, want 1", got)
		}
		_, _ = fmt.Fprint(w, `<ListBucketResult><IsTruncated>true</IsTruncated>
			<NextContinuationToken>more</NextContinuationToken>
			<Contents><Key>a</Key><Size>1</Size></Contents>
			<Contents><Key>b</Key><Size>2</Size></Contents>
			</ListBucketResult>`)
	})

	objs, err := c.ListObjects(context.Background(), "b", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 1 || objs[0].Key != "a" {
		t.Errorf("objects = %v, want exactly the first", objs)
	}
	if calls != 1 {
		t.Errorf("made %d requests, want 1 — the cap should stop paging", calls)
	}
}

func TestHeadObject(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assertSigned(t, r)
		if r.Method != http.MethodHead {
			t.Errorf("method = %s, want HEAD", r.Method)
		}
		if r.URL.Path != "/db-backups/e2e-a.sql.gz" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Length", "14000000")
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("ETag", `"abc"`)
		w.Header().Set("Last-Modified", "Fri, 21 Aug 2026 06:00:00 GMT")
		w.Header().Set("X-Amz-Meta-Region", "e2e")
		w.WriteHeader(http.StatusOK)
	})

	info, err := c.HeadObject(context.Background(), "db-backups", "e2e-a.sql.gz")
	if err != nil {
		t.Fatal(err)
	}
	if info.Size != 14000000 {
		t.Errorf("size = %d", info.Size)
	}
	if info.ETag != "abc" || info.ContentType != "application/gzip" {
		t.Errorf("etag = %q, content type = %q", info.ETag, info.ContentType)
	}
	if info.Metadata["region"] != "e2e" {
		t.Errorf("metadata = %v, want the x-amz-meta- prefix stripped", info.Metadata)
	}
	if info.LastModified.IsZero() {
		t.Error("last modified not parsed")
	}
}

func TestGetObject(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assertSigned(t, r)
		if r.URL.Path != "/db-backups/dir/a b.txt" {
			t.Errorf("path = %q, want the key decoded back to its literal form", r.URL.Path)
		}
		_, _ = w.Write([]byte("payload"))
	})

	var buf bytes.Buffer
	n, err := c.GetObject(context.Background(), "db-backups", "dir/a b.txt", &buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != 7 || buf.String() != "payload" {
		t.Errorf("got %d bytes %q", n, buf.String())
	}
}

// TestPutObject checks the two things a signed upload must get right: the body
// arrives intact, and the payload hash header is the hash of that body rather
// than the empty-body constant.
func TestPutObject(t *testing.T) {
	const body = "backup bytes"
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assertSigned(t, r)
		if r.Method != http.MethodPut {
			t.Errorf("method = %s, want PUT", r.Method)
		}
		got, _ := readAll(r)
		if got != body {
			t.Errorf("body = %q, want %q", got, body)
		}
		if h := r.Header.Get("X-Amz-Content-Sha256"); h != hexSHA256([]byte(body)) {
			t.Errorf("payload hash = %q, want the hash of the body", h)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/gzip" {
			t.Errorf("content type = %q", ct)
		}
		w.Header().Set("ETag", `"put-etag"`)
		w.WriteHeader(http.StatusOK)
	})

	info, err := c.PutObject(context.Background(), "db-backups", "new.gz",
		strings.NewReader(body), int64(len(body)), "application/gzip")
	if err != nil {
		t.Fatal(err)
	}
	if info.ETag != "put-etag" {
		t.Errorf("etag = %q", info.ETag)
	}
}

// signsHeader reports whether name appears in the Authorization header's
// SignedHeaders list.
func signsHeader(auth, name string) bool {
	_, rest, ok := strings.Cut(auth, "SignedHeaders=")
	if !ok {
		return false
	}
	list, _, _ := strings.Cut(rest, ",")
	for _, h := range strings.Split(list, ";") {
		if h == name {
			return true
		}
	}
	return false
}

func readAll(r *http.Request) (string, error) {
	var buf bytes.Buffer
	_, err := buf.ReadFrom(r.Body)
	return buf.String(), err
}

// TestAPIError checks that S3's XML error document becomes a typed error a
// caller can match on, and that IsNotFound covers both the coded and the
// bodiless (HEAD) forms.
func TestAPIError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Error>` +
			`<Code>AccessDenied</Code><Message>Forbidden: No such key: GKmissing</Message>` +
			`<Resource>/db-backups/</Resource><Region>garage</Region></Error>`))
	})

	_, err := c.ListObjects(context.Background(), "db-backups", "", 0)
	var apiErr *APIError
	if !asAPIError(err, &apiErr) {
		t.Fatalf("err = %v (%T), want *APIError", err, err)
	}
	if apiErr.Code != "AccessDenied" || apiErr.StatusCode != http.StatusForbidden {
		t.Errorf("code = %q, status = %d", apiErr.Code, apiErr.StatusCode)
	}
	if !strings.Contains(err.Error(), "No such key") {
		t.Errorf("error text %q drops the server's message", err)
	}
	if IsNotFound(err) {
		t.Error("AccessDenied must not read as not-found")
	}

	_, err = c.HeadObject(context.Background(), "db-backups", "missing")
	if !IsNotFound(err) {
		t.Errorf("bodiless 404 = %v, want IsNotFound", err)
	}
}

func asAPIError(err error, target **APIError) bool {
	ae, ok := err.(*APIError) //nolint:errorlint // the client returns it unwrapped; the wrapped case is covered by the CLI tests
	if ok {
		*target = ae
	}
	return ok
}

// TestURLStyles pins bucket addressing in both modes.
func TestURLStyles(t *testing.T) {
	path, err := New(Config{Endpoint: "https://s3.example.com", AccessKey: "a", SecretKey: "b", PathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := path.url("bucket", "key", nil).String(); got != "https://s3.example.com/bucket/key" {
		t.Errorf("path-style URL = %q", got)
	}

	host, err := New(Config{Endpoint: "https://s3.example.com", AccessKey: "a", SecretKey: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if got := host.url("bucket", "key", nil).String(); got != "https://bucket.s3.example.com/key" {
		t.Errorf("virtual-host URL = %q", got)
	}
}

// TestNewValidation covers the config errors an operator is most likely to hit,
// including the bare-host endpoint the installer's s3_host variable holds.
func TestNewValidation(t *testing.T) {
	if _, err := New(Config{AccessKey: "a", SecretKey: "b"}); err == nil {
		t.Error("missing endpoint accepted")
	}
	if _, err := New(Config{Endpoint: "https://s3.example.com"}); err == nil {
		t.Error("missing credentials accepted")
	}
	if _, err := New(Config{Endpoint: "ftp://s3.example.com", AccessKey: "a", SecretKey: "b"}); err == nil {
		t.Error("non-HTTP scheme accepted")
	}

	c, err := New(Config{Endpoint: "s3.example.com", AccessKey: "a", SecretKey: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Endpoint() != "https://s3.example.com" {
		t.Errorf("bare host became %q, want https", c.Endpoint())
	}
	if c.Region() != DefaultRegion {
		t.Errorf("region = %q, want the %q default", c.Region(), DefaultRegion)
	}

	// A copied console URL must not end up prefixed to every key.
	c, err = New(Config{Endpoint: "https://s3.example.com/browser/", AccessKey: "a", SecretKey: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Endpoint() != "https://s3.example.com" {
		t.Errorf("endpoint path not stripped: %q", c.Endpoint())
	}
}

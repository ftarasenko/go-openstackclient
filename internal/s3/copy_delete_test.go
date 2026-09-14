package s3

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestCopyObject(t *testing.T) {
	var gotMethod, gotPath, gotSource, gotDirective string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assertSigned(t, r)
		gotMethod, gotPath = r.Method, r.URL.Path
		gotSource = r.Header.Get("x-amz-copy-source")
		gotDirective = r.Header.Get("x-amz-metadata-directive")
		_, _ = fmt.Fprint(w, `<CopyObjectResult><ETag>"copied"</ETag>`+
			`<LastModified>2026-09-14T10:00:00.000Z</LastModified></CopyObjectResult>`)
	})

	src := ObjectRef{Bucket: "db-backups", Key: "nightly.sql.gz"}
	dst := ObjectRef{Bucket: "archive", Key: "2026/nightly.sql.gz"}
	obj, err := c.CopyObject(context.Background(), src, dst, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPut || gotPath != "/archive/2026/nightly.sql.gz" {
		t.Errorf("request = %s %s, want PUT the destination", gotMethod, gotPath)
	}
	if gotSource != "/db-backups/nightly.sql.gz" {
		t.Errorf("x-amz-copy-source = %q", gotSource)
	}
	// Without --content-type the source's metadata is carried over, which is
	// S3's default and must not be overridden.
	if gotDirective != "" {
		t.Errorf("x-amz-metadata-directive = %q, want it unset", gotDirective)
	}
	if obj.ETag != "copied" {
		t.Errorf("ETag = %q, want copied", obj.ETag)
	}
	if obj.LastModified.IsZero() {
		t.Error("LastModified was not parsed")
	}
}

// A key with a space or a "+" has to reach the header percent-encoded, with its
// slashes intact — the copy source is a path, and the signature covers it.
func TestCopyObjectEncodesTheSource(t *testing.T) {
	var gotSource string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotSource = r.Header.Get("x-amz-copy-source")
		_, _ = fmt.Fprint(w, `<CopyObjectResult><ETag>"e"</ETag></CopyObjectResult>`)
	})

	src := ObjectRef{Bucket: "b", Key: "dir/a b+c.txt"}
	if _, err := c.CopyObject(context.Background(), src, ObjectRef{Bucket: "b", Key: "d"}, "", ""); err != nil {
		t.Fatal(err)
	}
	if gotSource != "/b/dir/a%20b%2Bc.txt" {
		t.Errorf("x-amz-copy-source = %q, want the key encoded and the slashes kept", gotSource)
	}
}

// Replacing the content type needs the REPLACE directive, or S3 keeps the
// source's and ignores the header.
func TestCopyObjectReplacesMetadataForANewContentType(t *testing.T) {
	var gotType, gotDirective string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotType, gotDirective = r.Header.Get("Content-Type"), r.Header.Get("x-amz-metadata-directive")
		_, _ = fmt.Fprint(w, `<CopyObjectResult><ETag>"e"</ETag></CopyObjectResult>`)
	})

	_, err := c.CopyObject(context.Background(),
		ObjectRef{Bucket: "b", Key: "k"}, ObjectRef{Bucket: "b", Key: "k2"}, "", "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	if gotType != "text/plain" || gotDirective != "REPLACE" {
		t.Errorf("Content-Type = %q, directive = %q; want text/plain and REPLACE", gotType, gotDirective)
	}
}

// A version ID travels in the copy-source header as a query string, not as a
// request parameter.
func TestCopyObjectCarriesTheSourceVersion(t *testing.T) {
	var gotSource string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotSource = r.Header.Get("x-amz-copy-source")
		_, _ = fmt.Fprint(w, `<CopyObjectResult><ETag>"e"</ETag></CopyObjectResult>`)
	})

	_, err := c.CopyObject(context.Background(),
		ObjectRef{Bucket: "b", Key: "k"}, ObjectRef{Bucket: "b", Key: "k2"}, "v7", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotSource, "versionId%3Dv7") && !strings.Contains(gotSource, "versionId=v7") {
		t.Errorf("x-amz-copy-source = %q, want it to name version v7", gotSource)
	}
}

// Like a multipart completion, a copy can be refused inside a 200.
func TestCopyObjectDetectsAnErrorInsideA200(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `<Error><Code>SlowDown</Code><Message>try later</Message></Error>`)
	})

	_, err := c.CopyObject(context.Background(),
		ObjectRef{Bucket: "b", Key: "k"}, ObjectRef{Bucket: "b", Key: "k2"}, "", "")
	if err == nil {
		t.Fatal("an <Error> inside a 200 was reported as a successful copy")
	}
	if got := ErrorCode(err); got != "SlowDown" {
		t.Errorf("ErrorCode = %q, want SlowDown", got)
	}
}

func TestDeleteObjectsBatch(t *testing.T) {
	var gotMethod, gotBody, gotMD5, gotType string
	var hasDeleteParam bool
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assertSigned(t, r)
		body, _ := io.ReadAll(r.Body)
		gotMethod, gotBody = r.Method, string(body)
		gotMD5, gotType = r.Header.Get("Content-MD5"), r.Header.Get("Content-Type")
		hasDeleteParam = r.URL.Query().Has("delete")
		_, _ = fmt.Fprint(w, `<DeleteResult></DeleteResult>`)
	})

	targets := []DeleteTarget{{Key: "a"}, {Key: "b", VersionID: "v1"}}
	failures, err := c.DeleteObjects(context.Background(), "db-backups", targets)
	if err != nil {
		t.Fatal(err)
	}
	if len(failures) != 0 {
		t.Errorf("failures = %v, want none", failures)
	}
	if gotMethod != http.MethodPost || !hasDeleteParam {
		t.Errorf("request = %s (delete param: %v), want POST ?delete", gotMethod, hasDeleteParam)
	}
	if gotType != "application/xml" {
		t.Errorf("Content-Type = %q", gotType)
	}
	// Quiet mode: only failures come back, which is all this call reports.
	for _, want := range []string{"<Quiet>true</Quiet>", "<Key>a</Key>", "<Key>b</Key>", "<VersionId>v1</VersionId>"} {
		if !strings.Contains(gotBody, want) {
			t.Errorf("body %s missing %q", gotBody, want)
		}
	}
	// AWS refuses a batch delete without the Content-MD5 of the body.
	sum := md5.Sum([]byte(gotBody))
	if want := base64.StdEncoding.EncodeToString(sum[:]); gotMD5 != want {
		t.Errorf("Content-MD5 = %q, want %q", gotMD5, want)
	}
}

// S3 reports per-key refusals inside a 200, so they are returned rather than
// raised: the rest of the batch did happen.
func TestDeleteObjectsReportsPerKeyFailures(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `<DeleteResult>
			<Error><Key>locked</Key><Code>AccessDenied</Code><Message>no</Message></Error>
			</DeleteResult>`)
	})

	failures, err := c.DeleteObjects(context.Background(), "b", []DeleteTarget{{Key: "ok"}, {Key: "locked"}})
	if err != nil {
		t.Fatalf("a partial failure must not fail the call: %v", err)
	}
	if len(failures) != 1 || failures[0].Key != "locked" || failures[0].Code != "AccessDenied" {
		t.Fatalf("failures = %+v, want the one refused key", failures)
	}
	if got := failures[0].Error(); !strings.Contains(got, "locked") || !strings.Contains(got, "AccessDenied") {
		t.Errorf("failure message %q does not name the key and the code", got)
	}
}

func TestDeleteObjectsRejectsAnOversizedBatch(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("an oversized batch must be refused before any request")
		w.WriteHeader(http.StatusOK)
	})

	targets := make([]DeleteTarget, MaxDeleteBatch+1)
	if _, err := c.DeleteObjects(context.Background(), "b", targets); err == nil {
		t.Fatal("an oversized batch was accepted")
	}
}

// An empty batch is a no-op, not a malformed request.
func TestDeleteObjectsOnAnEmptyBatch(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("an empty batch must make no request")
		w.WriteHeader(http.StatusOK)
	})

	failures, err := c.DeleteObjects(context.Background(), "b", nil)
	if err != nil || failures != nil {
		t.Errorf("DeleteObjects(nil) = %v, %v; want no-op", failures, err)
	}
}

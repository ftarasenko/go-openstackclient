package s3

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func TestCreateBucketSendsLocationConstraint(t *testing.T) {
	var gotMethod, gotPath, gotBody string
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assertSigned(t, r)
		body, _ := io.ReadAll(r.Body)
		gotMethod, gotPath, gotBody = r.Method, r.URL.Path, string(body)
		w.WriteHeader(http.StatusOK)
	})

	if err := client.CreateBucket(context.Background(), "scratch"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
	if gotPath != "/scratch" {
		t.Errorf("path = %q, want /scratch", gotPath)
	}
	// The signing region is the location constraint: a request signed for one
	// region is not served by another, so there is nothing else it could be.
	want := "<CreateBucketConfiguration><LocationConstraint>garage</LocationConstraint></CreateBucketConfiguration>"
	if gotBody != want {
		t.Errorf("body = %q, want %q", gotBody, want)
	}
}

// us-east-1 is the one region AWS rejects a location constraint for, so the
// body must be absent rather than naming it.
func TestCreateBucketOmitsBodyForUSEast1(t *testing.T) {
	var gotBody string
	var gotLen int64
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody, gotLen = string(body), r.ContentLength
		w.WriteHeader(http.StatusOK)
	})
	client.cfg.Region = usEast1

	if err := client.CreateBucket(context.Background(), "scratch"); err != nil {
		t.Fatal(err)
	}
	if gotBody != "" {
		t.Errorf("body = %q, want empty", gotBody)
	}
	if gotLen > 0 {
		t.Errorf("Content-Length = %d, want 0", gotLen)
	}
}

func TestCreateBucketAlreadyOwnedReportsCode(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = fmt.Fprint(w, `<Error><Code>BucketAlreadyOwnedByYou</Code>`+
			`<Message>Bucket already exists</Message></Error>`)
	})

	err := client.CreateBucket(context.Background(), "scratch")
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := ErrorCode(err); got != "BucketAlreadyOwnedByYou" {
		t.Errorf("ErrorCode = %q, want BucketAlreadyOwnedByYou", got)
	}
}

func TestDeleteBucket(t *testing.T) {
	var gotMethod, gotPath string
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assertSigned(t, r)
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})

	if err := client.DeleteBucket(context.Background(), "scratch"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/scratch" {
		t.Errorf("request = %s %s, want DELETE /scratch", gotMethod, gotPath)
	}
}

func TestDeleteBucketNotEmptyReportsCode(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = fmt.Fprint(w, `<Error><Code>BucketNotEmpty</Code><Message>nope</Message></Error>`)
	})

	err := client.DeleteBucket(context.Background(), "scratch")
	if got := ErrorCode(err); got != "BucketNotEmpty" {
		t.Errorf("ErrorCode = %q, want BucketNotEmpty (err = %v)", got, err)
	}
}

func TestDeleteObject(t *testing.T) {
	var gotMethod, gotPath string
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assertSigned(t, r)
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})

	if err := client.DeleteObject(context.Background(), "db-backups", "dump.sql.gz"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/db-backups/dump.sql.gz" {
		t.Errorf("request = %s %s, want DELETE /db-backups/dump.sql.gz", gotMethod, gotPath)
	}
}

// ErrorCode must not claim a code for an error that never came from S3.
func TestErrorCodeOnForeignError(t *testing.T) {
	if got := ErrorCode(io.EOF); got != "" {
		t.Errorf("ErrorCode(io.EOF) = %q, want empty", got)
	}
	if got := ErrorCode(nil); got != "" {
		t.Errorf("ErrorCode(nil) = %q, want empty", got)
	}
}

// ListObjectsFunc must follow continuation tokens and stop the walk the moment
// the limit is reached, without asking for a page it cannot use.
func TestListObjectsFuncPagesAndCapsLimit(t *testing.T) {
	var maxKeys []string
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		maxKeys = append(maxKeys, r.URL.Query().Get("max-keys"))
		if r.URL.Query().Get("continuation-token") == "" {
			_, _ = fmt.Fprint(w, `<ListBucketResult><IsTruncated>true</IsTruncated>
				<NextContinuationToken>tok</NextContinuationToken>
				<Contents><Key>a</Key><Size>1</Size></Contents>
				<Contents><Key>b</Key><Size>2</Size></Contents>
				</ListBucketResult>`)
			return
		}
		_, _ = fmt.Fprint(w, `<ListBucketResult><IsTruncated>false</IsTruncated>
			<Contents><Key>c</Key><Size>4</Size></Contents>
			</ListBucketResult>`)
	})

	var keys []string
	var sum int64
	err := client.ListObjectsFunc(context.Background(), "b", "", 3, func(o Object) error {
		keys = append(keys, o.Key)
		sum += o.Size
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(keys, ","); got != "a,b,c" {
		t.Errorf("keys = %q, want a,b,c", got)
	}
	if sum != 7 {
		t.Errorf("sum = %d, want 7", sum)
	}
	// Page two may ask for at most the one key still wanted.
	if len(maxKeys) != 2 {
		t.Fatalf("requests = %d, want 2", len(maxKeys))
	}
	if n, _ := strconv.Atoi(maxKeys[1]); n != 1 {
		t.Errorf("second max-keys = %q, want 1", maxKeys[1])
	}
}

// An error from the callback stops the walk and reaches the caller unwrapped,
// which is what lets a delete-as-you-list abort on the first failure.
func TestListObjectsFuncPropagatesCallbackError(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `<ListBucketResult><IsTruncated>false</IsTruncated>
			<Contents><Key>a</Key></Contents><Contents><Key>b</Key></Contents>
			</ListBucketResult>`)
	})

	seen := 0
	err := client.ListObjectsFunc(context.Background(), "b", "", 0, func(Object) error {
		seen++
		return io.ErrUnexpectedEOF
	})
	if err != io.ErrUnexpectedEOF { //nolint:errorlint // identity is the assertion
		t.Errorf("err = %v, want io.ErrUnexpectedEOF", err)
	}
	if seen != 1 {
		t.Errorf("callback ran %d times, want 1", seen)
	}
}

package s3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// retryClient is newTestClient with retries on and a backoff short enough that
// a test does not spend real time asleep.
func retryClient(t *testing.T, retries int, h http.HandlerFunc) *Client {
	t.Helper()
	c := newTestClient(t, h)
	c.cfg.MaxRetries = retries
	c.retryBase = time.Microsecond
	return c
}

func TestRetryableClassification(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"transport failure", errors.New("dial tcp: connection refused"), true},
		{"500", &APIError{StatusCode: 500}, true},
		{"503 SlowDown", &APIError{StatusCode: 503, Code: "SlowDown"}, true},
		{"429", &APIError{StatusCode: 429}, true},
		{"408", &APIError{StatusCode: 408}, true},
		{"403", &APIError{StatusCode: 403, Code: "AccessDenied"}, false},
		{"404", &APIError{StatusCode: 404, Code: "NoSuchKey"}, false},
		{"clock skew is not retryable", &APIError{StatusCode: 403, Code: "RequestTimeTooSkewed"}, false},
		{"a sink failure is never replayed", &nonRetryableError{err: errors.New("short write")}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := retryable(tc.err); got != tc.want {
				t.Errorf("retryable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestDoRetriesA503ThenSucceeds(t *testing.T) {
	var calls atomic.Int32
	c := retryClient(t, 3, func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprint(w, `<Error><Code>SlowDown</Code></Error>`)
			return
		}
		_, _ = fmt.Fprint(w, `<ListAllMyBucketsResult><Buckets></Buckets></ListAllMyBucketsResult>`)
	})

	if _, err := c.ListBuckets(context.Background()); err != nil {
		t.Fatalf("a retryable failure was not retried: %v", err)
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("attempts = %d, want 3", got)
	}
}

// The retry budget is finite: past it the last error reaches the caller.
func TestDoGivesUpAfterMaxRetries(t *testing.T) {
	var calls atomic.Int32
	c := retryClient(t, 2, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	})

	if _, err := c.ListBuckets(context.Background()); err == nil {
		t.Fatal("expected the failure to be reported once the budget ran out")
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("attempts = %d, want 1 try + 2 retries", got)
	}
}

func TestDoDoesNotRetryA403(t *testing.T) {
	var calls atomic.Int32
	c := retryClient(t, 5, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprint(w, `<Error><Code>AccessDenied</Code></Error>`)
	})

	if _, err := c.ListBuckets(context.Background()); err == nil {
		t.Fatal("expected an error")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("attempts = %d, want 1 — a 403 is not worth repeating", got)
	}
}

// A retried PUT has to send the same bytes again, which is the whole reason
// request.body is an io.ReadSeeker.
func TestDoRewindsTheBodyOnRetry(t *testing.T) {
	var bodies []string
	var calls atomic.Int32
	c := retryClient(t, 1, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("ETag", `"abc"`)
		w.WriteHeader(http.StatusOK)
	})

	payload := "the whole dump"
	obj, err := c.PutObject(context.Background(), "b", "k", strings.NewReader(payload),
		int64(len(payload)), "application/octet-stream")
	if err != nil {
		t.Fatal(err)
	}
	if obj.ETag != "abc" {
		t.Errorf("ETag = %q, want abc", obj.ETag)
	}
	if len(bodies) != 2 || bodies[0] != payload || bodies[1] != payload {
		t.Errorf("bodies = %q, want the same payload twice", bodies)
	}
}

// A body read from the caller's current offset must rewind to *that* offset, not
// to byte zero: an upload of one slice of a file would otherwise re-send the
// wrong bytes.
func TestDoRewindsToTheCallerOffset(t *testing.T) {
	var bodies []string
	var calls atomic.Int32
	c := retryClient(t, 1, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	src := bytes.NewReader([]byte("SKIPMEpayload"))
	if _, err := src.Seek(6, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if _, err := c.PutObject(context.Background(), "b", "k", src, 7, ""); err != nil {
		t.Fatal(err)
	}
	for i, got := range bodies {
		if got != "payload" {
			t.Errorf("attempt %d sent %q, want payload", i+1, got)
		}
	}
}

// A failure raised while the sink was consuming the body must not be replayed:
// the bytes it already wrote to a file or to stdout cannot be taken back.
func TestDoDoesNotRetryASinkFailure(t *testing.T) {
	var calls atomic.Int32
	c := retryClient(t, 5, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte("payload"))
	})

	_, err := c.GetObject(context.Background(), "b", "k", "", failWriter{})
	if err == nil {
		t.Fatal("expected the sink failure to be reported")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("attempts = %d, want 1 — a partly written destination cannot be replayed", got)
	}
	// The caller must see its own error, not the internal marker.
	var marker *nonRetryableError
	if errors.As(err, &marker) {
		t.Errorf("error %v still carries the internal retry marker", err)
	}
}

// A cancelled context ends the loop instead of sleeping out the budget.
func TestDoStopsOnContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int32
	c := retryClient(t, 5, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		cancel()
		w.WriteHeader(http.StatusServiceUnavailable)
	})

	if _, err := c.ListBuckets(ctx); err == nil {
		t.Fatal("expected an error")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("attempts = %d, want 1 once the caller gave up", got)
	}
}

// Anonymous mode must send no credential at all — that is the point of it.
func TestAnonymousSendsNoSignature(t *testing.T) {
	var auth, sha string
	srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		auth, sha = r.Header.Get("Authorization"), r.Header.Get("X-Amz-Content-Sha256")
		_, _ = fmt.Fprint(w, `<ListBucketResult><IsTruncated>false</IsTruncated></ListBucketResult>`)
	})

	c, err := New(Config{Endpoint: srv.Endpoint(), Anonymous: true, PathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListObjects(context.Background(), "public", ListOptions{}); err != nil {
		t.Fatal(err)
	}
	if auth != "" {
		t.Errorf("Authorization = %q, want none", auth)
	}
	if sha != "" {
		t.Errorf("X-Amz-Content-Sha256 = %q, want none", sha)
	}
}

// Anonymous mode is the only one that needs no credentials; every other mode
// must still insist on them.
func TestNewRequiresCredentialsUnlessAnonymous(t *testing.T) {
	if _, err := New(Config{Endpoint: "https://s3.example.com"}); err == nil {
		t.Error("a client with no credentials was accepted")
	}
	if _, err := New(Config{Endpoint: "https://s3.example.com", Anonymous: true}); err != nil {
		t.Errorf("anonymous client rejected: %v", err)
	}
}

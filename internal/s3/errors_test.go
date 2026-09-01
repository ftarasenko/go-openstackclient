package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// APIError's message shape depends on how much the server told us: a full XML
// error document, a code with no message, or a bodiless status.
func TestAPIErrorMessageShapes(t *testing.T) {
	tests := []struct {
		name string
		err  *APIError
		want string
	}{
		{
			name: "code and message",
			err:  &APIError{StatusCode: 403, Method: "GET", Path: "/b/k", Code: "AccessDenied", Message: "no"},
			want: "s3 GET /b/k: Forbidden (AccessDenied): no",
		},
		{
			name: "code only",
			err:  &APIError{StatusCode: 404, Method: "HEAD", Path: "/b/k", Code: "NoSuchKey"},
			want: "s3 HEAD /b/k: Not Found (NoSuchKey)",
		},
		{
			name: "neither",
			err:  &APIError{StatusCode: 500, Method: "GET", Path: "/b/k"},
			want: "s3 GET /b/k: HTTP 500",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.Error(); got != tc.want {
				t.Errorf("Error() = %q, want %q", got, tc.want)
			}
		})
	}
}

// IsNotFound has to answer on the code where there is one and on the status
// where there is not, and must not claim anything about unrelated errors.
func TestIsNotFound(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"NoSuchKey", &APIError{StatusCode: 404, Code: "NoSuchKey"}, true},
		{"NoSuchBucket", &APIError{StatusCode: 404, Code: "NoSuchBucket"}, true},
		{"bodiless 404", &APIError{StatusCode: 404}, true},
		{"wrapped", fmt.Errorf("downloading: %w", &APIError{StatusCode: 404}), true},
		{"403", &APIError{StatusCode: 403, Code: "AccessDenied"}, false},
		{"not an APIError", errors.New("dial tcp: refused"), false},
		{"nil", nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsNotFound(tc.err); got != tc.want {
				t.Errorf("IsNotFound(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// A bodiless 404 is what HEAD returns, so newAPIError has to synthesise the
// code the caller matches on.
func TestNewAPIErrorSynthesisesNoSuchKey(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	_, err := c.HeadObject(context.Background(), "db-backups", "missing")
	if !IsNotFound(err) {
		t.Fatalf("a bodiless 404 was not recognised as not-found: %v", err)
	}
}

// The RequestId in the XML document wins over the header, since it is the one
// the server chose to report in the error itself.
func TestNewAPIErrorPrefersDocumentRequestID(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Amz-Request-Id", "from-header")
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprint(w, `<Error><Code>AccessDenied</Code><Message>no</Message>
			<Resource>/db-backups</Resource><RequestId>from-body</RequestId></Error>`)
	})

	_, err := c.ListBuckets(context.Background())
	var ae *APIError
	if !errors.As(err, &ae) {
		t.Fatalf("error is not an *APIError: %v", err)
	}
	if ae.RequestID != "from-body" {
		t.Errorf("RequestID = %q, want the document's", ae.RequestID)
	}
	if ae.Resource != "/db-backups" {
		t.Errorf("Resource = %q", ae.Resource)
	}
}

// A CA bundle that parses to nothing is an operator error worth naming, not a
// silent fallback to the system roots.
func TestNewRejectsUnparseableCABundle(t *testing.T) {
	_, err := New(Config{
		Endpoint: "https://s3.example.com", AccessKey: "a", SecretKey: "b",
		CACertPEM: []byte("not a certificate"),
	})
	if err == nil {
		t.Fatal("an unparseable CA bundle was accepted")
	}
	if !strings.Contains(err.Error(), "CA bundle") {
		t.Errorf("error %q does not name the CA bundle", err)
	}
}

// --insecure-s3 and a plain-HTTP endpoint must each announce themselves.
func TestNewWarnsOnInsecureAndCleartext(t *testing.T) {
	out := captureStderr(t, func() {
		if _, err := New(Config{
			Endpoint: "https://s3.example.com", AccessKey: "a", SecretKey: "b", Insecure: true,
		}); err != nil {
			t.Error(err)
		}
	})
	if !strings.Contains(out, "verification is disabled") {
		t.Errorf("no insecure warning: %q", out)
	}

	out = captureStderr(t, func() {
		if _, err := New(Config{
			Endpoint: "http://s3.example.com", AccessKey: "a", SecretKey: "b",
		}); err != nil {
			t.Error(err)
		}
	})
	if !strings.Contains(out, "plain HTTP") {
		t.Errorf("no cleartext warning: %q", out)
	}
}

// A malformed endpoint must be refused by name, and a bare host must not be
// mistaken for one.
func TestParseEndpointErrors(t *testing.T) {
	for _, ep := range []string{
		"https://",             // no host
		"http://[::1",          // unparseable
		"ftp://s3.example.com", // wrong scheme
	} {
		if _, err := parseEndpoint(ep); err == nil {
			t.Errorf("parseEndpoint(%q) accepted it", ep)
		}
	}
}

// PutObject signs a hash of the payload, so it needs a seekable reader; a body
// that cannot be rewound must fail before anything is sent.
func TestPutObjectRejectsUnseekableBody(t *testing.T) {
	called := false
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	_, err := c.PutObject(context.Background(), "db-backups", "key", badSeeker{}, 4, "")
	if err == nil {
		t.Fatal("an unseekable body was accepted")
	}
	if called {
		t.Error("the request was sent despite the body not being hashable")
	}
}

type badSeeker struct{}

func (badSeeker) Read([]byte) (int, error)       { return 0, io.EOF }
func (badSeeker) Seek(int64, int) (int64, error) { return 0, errors.New("not seekable") }

// A body that fails partway through the copy must surface as an error naming
// the object rather than a short, silently truncated download.
func TestGetObjectCopyError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("some bytes"))
	})

	_, err := c.GetObject(context.Background(), "db-backups", "key", failWriter{})
	if err == nil {
		t.Fatal("a failing writer was reported as success")
	}
	if !strings.Contains(err.Error(), "db-backups/key") {
		t.Errorf("error %q does not name the object", err)
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

// A body that is not the XML the caller expects must be an error, not an empty
// result that reads as "no buckets".
func TestGetXMLDecodeError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("this is not xml at all <<<"))
	})

	if _, err := c.ListBuckets(context.Background()); err == nil {
		t.Fatal("an undecodable body was reported as success")
	}
}

// A transport-level failure (nothing listening) must name the verb and path.
func TestDoTransportError(t *testing.T) {
	c, err := New(Config{
		// RFC 5737 TEST-NET-1, with a 1ms timeout so the dial cannot hang.
		Endpoint: "http://192.0.2.1", AccessKey: "a", SecretKey: "b",
		PathStyle: true, Timeout: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = c.HeadObject(context.Background(), "db-backups", "key")
	if err == nil {
		t.Fatal("an unreachable endpoint was reported as success")
	}
	if !strings.Contains(err.Error(), "s3 HEAD") {
		t.Errorf("error %q does not name the verb", err)
	}
}

// objectInfoFromHeader must trust the Content-Length header on a HEAD reply,
// where Go reports ContentLength as -1, and must tolerate a missing or
// unparseable Last-Modified.
func TestHeadObjectUsesContentLengthHeader(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "14200000")
		w.Header().Set("Last-Modified", "not a date")
		w.WriteHeader(http.StatusOK)
	})

	info, err := c.HeadObject(context.Background(), "db-backups", "key")
	if err != nil {
		t.Fatal(err)
	}
	if info.Size != 14200000 {
		t.Errorf("Size = %d, want the Content-Length header's value", info.Size)
	}
	if !info.LastModified.IsZero() {
		t.Errorf("an unparseable Last-Modified became %v", info.LastModified)
	}
}

// --debug prints one line per exchange, and never a body.
func TestDoDebugLogsTheExchange(t *testing.T) {
	srv := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, listBucketsBody)
	})
	srv.cfg.Debug = true

	out := captureStderr(t, func() {
		if _, err := srv.ListBuckets(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if !strings.Contains(out, "s3: GET") || !strings.Contains(out, "200") {
		t.Errorf("debug line missing: %q", out)
	}
	if strings.Contains(out, "db-backups") {
		t.Errorf("--debug leaked a response body: %q", out)
	}
}

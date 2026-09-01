package s3

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

// captureStderr collects what f writes to os.Stderr. This package's warnings go
// there directly (as internal/auth's and internal/vault's do), so asserting them
// means capturing the real stream.
func captureStderr(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w

	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()

	f()

	os.Stderr = old
	_ = w.Close()
	out := <-done
	_ = r.Close()
	return out
}

// req builds the minimal *http.Request sameHostRedirect inspects.
func req(t *testing.T, rawURL string, hdr map[string]string) *http.Request {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	r := &http.Request{URL: u, Header: http.Header{}}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	return r
}

// A redirect that stays on the origin host keeps the signature: SigV4 signs the
// Host header, so the upgraded request still verifies.
func TestSameHostRedirect_KeepsCredentialsOnTheSameHost(t *testing.T) {
	via := []*http.Request{req(t, "http://s3.example.com/bucket/key", nil)}
	next := req(t, "https://s3.example.com/bucket/key", map[string]string{
		"Authorization":        "AWS4-HMAC-SHA256 Credential=GKtest/...",
		"X-Amz-Date":           "20260821T060000Z",
		"X-Amz-Content-Sha256": emptySHA256,
	})

	if err := sameHostRedirect(next, via); err != nil {
		t.Fatalf("same-host redirect refused: %v", err)
	}
	// A scheme change is the common case (a gateway 301ing HTTP to HTTPS), not
	// an attack, so nothing is stripped.
	for _, h := range credentialHeaders {
		if h == "Authorization" && next.Header.Get(h) == "" {
			t.Errorf("%s was stripped on a same-host redirect", h)
		}
	}
	if next.Header.Get("X-Amz-Date") == "" {
		t.Error("X-Amz-Date was stripped on a same-host redirect")
	}
}

// Leaving the origin host invalidates the signature anyway; stripping makes the
// failure an honest AccessDenied instead of leaking a credential.
func TestSameHostRedirect_StripsCredentialsOffHost(t *testing.T) {
	via := []*http.Request{req(t, "https://s3.example.com/bucket/key", nil)}
	hdr := map[string]string{
		"Authorization":        "AWS4-HMAC-SHA256 Credential=GKtest/...",
		"Proxy-Authorization":  "Basic Zm9v",
		"Cookie":               "session=1",
		"X-Amz-Date":           "20260821T060000Z",
		"X-Amz-Content-Sha256": emptySHA256,
		"X-Amz-Security-Token": "token",
	}
	next := req(t, "https://elsewhere.example.com/bucket/key", hdr)

	if err := sameHostRedirect(next, via); err != nil {
		t.Fatalf("off-host redirect refused: %v", err)
	}
	for _, h := range credentialHeaders {
		if got := next.Header.Get(h); got != "" {
			t.Errorf("%s survived a redirect off the origin host: %q", h, got)
		}
	}
}

func TestSameHostRedirect_CapsTheChain(t *testing.T) {
	via := make([]*http.Request, maxRedirects)
	for i := range via {
		via[i] = req(t, "https://s3.example.com/bucket/key", nil)
	}

	err := sameHostRedirect(req(t, "https://s3.example.com/bucket/key", nil), via)
	if err == nil {
		t.Fatalf("a chain of %d redirects was allowed to continue", maxRedirects)
	}
	if !strings.Contains(err.Error(), "redirects") {
		t.Errorf("error %q does not mention redirects", err)
	}
}

// The redirect policy has to work through a real http.Client, since that is
// what installs it — and a same-host redirect must arrive still signed.
func TestNewHTTPClient_FollowsSameHostRedirectSigned(t *testing.T) {
	var authOnFinal string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, "/final", http.StatusMovedPermanently)
		default:
			authOnFinal = r.Header.Get("Authorization")
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	hc := newHTTPClient(nil, 0)
	r, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/redirect", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=GKtest/...")

	resp, err := hc.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if authOnFinal == "" {
		t.Error("the signature did not survive a same-host redirect")
	}
	if hc.Transport.(*http.Transport).ResponseHeaderTimeout != responseHeaderTimeout {
		t.Error("ResponseHeaderTimeout was not set")
	}
}

// Every path that disables verification or sends a credential in the clear must
// say so on stderr — and loopback is the documented exception.
func TestWarnings(t *testing.T) {
	if got := captureStderr(t, func() { warnInsecure("https://s3.example.com") }); !strings.Contains(got, "WARNING") {
		t.Errorf("warnInsecure printed %q", got)
	}
	if got := captureStderr(t, func() { warnCleartext("http://s3.example.com", "s3.example.com") }); !strings.Contains(got, "WARNING") {
		t.Errorf("warnCleartext printed %q", got)
	}
	for _, host := range []string{"localhost", "127.0.0.1", "::1"} {
		if got := captureStderr(t, func() { warnCleartext("http://"+host, host) }); got != "" {
			t.Errorf("warnCleartext warned about loopback %s: %q", host, got)
		}
	}
}

package vault

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// captureStderr runs f with os.Stderr replaced by a pipe and returns what was
// written, so the mandated TLS warnings can be asserted.
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

// A Vault 307 to the active node inside the same host must keep the token; one
// that leaves the host must not. X-Vault-Token is invisible to net/http's own
// cross-host stripping, so before the fix it reached a foreign host verbatim.
func TestVaultClient_RedirectDropsTokenCrossHost(t *testing.T) {
	var foreignToken string
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		foreignToken = r.Header.Get("X-Vault-Token")
		_, _ = w.Write([]byte(`{"data":{"data":{"k":"v"}}}`))
	}))
	defer foreign.Close()

	var sameHostToken string
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/kv/data/cross":
			http.Redirect(w, r, foreign.URL+"/v1/kv/data/cross", http.StatusTemporaryRedirect)
		case "/v1/kv/data/local":
			http.Redirect(w, r, "/v1/kv/data/landed", http.StatusTemporaryRedirect)
		default:
			sameHostToken = r.Header.Get("X-Vault-Token")
			_, _ = w.Write([]byte(`{"data":{"data":{"k":"v"}}}`))
		}
	}))
	defer origin.Close()

	c, err := New(context.Background(), Config{Addr: origin.URL, Token: "hvs.secret", KVMount: "kv"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if _, err := c.ReadKVDataAt(ctx, "kv", "local", 0); err != nil {
		t.Fatalf("same-host redirect should succeed: %v", err)
	}
	if sameHostToken != "hvs.secret" {
		t.Errorf("same-host redirect lost the token (got %q)", sameHostToken)
	}

	_, _ = c.ReadKVDataAt(ctx, "kv", "cross", 0)
	if foreignToken != "" {
		t.Errorf("a foreign host received the Vault token: %q", foreignToken)
	}
}

func TestVaultClient_UsesDefaultTransportAndTimeout(t *testing.T) {
	c, err := New(context.Background(), Config{Addr: "https://vault.example.com", Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	tr, ok := c.hc.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T, want *http.Transport", c.hc.Transport)
	}
	// A hand-rolled &http.Transport{} loses all of these; a DefaultTransport clone
	// keeps them, which is what a proxied air-gapped deployment depends on.
	if tr.Proxy == nil {
		t.Error("Proxy is nil: HTTPS_PROXY/NO_PROXY would be ignored")
	}
	if tr.TLSHandshakeTimeout == 0 || tr.IdleConnTimeout == 0 {
		t.Error("dial/handshake/idle timeouts are unset")
	}
	if c.hc.Timeout != defaultTimeout {
		t.Errorf("Timeout = %v, want %v", c.hc.Timeout, defaultTimeout)
	}
	if c.hc.CheckRedirect == nil {
		t.Error("no redirect policy")
	}
}

func TestVaultClient_TimeoutIsHonored(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-block
	}))
	defer func() { close(block); srv.Close() }()

	c, err := New(context.Background(), Config{Addr: srv.URL, Token: "t", Timeout: 150 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := c.ReadKVDataAt(context.Background(), "kv", "slow", 0); err == nil {
		t.Fatal("expected a timeout error")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("took %v to give up; the timeout is not being applied", elapsed)
	}
}

// The Vault endpoint carries the Vault token and, on --creds-from-vault, every
// OpenStack credential in the openrc. Disabling verification here was silent.
func TestNew_WarnsOnInsecureAndCleartext(t *testing.T) {
	out := captureStderr(t, func() {
		if _, err := New(context.Background(), Config{Addr: "https://vault.example.com", Token: "t", Insecure: true}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "TLS certificate verification is disabled") {
		t.Errorf("no --insecure-vault warning:\n%s", out)
	}

	out = captureStderr(t, func() {
		if _, err := New(context.Background(), Config{Addr: "http://vault.example.com", Token: "t"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "plain HTTP") {
		t.Errorf("no cleartext warning for an http:// Vault:\n%s", out)
	}

	out = captureStderr(t, func() {
		if _, err := New(context.Background(), Config{Addr: "https://vault.example.com", Token: "t"}); err != nil {
			t.Fatal(err)
		}
	})
	if out != "" {
		t.Errorf("a verified https Vault must be quiet, got %q", out)
	}
}

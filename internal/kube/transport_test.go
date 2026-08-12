package kube

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

// The apiserver client reads Secrets, so its bearer token must not follow a
// redirect off the cluster's own host.
func TestKubeClient_RedirectDropsBearerTokenCrossHost(t *testing.T) {
	var foreignAuth string
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		foreignAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer foreign.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, foreign.URL+"/api/v1/namespaces/ns/secrets/s", http.StatusFound)
	}))
	defer origin.Close()

	c, err := Load(Options{Kubeconfig: writeKubeconfig(t, origin.URL)})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = c.GetSecret(context.Background(), "ns", "s")

	if foreignAuth != "" {
		t.Errorf("a foreign host received the bearer token: %q", foreignAuth)
	}
}

func TestKubeClient_UsesDefaultTransportAndTimeout(t *testing.T) {
	c, err := Load(Options{Kubeconfig: writeKubeconfig(t, "https://apiserver.example.com:6443")})
	if err != nil {
		t.Fatal(err)
	}
	tr, ok := c.hc.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T, want *http.Transport", c.hc.Transport)
	}
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

	// koc's --timeout is passed through.
	c, err = Load(Options{Kubeconfig: writeKubeconfig(t, "https://apiserver.example.com:6443"), Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if c.hc.Timeout != 5*time.Second {
		t.Errorf("Timeout = %v, want the option's 5s", c.hc.Timeout)
	}
}

// A kubeconfig that opts out of verification was silent about it.
func TestLoad_WarnsOnInsecureCluster(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	doc := `apiVersion: v1
current-context: test
clusters:
- name: c
  cluster:
    server: https://apiserver.example.com:6443
    insecure-skip-tls-verify: true
users:
- name: u
  user:
    token: sekret-token
contexts:
- name: test
  context:
    cluster: c
    user: u
`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}

	out := captureStderr(t, func() {
		if _, err := Load(Options{Kubeconfig: path}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "TLS certificate verification is disabled") {
		t.Errorf("no insecure-skip-tls-verify warning:\n%s", out)
	}
}

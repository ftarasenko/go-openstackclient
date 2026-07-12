package kube

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// writeKubeconfig writes a token-auth kubeconfig pointing at server and returns
// its path. server is an http:// URL (httptest), so TLS config is not exercised
// but the full parse + request path is.
func writeKubeconfig(t *testing.T, server string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	cfg := fmt.Sprintf(`apiVersion: v1
kind: Config
current-context: test
clusters:
- name: c
  cluster:
    server: %s
users:
- name: u
  user:
    token: sekret-token
contexts:
- name: test
  context:
    cluster: c
    user: u
`, server)
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestGetSecret_DecodesData(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/api/v1/namespaces/lcm-ironic/secrets/api" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		u := base64.StdEncoding.EncodeToString([]byte("ironic"))
		p := base64.StdEncoding.EncodeToString([]byte("s3cr3t"))
		_, _ = fmt.Fprintf(w, `{"data":{"username":%q,"password":%q}}`, u, p)
	}))
	defer srv.Close()

	c, err := Load(Options{Kubeconfig: writeKubeconfig(t, srv.URL)})
	if err != nil {
		t.Fatal(err)
	}
	sec, err := c.GetSecret(context.Background(), "lcm-ironic", "api")
	if err != nil {
		t.Fatal(err)
	}
	if string(sec["username"]) != "ironic" || string(sec["password"]) != "s3cr3t" {
		t.Errorf("decoded secret = %q/%q", sec["username"], sec["password"])
	}
	if gotAuth != "Bearer sekret-token" {
		t.Errorf("Authorization = %q, want bearer token", gotAuth)
	}
}

func TestGetIronic_ResolvesSpec(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"items":[{"metadata":{"name":"ironic"},"spec":{
			"apiCredentialsName":"ironic-service-abc",
			"networking":{"ipAddress":"10.0.0.9","apiPort":6385},
			"tls":{"certificateName":"ironic-tls"}}}]}`)
	}))
	defer srv.Close()

	c, _ := Load(Options{Kubeconfig: writeKubeconfig(t, srv.URL)})
	api, err := c.GetIronic(context.Background(), "lcm-ironic")
	if err != nil {
		t.Fatal(err)
	}
	if api.APICredentialsName != "ironic-service-abc" || api.IPAddress != "10.0.0.9" ||
		api.APIPort != 6385 || api.TLSCertificateName != "ironic-tls" {
		t.Errorf("unexpected IronicAPI: %+v", api)
	}
}

func TestGetIronic_ZeroAndMultiple(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"zero", `{"items":[]}`},
		{"multiple", `{"items":[{"metadata":{"name":"a"},"spec":{}},{"metadata":{"name":"b"},"spec":{}}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			c, _ := Load(Options{Kubeconfig: writeKubeconfig(t, srv.URL)})
			if _, err := c.GetIronic(context.Background(), "lcm-ironic"); err == nil {
				t.Fatalf("expected error for %s ironic instances", tc.name)
			}
		})
	}
}

func TestGetSecret_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"message":"secrets \"api\" not found"}`)
	}))
	defer srv.Close()
	c, _ := Load(Options{Kubeconfig: writeKubeconfig(t, srv.URL)})
	_, err := c.GetSecret(context.Background(), "lcm-ironic", "api")
	if err == nil {
		t.Fatal("expected not-found error")
	}
}

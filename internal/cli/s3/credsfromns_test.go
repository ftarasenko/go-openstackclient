package s3cli

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/pflag"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/s3"
)

// writeKubeconfig points a kubeconfig at the mock apiserver. It mirrors the
// helper in internal/kube's own tests, which is unexported there.
func writeKubeconfig(t *testing.T, server string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
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

// mockAPIServer serves one Secret, so --s3-creds-from-ns is exercised over the
// real kube client rather than by calling credsFromSecret directly.
func mockAPIServer(t *testing.T, secret map[string]string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !strings.HasSuffix(r.URL.Path, "/secrets/rgw-keys") {
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `{"message":"not found"}`)
			return
		}
		enc := map[string]string{}
		for k, v := range secret {
			enc[k] = base64.StdEncoding.EncodeToString([]byte(v))
		}
		_, _ = fmt.Fprint(w, `{"data":`+jsonMap(enc)+`}`)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func jsonMap(m map[string]string) string {
	parts := make([]string, 0, len(m))
	for k, v := range m {
		parts = append(parts, fmt.Sprintf("%q:%q", k, v))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// newClusterFlags builds a connFlags with a real flag set, so Changed() — which
// decides whether the Secret may fill a value — behaves as it does in the
// command.
func newClusterFlags(t *testing.T, kubeconfig, credsRef string) (*connFlags, *auth.Options) {
	t.Helper()
	f := &connFlags{}
	f.addTo(pflag.NewFlagSet("test", pflag.ContinueOnError))
	f.credsFromNS = credsRef
	// The env-derived defaults must not leak a developer's own AWS_* into the
	// assertions.
	f.endpoint, f.accessKey, f.secretKey, f.region = "", "", "", ""
	return f, &auth.Options{Kubeconfig: kubeconfig}
}

// TestClusterCredsKeysOnly covers a Secret holding a bare key pair: the
// credentials apply and the endpoint stays the operator's job, reported in the
// terms they can act on.
func TestClusterCredsKeysOnly(t *testing.T) {
	url := mockAPIServer(t, map[string]string{
		"access_key": "GKrgw",
		"secret_key": "s3cr3t",
	})
	f, a := newClusterFlags(t, writeKubeconfig(t, url), "lcm-ceph/rgw-keys")

	cfg := s3.Config{}
	if err := f.applyClusterCreds(context.Background(), a, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.AccessKey != "GKrgw" || cfg.SecretKey != "s3cr3t" {
		t.Errorf("credentials = %q/%q", cfg.AccessKey, cfg.SecretKey)
	}
	if cfg.Endpoint != "" {
		t.Errorf("endpoint = %q, want it left unset", cfg.Endpoint)
	}

	_, err := s3.New(cfg)
	if err == nil {
		t.Fatal("expected s3.New to reject a config with no endpoint")
	}
	if !strings.Contains(err.Error(), "--s3-endpoint") {
		t.Errorf("err = %v, want it to point at --s3-endpoint", err)
	}
}

// TestClusterCredsFromConfigBlob is the GitLab shape: one value holding the
// whole connection, from which the endpoint and region come too.
func TestClusterCredsFromConfigBlob(t *testing.T) {
	url := mockAPIServer(t, map[string]string{
		"config": "aws_access_key_id: GKgitlab\n" +
			"aws_secret_access_key: s3cr3t\n" +
			"endpoint: https://s3.internal.example.com\n" +
			"region: garage\n",
	})
	f, a := newClusterFlags(t, writeKubeconfig(t, url), "lcm-gitlab/rgw-keys")

	cfg := s3.Config{}
	if err := f.applyClusterCreds(context.Background(), a, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Endpoint != "https://s3.internal.example.com" {
		t.Errorf("endpoint = %q", cfg.Endpoint)
	}
	if cfg.Region != "garage" {
		t.Errorf("region = %q", cfg.Region)
	}
}

// TestClusterCredsExplicitFlagWins pins the precedence rule: an explicit
// --s3-endpoint is never replaced by what the Secret says.
func TestClusterCredsExplicitFlagWins(t *testing.T) {
	url := mockAPIServer(t, map[string]string{
		"config": "access_key: GKrgw\nsecret_key: s3cr3t\nendpoint: https://from-secret.example.com\n",
	})
	f, a := newClusterFlags(t, writeKubeconfig(t, url), "lcm-ceph/rgw-keys")
	if err := f.fs.Set("s3-endpoint", "https://chosen.example.com"); err != nil {
		t.Fatal(err)
	}
	f.endpoint = "https://chosen.example.com"

	cfg := s3.Config{Endpoint: f.endpoint}
	if err := f.applyClusterCreds(context.Background(), a, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Endpoint != "https://chosen.example.com" {
		t.Errorf("endpoint = %q, want the explicit flag to win", cfg.Endpoint)
	}
}

// TestClusterCredsMissingSecret checks the error names what was looked up, since
// a wrong namespace is the likely mistake.
func TestClusterCredsMissingSecret(t *testing.T) {
	url := mockAPIServer(t, nil)
	f, a := newClusterFlags(t, writeKubeconfig(t, url), "lcm-gitlab")

	cfg := s3.Config{}
	err := f.applyClusterCreds(context.Background(), a, &cfg)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "lcm-gitlab/"+defaultCredsSecret) {
		t.Errorf("err = %v, want it to name the namespace/secret it tried", err)
	}
}

package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- internal/cli/*: service-factory error paths (services.go) ---
//
// In --creds-from-ns mode a Client carries a standalone ironic session and no
// Keystone provider, so every service factory except Baremetal must fail
// before touching the network.

func TestClient_RequireKeystoneErrorsInIronicMode(t *testing.T) {
	c := &Client{ironic: &ironicCreds{}, opts: &Options{}}

	cases := []struct {
		name string
		call func() error
	}{
		{"Introspection", func() error { _, err := c.Introspection(); return err }},
		{"Compute", func() error { _, err := c.Compute(); return err }},
		{"Identity", func() error { _, err := c.Identity(); return err }},
		{"Volume", func() error { _, err := c.Volume(); return err }},
		{"DNS", func() error { _, err := c.DNS(); return err }},
		{"Image", func() error { _, err := c.Image(); return err }},
		{"Network", func() error { _, err := c.Network(); return err }},
		{"Placement", func() error { _, err := c.Placement(); return err }},
		{"LoadBalancer", func() error { _, err := c.LoadBalancer(); return err }},
		{"KeyVRM", func() error { _, err := c.KeyVRM(); return err }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if err == nil {
				t.Fatalf("%s: expected an error in --creds-from-ns mode, got nil", tc.name)
			}
			if !strings.Contains(err.Error(), "creds-from-ns provides baremetal (ironic) credentials only") {
				t.Errorf("%s: unexpected error %q", tc.name, err.Error())
			}
		})
	}
}

func TestWrapService(t *testing.T) {
	inner := errors.New("dial tcp: connection refused")
	err := wrapService("network", inner)

	var se *ServiceError
	if !errors.As(err, &se) {
		t.Fatalf("wrapService did not return a *ServiceError: %T", err)
	}
	if se.Service != "network" {
		t.Errorf("Service = %q, want %q", se.Service, "network")
	}
	if got, want := se.Error(), "creating network client: dial tcp: connection refused"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(err, inner) {
		t.Error("errors.Is(err, inner) = false, want true (Unwrap must expose the inner error)")
	}
}

func TestNewServiceClient_PropagatesAuthenticateError(t *testing.T) {
	resetBadEnv()
	t.Cleanup(resetBadEnv)

	// Zero-value Options: no --os-cloud, no OS_AUTH_URL, so Authenticate fails
	// before any network access.
	o := &Options{}
	sc, err := o.NewServiceClient(context.Background(), (*Client).Compute)
	if err == nil {
		t.Fatal("expected an error when no credentials are configured")
	}
	if sc != nil {
		t.Error("service client should be nil on error")
	}
	if !strings.Contains(err.Error(), "no credentials found") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestNewServiceSession_PropagatesAuthenticateError(t *testing.T) {
	resetBadEnv()
	t.Cleanup(resetBadEnv)

	o := &Options{}
	sc, client, err := o.NewServiceSession(context.Background(), (*Client).Compute)
	if err == nil {
		t.Fatal("expected an error when no credentials are configured")
	}
	if sc != nil || client != nil {
		t.Error("service client and Client should both be nil on error")
	}
}

// --- provider.go: resolveAuth / Authenticate error paths ---

func TestResolveAuth_EnvMissingCredentials(t *testing.T) {
	o := &Options{AuthURL: "https://example.com:5000/v3"}
	_, _, _, err := o.resolveAuth()
	if err == nil {
		t.Fatal("expected an error when neither a password nor application credentials are set")
	}
	if !strings.Contains(err.Error(), "OS_PASSWORD or application credentials") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestResolveAuth_CloudsFileNotFound(t *testing.T) {
	t.Setenv("OS_CLIENT_CONFIG_FILE", filepath.Join(t.TempDir(), "does-not-exist.yaml"))

	o := &Options{Cloud: "example"}
	_, _, _, err := o.resolveAuth()
	if err == nil {
		t.Fatal("expected an error when clouds.yaml cannot be found")
	}
	if !strings.Contains(err.Error(), `loading cloud "example" from clouds.yaml`) {
		t.Errorf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "clouds file not found") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestResolveAuth_CloudNotFoundInFile(t *testing.T) {
	dir := t.TempDir()
	cloudsPath := filepath.Join(dir, "clouds.yaml")
	const yaml = `
clouds:
  other:
    auth:
      auth_url: https://example.com:5000/v3
      username: demo
      password: demo
      project_name: demo
`
	if err := os.WriteFile(cloudsPath, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OS_CLIENT_CONFIG_FILE", cloudsPath)

	o := &Options{Cloud: "missing"}
	_, _, _, err := o.resolveAuth()
	if err == nil {
		t.Fatal("expected an error for a cloud name absent from clouds.yaml")
	}
	if !strings.Contains(err.Error(), `cloud "missing" not found in clouds.yaml`) {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestAuthenticate_CredsFromNSAndVaultMutuallyExclusive(t *testing.T) {
	resetBadEnv()
	t.Cleanup(resetBadEnv)

	o := &Options{CredsFromNS: "ironic-ns", CredsFromVault: "path/to/openrc"}
	_, err := o.Authenticate(context.Background())
	if err == nil {
		t.Fatal("expected an error when both credential sources are set")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("unexpected error: %v", err)
	}
}

// --- tls.go: resolveTLSConfig error paths not covered by tls_test.go ---

func TestResolveTLSConfig_MissingCABundleFile(t *testing.T) {
	o := &Options{CACert: filepath.Join(t.TempDir(), "missing-ca.pem")}
	if _, _, err := o.resolveTLSConfig(nil); err == nil {
		t.Error("expected an error reading a nonexistent CA bundle")
	}
}

func TestResolveTLSConfig_CABundleNoCertificates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(path, []byte("not a certificate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	o := &Options{CACert: path}
	_, _, err := o.resolveTLSConfig(nil)
	if err == nil {
		t.Fatal("expected an error for a CA bundle with no parseable certificates")
	}
	if !strings.Contains(err.Error(), "no certificates parsed") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestResolveTLSConfig_ClientKeyRequiresCert(t *testing.T) {
	o := &Options{ClientKey: "/some/key.pem"}
	_, _, err := o.resolveTLSConfig(nil)
	if err == nil {
		t.Fatal("expected an error when --os-key is set without --os-cert")
	}
	if !strings.Contains(err.Error(), "--os-key is set but --os-cert is missing") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestResolveTLSConfig_MutualTLSMismatchedPair(t *testing.T) {
	certPath, _ := writeSelfSigned(t, t.TempDir())
	_, keyPath := writeSelfSigned(t, t.TempDir()) // a *different* key

	o := &Options{ClientCert: certPath, ClientKey: keyPath}
	_, _, err := o.resolveTLSConfig(nil)
	if err == nil {
		t.Fatal("expected an error for a cert/key pair that do not match")
	}
	if !strings.Contains(err.Error(), "loading client certificate/key") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestResolveTLSConfig_InMemoryClientCertInvalid(t *testing.T) {
	o := &Options{ClientCertPEM: []byte("garbage"), ClientKeyPEM: []byte("garbage")}
	_, _, err := o.resolveTLSConfig(nil)
	if err == nil {
		t.Fatal("expected an error for invalid in-memory client cert/key material")
	}
	if !strings.Contains(err.Error(), "loading in-memory client certificate/key") {
		t.Errorf("unexpected error: %v", err)
	}
}

// --- credsfrom.go: firstCertDNSName (pure parsing, no I/O) ---

// certPEMWithDNSNames builds an in-memory self-signed certificate PEM block
// with the given DNS SANs, for exercising firstCertDNSName without touching
// the filesystem.
func certPEMWithDNSNames(t *testing.T, dnsNames ...string) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "koc-dns-test"},
		DNSNames:     dnsNames,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestFirstCertDNSName(t *testing.T) {
	certNoDNS := certPEMWithDNSNames(t)
	certWithDNS := certPEMWithDNSNames(t, "ironic.example.com", "ironic-alt.example.com")

	cases := []struct {
		name string
		pem  []byte
		want string
	}{
		{"empty input", nil, ""},
		{"garbage bytes", []byte("not a pem block at all"), ""},
		{"non-certificate PEM block", []byte("-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n"), ""},
		{"certificate without DNS SANs", certNoDNS, ""},
		{"certificate with DNS SANs", certWithDNS, "ironic.example.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := firstCertDNSName(tc.pem); got != tc.want {
				t.Errorf("firstCertDNSName() = %q, want %q", got, tc.want)
			}
		})
	}
}

// --- credsfrom.go: small pure helpers not yet covered ---

func TestKvKeys(t *testing.T) {
	got := kvKeys(map[string]any{"zeta": 1, "alpha": 2, "mid": 3})
	want := []string{"alpha", "mid", "zeta"}
	if len(got) != len(want) {
		t.Fatalf("kvKeys() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("kvKeys()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if got := kvKeys(nil); len(got) != 0 {
		t.Errorf("kvKeys(nil) = %v, want empty", got)
	}
}

func TestFileExists(t *testing.T) {
	if fileExists("") {
		t.Error(`fileExists("") should be false`)
	}
	if fileExists(filepath.Join(t.TempDir(), "nope")) {
		t.Error("fileExists() on a nonexistent path should be false")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "present")
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !fileExists(p) {
		t.Error("fileExists() on an existing file should be true")
	}
}

// --- credsfrom.go: VaultConfig / discoverVaultFromCluster error paths ---

func TestVaultConfig_MissingCACertFile(t *testing.T) {
	resetBadEnv()
	t.Cleanup(resetBadEnv)

	o := &Options{
		VaultAddr:   "https://vault.example.com:8200",
		VaultToken:  "s.faketoken",
		VaultCACert: filepath.Join(t.TempDir(), "missing-ca.pem"),
	}
	_, err := o.VaultConfig(context.Background())
	if err == nil {
		t.Fatal("expected an error reading a nonexistent --vault-cacert")
	}
	if !strings.Contains(err.Error(), "reading --vault-cacert") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestVaultConfig_DiscoveryFailsWithoutAddr(t *testing.T) {
	resetBadEnv()
	t.Cleanup(resetBadEnv)
	t.Setenv("VAULT_TOKEN_FILE", filepath.Join(t.TempDir(), "no-such-token"))

	o := &Options{Kubeconfig: filepath.Join(t.TempDir(), "no-such-kubeconfig")}
	_, err := o.VaultConfig(context.Background())
	if err == nil {
		t.Fatal("expected an error when Vault is unconfigured and discovery cannot reach a cluster")
	}
	if !strings.Contains(err.Error(), "vault not configured and cluster auto-discovery failed") {
		t.Errorf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "--vault-addr") {
		t.Errorf("error should point the operator at --vault-addr: %v", err)
	}
}

func TestVaultConfig_RejectsUnusableEnvBool(t *testing.T) {
	resetBadEnv()
	t.Cleanup(resetBadEnv)
	t.Setenv("KOC_TEST_UNUSABLE_BOOL", "maybe")
	captureStderr(t, func() { EnvBool("KOC_TEST_UNUSABLE_BOOL") })

	o := &Options{VaultAddr: "https://vault.example.com:8200", VaultToken: "s.faketoken"}
	_, err := o.VaultConfig(context.Background())
	if err == nil {
		t.Fatal("expected VaultConfig to surface a previously recorded unusable environment variable")
	}
	if !strings.Contains(err.Error(), "unusable environment variable") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestDiscoverVaultFromCluster_BadKubeconfigFailsFast(t *testing.T) {
	o := &Options{Kubeconfig: filepath.Join(t.TempDir(), "no-such-kubeconfig")}
	if err := o.discoverVaultFromCluster(context.Background()); err == nil {
		t.Fatal("expected an error for an unreadable kubeconfig")
	}
}

// --- credsfrom.go: applyVaultOpenrc / VaultClient propagate VaultConfig errors ---

func TestApplyVaultOpenrc_PropagatesVaultConfigError(t *testing.T) {
	resetBadEnv()
	t.Cleanup(resetBadEnv)
	t.Setenv("VAULT_TOKEN_FILE", filepath.Join(t.TempDir(), "no-such-token"))

	o := &Options{
		Kubeconfig:     filepath.Join(t.TempDir(), "no-such-kubeconfig"),
		CredsFromVault: "openstack/openrc",
	}
	err := o.applyVaultOpenrc(context.Background())
	if err == nil {
		t.Fatal("expected an error when the underlying VaultConfig cannot be resolved")
	}
	if !strings.Contains(err.Error(), "vault not configured") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestVaultClient_PropagatesVaultConfigError(t *testing.T) {
	resetBadEnv()
	t.Cleanup(resetBadEnv)
	t.Setenv("VAULT_TOKEN_FILE", filepath.Join(t.TempDir(), "no-such-token"))

	o := &Options{Kubeconfig: filepath.Join(t.TempDir(), "no-such-kubeconfig")}
	client, err := o.VaultClient(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if client != nil {
		t.Error("client should be nil on error")
	}
}

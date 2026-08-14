package auth

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2/openstack/baremetal/v1/nodes"
	"github.com/spf13/pflag"
)

func TestParseOpenrcAndOpenrcFromKV(t *testing.T) {
	script := `# Ansible managed
export OS_AUTH_URL=https://keystone:5000/v3
export OS_USERNAME="admin"
export OS_PASSWORD='p@ss'
OS_REGION_NAME=RegionOne
NOT_OS_VAR=ignored
`
	kv := parseOpenrc(script)
	if kv["OS_AUTH_URL"] != "https://keystone:5000/v3" {
		t.Errorf("OS_AUTH_URL = %q", kv["OS_AUTH_URL"])
	}
	if kv["OS_USERNAME"] != "admin" || kv["OS_PASSWORD"] != "p@ss" {
		t.Errorf("quoted values not stripped: %q / %q", kv["OS_USERNAME"], kv["OS_PASSWORD"])
	}
	if kv["OS_REGION_NAME"] != "RegionOne" {
		t.Errorf("OS_REGION_NAME = %q", kv["OS_REGION_NAME"])
	}
	if _, ok := kv["NOT_OS_VAR"]; ok {
		t.Error("non-OS_ variable should be ignored")
	}

	// openrcFromKV: value field preferred.
	if got, _ := openrcFromKV(map[string]any{"value": script}); got != script {
		t.Error("openrcFromKV should return the value field verbatim")
	}
	// base64 openrc field.
	enc := base64.StdEncoding.EncodeToString([]byte("export OS_PASSWORD=y\n"))
	got, err := openrcFromKV(map[string]any{"openrc": enc})
	if err != nil || got != "export OS_PASSWORD=y\n" {
		t.Errorf("openrcFromKV base64 = %q, %v", got, err)
	}
	// flat OS_ keys.
	got, err = openrcFromKV(map[string]any{"OS_USERNAME": "z"})
	if err != nil || parseOpenrc(got)["OS_USERNAME"] != "z" {
		t.Errorf("openrcFromKV flat = %q, %v", got, err)
	}
	// nothing usable.
	if _, err := openrcFromKV(map[string]any{"foo": "bar"}); err == nil {
		t.Error("expected error when no openrc data present")
	}
}

func TestApplyOpenrcVars_FlagPrecedenceAndNormalize(t *testing.T) {
	o := &Options{}
	fs := pflag.NewFlagSet("t", pflag.ContinueOnError)
	o.AddFlags(fs)
	if err := fs.Parse([]string{"--os-project-name", "explicit"}); err != nil {
		t.Fatal(err)
	}

	o.applyOpenrcVars(map[string]string{
		"OS_AUTH_URL":      "https://keystone:5000/v3",
		"OS_USERNAME":      "u",
		"OS_PASSWORD":      "p",
		"OS_PROJECT_NAME":  "vaultproj",
		"OS_ENDPOINT_TYPE": "publicURL",
	})

	if o.AuthURL != "https://keystone:5000/v3" || o.Username != "u" || o.Password != "p" {
		t.Errorf("vault values not applied: %+v", o)
	}
	if o.ProjectName != "explicit" {
		t.Errorf("explicit --os-project-name should win, got %q", o.ProjectName)
	}
	if o.Interface != "public" {
		t.Errorf("OS_ENDPOINT_TYPE publicURL should normalize to public, got %q", o.Interface)
	}
}

func TestSiblingVaultPathAndCertNames(t *testing.T) {
	const openrc = "deployments/example/dev/reg/reg-cp/openrc"
	if got := siblingVaultPath(openrc, "ssl_certificates"); got != "deployments/example/dev/reg/reg-cp/ssl_certificates" {
		t.Errorf("siblingVaultPath = %q", got)
	}
	if got := siblingVaultPath("openrc", "ssl_certificates"); got != "ssl_certificates" {
		t.Errorf("siblingVaultPath (no dir) = %q", got)
	}

	certName, keyName := certKeyKVNames("/etc/kolla/certificates/backend-cert.pem", "/etc/kolla/certificates/backend-key.pem")
	if certName != "backend_pem" || keyName != "backend_key_pem" {
		t.Errorf("certKeyKVNames = %q/%q, want backend_pem/backend_key_pem", certName, keyName)
	}
	// Derive from the key alone.
	certName, keyName = certKeyKVNames("", "/x/haproxy-key.pem")
	if certName != "haproxy_pem" || keyName != "haproxy_key_pem" {
		t.Errorf("certKeyKVNames(key only) = %q/%q", certName, keyName)
	}
}

func TestPemFromKV(t *testing.T) {
	data := map[string]any{"backend_pem": "CERTDATA", "other": 42}
	if got := string(pemFromKV(data, "missing", "backend_pem")); got != "CERTDATA" {
		t.Errorf("pemFromKV = %q", got)
	}
	if pemFromKV(data, "nope") != nil {
		t.Error("pemFromKV should return nil when absent")
	}
}

func TestFillVaultOption_Precedence(t *testing.T) {
	t.Setenv("VAULT_ADDR", "")
	t.Setenv("VAULT_ROLE_ID", "")

	o := &Options{}
	fs := pflag.NewFlagSet("t", pflag.ContinueOnError)
	o.AddFlags(fs)
	// Explicitly set role-id on the command line; leave addr unset.
	if err := fs.Parse([]string{"--vault-role-id", "cli-role"}); err != nil {
		t.Fatal(err)
	}

	o.fillVaultOption("vault-addr", &o.VaultAddr, "https://vault.example", "VAULT_ADDR")
	o.fillVaultOption("vault-role-id", &o.VaultRoleID, "cluster-role", "VAULT_ROLE_ID")

	if o.VaultAddr != "https://vault.example" {
		t.Errorf("addr should come from discovery, got %q", o.VaultAddr)
	}
	if o.VaultRoleID != "cli-role" {
		t.Errorf("explicit --vault-role-id should win over discovery, got %q", o.VaultRoleID)
	}
}

// A fallback env name (VAULT_PREFIX for --vault-kv-prefix) must count as
// explicit, so cluster discovery does not clobber it.
func TestFillVaultOption_FallbackEnvBlocksDiscovery(t *testing.T) {
	t.Setenv("VAULT_KV_PREFIX", "")
	t.Setenv("VAULT_PREFIX", "deployments/example")

	o := &Options{}
	fs := pflag.NewFlagSet("t", pflag.ContinueOnError)
	o.AddFlags(fs)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}

	o.fillVaultOption("vault-kv-prefix", &o.VaultKVPrefix, "cluster/prefix", "VAULT_KV_PREFIX", "VAULT_PREFIX")
	if o.VaultKVPrefix != "deployments/example" {
		t.Errorf("VAULT_PREFIX should win over discovery, got %q", o.VaultKVPrefix)
	}
}

// TestInsecureVaultFlag verifies --insecure-vault and its --vault-insecure
// back-compat alias both set VaultInsecure, which the Vault client turns into
// InsecureSkipVerify.
func TestInsecureVaultFlag(t *testing.T) {
	t.Setenv("VAULT_SKIP_VERIFY", "")
	for _, arg := range []string{"--insecure-vault", "--vault-insecure"} {
		o := &Options{}
		fs := pflag.NewFlagSet("t", pflag.ContinueOnError)
		o.AddFlags(fs)
		if err := fs.Parse([]string{arg}); err != nil {
			t.Fatalf("parse %s: %v", arg, err)
		}
		if !o.VaultInsecure {
			t.Errorf("%s did not set VaultInsecure", arg)
		}
	}
	// Default (no flag) stays secure.
	o := &Options{}
	fs := pflag.NewFlagSet("t", pflag.ContinueOnError)
	o.AddFlags(fs)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	if o.VaultInsecure {
		t.Error("VaultInsecure should default to false")
	}
}

func TestReadVaultTokenFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("VAULT_TOKEN_FILE", "")
	t.Setenv("HOME", dir)
	if got := readVaultTokenFile(); got != "" {
		t.Errorf("no token file should yield empty, got %q", got)
	}
	if err := os.WriteFile(filepath.Join(dir, ".vault-token"), []byte("hvs.abc123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readVaultTokenFile(); got != "hvs.abc123" {
		t.Errorf("readVaultTokenFile = %q, want hvs.abc123 (trimmed)", got)
	}

	// VAULT_TOKEN_FILE override wins.
	other := filepath.Join(dir, "other-token")
	if err := os.WriteFile(other, []byte("hvs.override"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VAULT_TOKEN_FILE", other)
	if got := readVaultTokenFile(); got != "hvs.override" {
		t.Errorf("VAULT_TOKEN_FILE override = %q", got)
	}
}

func TestVaultNeedsDiscovery(t *testing.T) {
	cases := []struct {
		o    Options
		want bool
	}{
		{Options{}, true},                                 // nothing set
		{Options{VaultAddr: "a"}, true},                   // addr but no auth
		{Options{VaultAddr: "a", VaultToken: "t"}, false}, // addr + token
		{Options{VaultAddr: "a", VaultRoleID: "r"}, true}, // missing secret-id
		{Options{VaultAddr: "a", VaultRoleID: "r", VaultSecretID: "s"}, false},
	}
	for i, c := range cases {
		if got := c.o.vaultNeedsDiscovery(); got != c.want {
			t.Errorf("case %d: vaultNeedsDiscovery = %v, want %v", i, got, c.want)
		}
	}
}

// fakeAPIServer serves the two objects --creds-from-ns reads: the Ironic CR and
// its API credentials secret. tlsCertName empty means the CR has no certificate,
// which is what makes koc fall back to plain http.
func fakeAPIServer(t *testing.T, tlsCertName string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/apis/ironic.metal3.io/v1alpha1/namespaces/metal3/ironics":
			_, _ = fmt.Fprintf(w, `{"items":[{"metadata":{"name":"ironic"},"spec":{"apiCredentialsName":"ironic-htpasswd","networking":{"ipAddress":"192.0.2.10"},"tls":{"certificateName":%q}}}]}`, tlsCertName)
		case "/api/v1/namespaces/metal3/secrets/ironic-htpasswd":
			_, _ = fmt.Fprintf(w, `{"data":{"username":%q,"password":%q}}`,
				base64.StdEncoding.EncodeToString([]byte("ironic")),
				base64.StdEncoding.EncodeToString([]byte("pw")))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"not found"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// writeKubeconfig writes a minimal kubeconfig pointing at server.
func writeKubeconfig(t *testing.T, server string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kubeconfig")
	doc := fmt.Sprintf(`apiVersion: v1
current-context: test
clusters:
- name: test
  cluster:
    server: %s
users:
- name: test
  user:
    token: fake-bearer-token
contexts:
- name: test
  context:
    cluster: test
    user: test
`, server)
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The --creds-from-ns branch returns before the Keystone branch's warning, so it
// has to emit its own: it honors --insecure, and an Ironic CR without a TLS
// certificate puts the basic-auth credentials on plain http.
func TestLoadIronicCreds_WarnsInsecureAndCleartext(t *testing.T) {
	api := fakeAPIServer(t, "")
	o := &Options{
		CredsFromNS: "metal3",
		Kubeconfig:  writeKubeconfig(t, api.URL),
		Insecure:    true,
	}

	var ic *ironicCreds
	var err error
	out := captureStderr(t, func() { ic, err = o.loadIronicCreds(context.Background()) })
	if err != nil {
		t.Fatalf("loadIronicCreds: %v", err)
	}
	if ic.endpoint != "http://192.0.2.10:6385/" {
		t.Fatalf("endpoint = %q", ic.endpoint)
	}
	if !strings.Contains(out, "TLS certificate verification is disabled") {
		t.Errorf("no --insecure warning:\n%s", out)
	}
	if !strings.Contains(out, "plain HTTP") {
		t.Errorf("no cleartext warning for an http:// ironic endpoint:\n%s", out)
	}
}

// basicAuthTransport injects on every request the client makes, redirects
// included, which used to re-add the credentials net/http had just stripped for a
// cross-host hop.
func TestBasicAuthTransport_NoCredentialsOnAnotherHost(t *testing.T) {
	var foreignAuth string
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		foreignAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"nodes":[]}`))
	}))
	defer foreign.Close()

	var originAuth string
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originAuth = r.Header.Get("Authorization")
		http.Redirect(w, r, foreign.URL+"/v1/nodes", http.StatusFound)
	}))
	defer origin.Close()

	ic := &ironicCreds{endpoint: origin.URL + "/", username: "ironic", password: "pw"}
	sc, err := ic.baremetalClient("1.82")
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, origin.URL+"/v1/nodes", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := sc.HTTPClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	if originAuth == "" {
		t.Error("the ironic endpoint itself must still receive basic auth")
	}
	if foreignAuth != "" {
		t.Errorf("a foreign host received the ironic credentials: %q", foreignAuth)
	}
}

func TestBaremetalClient_BasicAuthAndIronicMicroversion(t *testing.T) {
	var gotAuth, gotVer, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotVer = r.Header.Get("X-OpenStack-Ironic-API-Version")
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"nodes":[]}`))
	}))
	defer srv.Close()

	ic := &ironicCreds{endpoint: srv.URL + "/", username: "ironic", password: "pw"}
	sc, err := ic.baremetalClient("1.82")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nodes.List(sc, nil).AllPages(context.Background()); err != nil {
		t.Fatalf("nodes.List: %v", err)
	}

	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("ironic:pw"))
	if gotAuth != wantAuth {
		t.Errorf("Authorization = %q, want %q", gotAuth, wantAuth)
	}
	if gotVer != "1.82" {
		t.Errorf("X-OpenStack-Ironic-API-Version = %q, want 1.82", gotVer)
	}
	if gotPath != "/v1/nodes" {
		t.Errorf("request path = %q, want /v1/nodes", gotPath)
	}
}

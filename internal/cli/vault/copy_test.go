package vaultcli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/pflag"

	"github.com/ftarasenko/go-openstackclient/internal/output"
	"github.com/ftarasenko/go-openstackclient/internal/vault"
)

// clearSrcEnv unsets the VAULT_SRC_* variables so a developer's shell (or a CI
// job that exports them) cannot make the inheritance assertions read as
// "explicitly set".
func clearSrcEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"VAULT_SRC_ADDR", "VAULT_SRC_NAMESPACE", "VAULT_SRC_TOKEN",
		"VAULT_SRC_ROLE_ID", "VAULT_SRC_SECRET_ID", "VAULT_SRC_ENGINE",
		"VAULT_SRC_PREFIX", "VAULT_SRC_CACERT", "VAULT_SRC_SKIP_VERIFY",
	} {
		t.Setenv(k, "")
	}
}

// srcVaultFixture serves a two-secret subtree under "src/dev", one of them in a
// nested folder, and records every request it saw.
func srcVaultFixture(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		if r.Header.Get("X-Vault-Token") != "src-token" {
			t.Errorf("source token = %q, want src-token", r.Header.Get("X-Vault-Token"))
		}
		if r.Header.Get("X-Vault-Namespace") != "src-ns" {
			t.Errorf("source namespace = %q, want src-ns", r.Header.Get("X-Vault-Namespace"))
		}
		switch r.URL.Path {
		case "/v1/src_kv/metadata/src/dev":
			_, _ = w.Write([]byte(`{"data":{"keys":["openrc","nested/"]}}`))
		case "/v1/src_kv/metadata/src/dev/nested":
			_, _ = w.Write([]byte(`{"data":{"keys":["accounts"]}}`))
		case "/v1/src_kv/data/src/dev/openrc":
			_, _ = w.Write([]byte(`{"data":{"data":{"value":"export OS_USERNAME=admin\n"}}}`))
		case "/v1/src_kv/data/src/dev/nested/accounts":
			_, _ = w.Write([]byte(`{"data":{"data":{"a":"1","b":"2"}}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errors":[]}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

// dstVaultFixture accepts writes and records them. existing lists metadata paths
// that should report an already-present secret (for --skip-existing).
func dstVaultFixture(t *testing.T, existing ...string) (*httptest.Server, map[string]map[string]any) {
	t.Helper()
	writes := map[string]map[string]any{}
	has := map[string]bool{}
	for _, e := range existing {
		has[e] = true
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Vault-Token") != "dst-token" {
			t.Errorf("destination token = %q, want dst-token", r.Header.Get("X-Vault-Token"))
		}
		switch {
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/dst_kv/data/"):
			var body struct {
				Data map[string]any `json:"data"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			writes[strings.TrimPrefix(r.URL.Path, "/v1/dst_kv/data/")] = body.Data
			_, _ = w.Write([]byte(`{"data":{"version":1}}`))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/dst_kv/metadata/"):
			if has[strings.TrimPrefix(r.URL.Path, "/v1/dst_kv/metadata/")] {
				_, _ = w.Write([]byte(`{"data":{"current_version":1}}`))
				return
			}
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errors":[]}`))
		default:
			t.Errorf("unexpected destination request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, writes
}

func clients(t *testing.T, srcURL, dstURL string) (src, dst *vault.Client) {
	t.Helper()
	ctx := context.Background()
	var err error
	if src, err = vault.New(ctx, vault.Config{Addr: srcURL, Token: "src-token", Namespace: "src-ns", KVMount: "src_kv"}); err != nil {
		t.Fatal(err)
	}
	if dst, err = vault.New(ctx, vault.Config{Addr: dstURL, Token: "dst-token", KVMount: "dst_kv"}); err != nil {
		t.Fatal(err)
	}
	return src, dst
}

func copyOpts(recursive bool) copyOptions {
	return copyOptions{
		copyFlags:  copyFlags{recursive: recursive},
		srcPath:    "src/dev",
		dstPath:    "dst/e2e",
		srcDisplay: "src/dev",
	}
}

func TestRunKVCopy_RecursiveAcrossVaults(t *testing.T) {
	srcSrv, srcSeen := srcVaultFixture(t)
	dstSrv, writes := dstVaultFixture(t)
	src, dst := clients(t, srcSrv.URL, dstSrv.URL)

	var buf bytes.Buffer
	o := &output.Options{Format: output.FormatValue}
	if err := runKVCopy(context.Background(), src, dst, o, copyOpts(true), &buf); err != nil {
		t.Fatal(err)
	}

	// The nested folder is descended into, not read as a secret.
	if len(writes) != 2 {
		t.Fatalf("wrote %d secrets, want 2: %v", len(writes), writes)
	}
	if got := writes["dst/e2e/openrc"]["value"]; got != "export OS_USERNAME=admin\n" {
		t.Errorf("dst/e2e/openrc value = %v", got)
	}
	if got := writes["dst/e2e/nested/accounts"]["b"]; got != "2" {
		t.Errorf("dst/e2e/nested/accounts = %v", writes["dst/e2e/nested/accounts"])
	}

	for _, req := range *srcSeen {
		if !strings.HasPrefix(req, "GET ") {
			t.Errorf("source vault received a non-GET request: %s", req)
		}
	}

	out := buf.String()
	for _, want := range []string{"src/dev/openrc", "dst/e2e/openrc", "src/dev/nested/accounts", statusCopied} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	// Secret values must never be rendered.
	if strings.Contains(out, "OS_USERNAME") {
		t.Errorf("output leaked secret data:\n%s", out)
	}
}

func TestRunKVCopy_DryRunWritesNothing(t *testing.T) {
	srcSrv, _ := srcVaultFixture(t)
	dstSrv, writes := dstVaultFixture(t)
	src, dst := clients(t, srcSrv.URL, dstSrv.URL)

	opts := copyOpts(true)
	opts.dryRun = true
	var buf bytes.Buffer
	if err := runKVCopy(context.Background(), src, dst, &output.Options{Format: output.FormatValue}, opts, &buf); err != nil {
		t.Fatal(err)
	}
	if len(writes) != 0 {
		t.Errorf("--dry-run wrote %v", writes)
	}
	if !strings.Contains(buf.String(), statusWould) {
		t.Errorf("output = %q, want the %q status", buf.String(), statusWould)
	}
}

func TestRunKVCopy_SkipExisting(t *testing.T) {
	srcSrv, _ := srcVaultFixture(t)
	dstSrv, writes := dstVaultFixture(t, "dst/e2e/openrc")
	src, dst := clients(t, srcSrv.URL, dstSrv.URL)

	opts := copyOpts(true)
	opts.skipExisting = true
	var buf bytes.Buffer
	if err := runKVCopy(context.Background(), src, dst, &output.Options{Format: output.FormatValue}, opts, &buf); err != nil {
		t.Fatal(err)
	}
	if _, ok := writes["dst/e2e/openrc"]; ok {
		t.Error("--skip-existing overwrote an existing secret")
	}
	if _, ok := writes["dst/e2e/nested/accounts"]; !ok {
		t.Errorf("--skip-existing should still copy new secrets, wrote %v", writes)
	}
	if !strings.Contains(buf.String(), statusSkipped) {
		t.Errorf("output = %q, want a %q row", buf.String(), statusSkipped)
	}
}

func TestRunKVCopy_FolderWithoutRecursive(t *testing.T) {
	srcSrv, _ := srcVaultFixture(t)
	dstSrv, writes := dstVaultFixture(t)
	src, dst := clients(t, srcSrv.URL, dstSrv.URL)

	err := runKVCopy(context.Background(), src, dst, &output.Options{Format: output.FormatValue}, copyOpts(false), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "-r") {
		t.Fatalf("err = %v, want a folder error mentioning -r", err)
	}
	if len(writes) != 0 {
		t.Errorf("nothing should have been written, got %v", writes)
	}
}

func TestRunKVCopy_SingleSecret(t *testing.T) {
	srcSrv, _ := srcVaultFixture(t)
	dstSrv, writes := dstVaultFixture(t)
	src, dst := clients(t, srcSrv.URL, dstSrv.URL)

	opts := copyOpts(false)
	opts.srcPath, opts.dstPath = "src/dev/openrc", "dst/e2e/openrc"
	if err := runKVCopy(context.Background(), src, dst, &output.Options{Format: output.FormatValue}, opts, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if len(writes) != 1 || writes["dst/e2e/openrc"] == nil {
		t.Errorf("writes = %v, want only dst/e2e/openrc", writes)
	}
}

func TestRunKVCopy_SelfCopyGuard(t *testing.T) {
	srv, _ := srcVaultFixture(t)
	ctx := context.Background()
	same := func() *vault.Client {
		c, err := vault.New(ctx, vault.Config{Addr: srv.URL, Token: "src-token", Namespace: "src-ns", KVMount: "src_kv"})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	a, b := same(), same()

	opts := copyOpts(false)
	opts.dstPath = opts.srcPath
	if err := runKVCopy(ctx, a, b, &output.Options{}, opts, &bytes.Buffer{}); err == nil {
		t.Error("copying a secret onto itself should fail")
	}

	nested := copyOpts(true)
	nested.dstPath = "src/dev/inside"
	if err := runKVCopy(ctx, a, b, &output.Options{}, nested, &bytes.Buffer{}); err == nil {
		t.Error("copying a subtree into itself should fail")
	}
}

// A copy joins keys the SOURCE Vault reported onto the destination path, so a
// hostile or spoofed source answering with "../../../prod/openrc" could steer the
// WRITE outside the subtree the operator named — guardSelfCopy compares only the
// pre-join prefixes and cannot see it.
func TestRunKVCopy_RejectsTraversingSourceKey(t *testing.T) {
	srcSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/src_kv/metadata/src/dev" {
			_, _ = w.Write([]byte(`{"data":{"keys":["openrc","../../../prod/openrc"]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"data":{"value":"x"}}}`))
	}))
	defer srcSrv.Close()
	dstSrv, writes := dstVaultFixture(t)
	src, dst := clients(t, srcSrv.URL, dstSrv.URL)

	err := runKVCopy(context.Background(), src, dst, &output.Options{Format: output.FormatValue}, copyOpts(true), &bytes.Buffer{})
	if err == nil {
		t.Fatal("a traversing source key must fail the copy")
	}
	if !strings.Contains(err.Error(), "prod/openrc") {
		t.Errorf("err = %v, want it to name the offending path", err)
	}
	if len(writes) != 0 {
		t.Errorf("nothing must be written outside the chosen subtree, got %v", writes)
	}
}

// VAULT_SRC_SKIP_VERIFY must mean the same thing here as OS_INSECURE does in
// internal/auth: this group used to carry its own divergent envBool, whose comment
// claimed to mirror auth's while doing something else.
func TestInsecureSrcVault_EnvBoolFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		val  string
		want bool
	}{
		{"no", false},
		{"off", false},
		{"disabled", false},
		{"0", false},
		{"false", false},
		{"maybe", false}, // unparseable → closed
		{"yes", true},
		{"1", true},
	} {
		t.Run("VAULT_SRC_SKIP_VERIFY="+tc.val, func(t *testing.T) {
			clearSrcEnv(t)
			t.Setenv("VAULT_SRC_SKIP_VERIFY", tc.val)

			s := &srcFlags{}
			s.addTo(pflag.NewFlagSet("t", pflag.ContinueOnError))
			if s.insecure != tc.want {
				t.Errorf("insecure = %v, want %v", s.insecure, tc.want)
			}
			got, err := s.config(vault.Config{Addr: "https://dst", Token: "t"})
			if err != nil {
				t.Fatal(err)
			}
			if got.Insecure != tc.want {
				t.Errorf("config Insecure = %v, want %v", got.Insecure, tc.want)
			}
		})
	}
}

// TestSrcFlagsConfig covers the inheritance rule: unset --src-vault-* fields
// come from the destination, and any explicit source credential replaces the
// destination's whole credential set.
func TestSrcFlagsConfig(t *testing.T) {
	clearSrcEnv(t)
	dstCfg := vault.Config{
		Addr: "https://dst", Namespace: "dst-ns", Token: "dst-tok",
		KVMount: "dst_kv", ApprolePath: "approle", CACertPEM: []byte("ca"),
	}

	t.Run("inherits everything when unset", func(t *testing.T) {
		s := &srcFlags{}
		s.addTo(pflag.NewFlagSet("t", pflag.ContinueOnError))
		got, err := s.config(dstCfg)
		if err != nil {
			t.Fatal(err)
		}
		if got.Addr != "https://dst" || got.Namespace != "dst-ns" || got.Token != "dst-tok" || got.KVMount != "dst_kv" {
			t.Errorf("config = %+v, want the destination's values", got)
		}
	})

	t.Run("explicit source AppRole drops the inherited token", func(t *testing.T) {
		s := &srcFlags{}
		fs := pflag.NewFlagSet("t", pflag.ContinueOnError)
		s.addTo(fs)
		if err := fs.Parse([]string{
			"--src-vault-addr", "https://src",
			"--src-vault-role-id", "rid",
			"--src-vault-secret-id", "sid",
			"--src-vault-kv-mount", "src_kv",
		}); err != nil {
			t.Fatal(err)
		}
		got, err := s.config(dstCfg)
		if err != nil {
			t.Fatal(err)
		}
		if got.Token != "" {
			t.Errorf("Token = %q, want empty so the source AppRole is used", got.Token)
		}
		if got.RoleID != "rid" || got.SecretID != "sid" || got.Addr != "https://src" || got.KVMount != "src_kv" {
			t.Errorf("config = %+v", got)
		}
		if string(got.CACertPEM) != "ca" {
			t.Errorf("CACertPEM = %q, want the inherited bundle", got.CACertPEM)
		}
	})

	t.Run("explicitly empty namespace clears the inherited one", func(t *testing.T) {
		s := &srcFlags{}
		fs := pflag.NewFlagSet("t", pflag.ContinueOnError)
		s.addTo(fs)
		if err := fs.Parse([]string{"--src-vault-namespace", ""}); err != nil {
			t.Fatal(err)
		}
		got, err := s.config(dstCfg)
		if err != nil {
			t.Fatal(err)
		}
		if got.Namespace != "" {
			t.Errorf("Namespace = %q, want empty", got.Namespace)
		}
	})
}

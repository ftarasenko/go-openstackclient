//go:build functional

package functional

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/ftarasenko/go-openstackclient/internal/vault/vaulttest"
)

// koc's Vault path end to end: the real binary against internal/vault/vaulttest,
// the fake pinned to a real server's answers (docs/verification/
// 2026-09-27-vault-api-probe.md). The kv tests need no cloud and run wherever
// the functional tag builds; TestCredsFromVault also needs a devstack, because
// the openrc it serves has to authenticate somewhere real.

// vaultRunner points koc at s with the given credentials. VAULT_ADDR plus a
// token or an AppRole is what stops koc auto-discovering Vault from Kubernetes.
func vaultRunner(s *vaulttest.Server, creds ...string) runner {
	return runner{env: append(append(baseEnv(), "VAULT_ADDR="+s.URL), creds...)}
}

func seedRegion(s *vaulttest.Server) {
	s.Put("", "secret_v2", "reg/a/openrc", map[string]any{"OS_USERNAME": "admin"})
	s.Put("", "secret_v2", "reg/a/openrc", map[string]any{"OS_USERNAME": "admin", "OS_PASSWORD": "v2"})
	s.Put("", "secret_v2", "reg/b/openrc", map[string]any{"OS_USERNAME": "demo"})
}

// kvKeys decodes `vault kv list -f json` into its key column.
func kvKeys(t *testing.T, r runner, path string) []string {
	t.Helper()
	var rows []map[string]any
	r.json(t, &rows, "vault", "kv", "list", path)
	var keys []string
	for _, row := range rows {
		for _, v := range row {
			if s, ok := v.(string); ok {
				keys = append(keys, s)
			}
		}
	}
	sort.Strings(keys)
	return keys
}

func TestVaultKV_ListAndGet(t *testing.T) {
	s := vaulttest.New(t)
	seedRegion(s)
	r := vaultRunner(s, "VAULT_TOKEN="+s.RootToken)

	if got := kvKeys(t, r, "/reg"); strings.Join(got, ",") != "a/,b/" {
		t.Errorf("kv list /reg = %v, want the two folders with their slash", got)
	}
	var latest, first map[string]any
	r.json(t, &latest, "vault", "kv", "get", "/reg/a/openrc")
	r.json(t, &first, "vault", "kv", "get", "/reg/a/openrc", "--version", "1")
	if latest["OS_PASSWORD"] != "v2" || first["OS_PASSWORD"] != nil {
		t.Errorf("kv get latest = %v, --version 1 = %v", latest, first)
	}
	// Real Vault answers a missing secret and an empty folder with the same
	// 404; both must fail cleanly, not print an empty table.
	r.fails(t, "vault", "kv", "get", "/reg/nope")
	r.fails(t, "vault", "kv", "list", "/nope")
}

func TestVaultKV_AppRole(t *testing.T) {
	s := vaulttest.New(t)
	seedRegion(s)
	s.AddAppRole("role-1", "secret-1", vaulttest.ReadOnly)

	ok := vaultRunner(s, "VAULT_ROLE_ID=role-1", "VAULT_SECRET_ID=secret-1")
	if got := kvKeys(t, ok, "/reg/b"); strings.Join(got, ",") != "openrc" {
		t.Errorf("kv list with an AppRole = %v", got)
	}

	bad := vaultRunner(s, "VAULT_ROLE_ID=role-1", "VAULT_SECRET_ID=wrong")
	if msg := bad.fails(t, "vault", "kv", "list", "/reg"); !strings.Contains(msg, "invalid role or secret ID") {
		t.Errorf("wrong secret-id: stderr %q, want vault's own message", msg)
	}
}

func TestVaultKV_Copy(t *testing.T) {
	s := vaulttest.New(t)
	seedRegion(s)
	s.AddAppRole("reader", "r", vaulttest.ReadOnly)

	rw := vaultRunner(s, "VAULT_TOKEN="+s.RootToken)
	rw.ok(t, "vault", "kv", "copy", "-r", "/reg", "/copy")
	if got := kvKeys(t, rw, "/copy"); strings.Join(got, ",") != "a/,b/" {
		t.Errorf("after copy -r, /copy = %v", got)
	}
	var got map[string]any
	rw.json(t, &got, "vault", "kv", "get", "/copy/a/openrc")
	if got["OS_PASSWORD"] != "v2" {
		t.Errorf("copy carried %v, want the latest version", got)
	}

	ro := vaultRunner(s, "VAULT_ROLE_ID=reader", "VAULT_SECRET_ID=r")
	if msg := ro.fails(t, "vault", "kv", "copy", "/reg/b/openrc", "/copy/denied"); !strings.Contains(msg, "permission denied") {
		t.Errorf("copy with a read-only role: stderr %q, want permission denied", msg)
	}
}

// export seals every secret to a public key; decrypt, with the private key and
// no Vault at all, must give back exactly what was stored.
func TestVaultKV_ExportDecryptRoundTrip(t *testing.T) {
	s := vaulttest.New(t)
	seedRegion(s)
	dir := t.TempDir()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	pubPath := writeFile(t, dir, "recipient.pem", string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub})))
	privPath := writeFile(t, dir, "identity.pem", string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})))
	report := filepath.Join(dir, "report.xml")

	r := vaultRunner(s, "VAULT_TOKEN="+s.RootToken)
	r.ok(t, "vault", "kv", "export", "/reg", "--recipient", pubPath, "-o", report)

	var rows []map[string]any
	runner{env: baseEnv()}.json(t, &rows, "vault", "kv", "decrypt", report, "--identity", privPath)
	found := map[string]bool{}
	for _, row := range rows {
		found[strings.Join([]string{str(row["Path"]), str(row["Key"]), str(row["Value"])}, " ")] = true
	}
	for _, want := range []string{"reg/a/openrc OS_PASSWORD v2", "reg/b/openrc OS_USERNAME demo"} {
		if !found[want] {
			t.Errorf("decrypted report lacks %q; got %v", want, rows)
		}
	}
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// TestCredsFromVault serves devstack's own openrc from the fake Vault and has
// koc authenticate to the real Keystone with it — once as devstack writes it
// (domains by ID, the case that used to fail) and once with domains by name.
func TestCredsFromVault(t *testing.T) {
	c := ft(t)
	rc := openrcMap(c)

	var want []map[string]any
	defaultRunner(c).json(t, &want, "network", "list")

	byName := map[string]any{}
	for k, v := range rc {
		byName[k] = v
	}
	delete(byName, "OS_USER_DOMAIN_ID")
	delete(byName, "OS_PROJECT_DOMAIN_ID")
	byName["OS_USER_DOMAIN_NAME"] = "Default"
	byName["OS_PROJECT_DOMAIN_NAME"] = "Default"

	for name, secret := range map[string]map[string]any{"domain IDs": rc, "domain names": byName} {
		t.Run(name, func(t *testing.T) {
			s := vaulttest.New(t)
			s.Put("", "secret_v2", "koc-ft/openrc", secret)
			s.AddAppRole("role-1", "secret-1", vaulttest.ReadOnly)

			var got []map[string]any
			vaultRunner(s, "VAULT_ROLE_ID=role-1", "VAULT_SECRET_ID=secret-1").
				json(t, &got, "--creds-from-vault", "/koc-ft/openrc", "network", "list")
			if ids(got) != ids(want) {
				t.Errorf("networks through --creds-from-vault = %s, want %s", ids(got), ids(want))
			}
		})
	}
}

// openrcMap is openrc.env as a secret body: the authentication variables only.
func openrcMap(c *cloud) map[string]any {
	m := map[string]any{}
	for _, kv := range c.Openrc {
		k, v, _ := strings.Cut(kv, "=")
		switch k {
		case "OS_AUTH_URL", "OS_USERNAME", "OS_PASSWORD", "OS_PROJECT_NAME", "OS_REGION_NAME",
			"OS_USER_DOMAIN_ID", "OS_PROJECT_DOMAIN_ID", "OS_USER_DOMAIN_NAME", "OS_PROJECT_DOMAIN_NAME":
			m[k] = v
		}
	}
	return m
}

// ids is the sorted ID column of a -f json listing, for comparing two runs.
func ids(rows []map[string]any) string {
	var out []string
	for _, r := range rows {
		out = append(out, str(r["ID"]))
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

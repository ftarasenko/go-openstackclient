package vaulttest_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ftarasenko/go-openstackclient/internal/vault"
	"github.com/ftarasenko/go-openstackclient/internal/vault/vaulttest"
)

// koc's own Vault client against the fake: what each probed behaviour turns
// into on koc's side. Where koc loses information today (a 404 whose body says
// why), the test records the current behaviour so a fix shows up as a diff.

func tokenClient(t *testing.T, s *vaulttest.Server, cfg vault.Config) *vault.Client {
	t.Helper()
	cfg.Addr = s.URL
	if cfg.Token == "" && cfg.RoleID == "" {
		cfg.Token = s.RootToken
	}
	c, err := vault.New(t.Context(), cfg)
	if err != nil {
		t.Fatalf("vault.New: %v", err)
	}
	return c
}

func TestClient_ReadWriteListRoundTrip(t *testing.T) {
	s := vaulttest.New(t)
	c := tokenClient(t, s, vault.Config{})

	if err := c.WriteKVData(t.Context(), "secret_v2", "koc-ft/openrc", map[string]any{"OS_USERNAME": "admin"}); err != nil {
		t.Fatalf("WriteKVData: %v", err)
	}
	if err := c.WriteKVData(t.Context(), "secret_v2", "koc-ft/openrc", map[string]any{"OS_USERNAME": "demo"}); err != nil {
		t.Fatalf("WriteKVData v2: %v", err)
	}
	got, err := c.ReadKVData(t.Context(), "koc-ft/openrc")
	if err != nil || got["OS_USERNAME"] != "demo" {
		t.Fatalf("ReadKVData = %v, %v; want the latest version", got, err)
	}
	v1, err := c.ReadKVDataAt(t.Context(), "secret_v2", "koc-ft/openrc", 1)
	if err != nil || v1["OS_USERNAME"] != "admin" {
		t.Fatalf("ReadKVDataAt(1) = %v, %v", v1, err)
	}
	keys, err := c.ListKV(t.Context(), "secret_v2", "")
	if err != nil || !reflect.DeepEqual(keys, []string{"koc-ft/"}) {
		t.Fatalf("ListKV(root) = %v, %v; want the folder with its slash", keys, err)
	}
	if ok, err := c.HasKV(t.Context(), "secret_v2", "koc-ft/openrc"); err != nil || !ok {
		t.Errorf("HasKV(existing) = %v, %v", ok, err)
	}
	if ok, err := c.HasKV(t.Context(), "secret_v2", "koc-ft/none"); err != nil || ok {
		t.Errorf("HasKV(missing) = %v, %v; want false, nil", ok, err)
	}
}

func TestClient_WalkSkipsWhatRealVaultReportsAs404(t *testing.T) {
	s := vaulttest.New(t)
	s.Put("", "secret_v2", "reg/a/openrc", map[string]any{"k": "1"})
	s.Put("", "secret_v2", "reg/a/certs", map[string]any{"k": "2"})
	s.Put("", "secret_v2", "reg/b/openrc", map[string]any{"k": "3"})
	c := tokenClient(t, s, vault.Config{})

	got, err := c.WalkKV(t.Context(), "secret_v2", "reg")
	if err != nil {
		t.Fatalf("WalkKV: %v", err)
	}
	if want := []string{"a/certs", "a/openrc", "b/openrc"}; !reflect.DeepEqual(got, want) {
		t.Errorf("WalkKV = %v, want %v", got, want)
	}
	// A missing root lists as 404, which a walk reads as "nothing here".
	if got, err := c.WalkKV(t.Context(), "secret_v2", "nope"); err != nil || len(got) != 0 {
		t.Errorf("WalkKV(missing) = %v, %v; want empty, nil", got, err)
	}
}

func TestClient_AppRole(t *testing.T) {
	s := vaulttest.New(t)
	s.Put("", "secret_v2", "x", map[string]any{"k": "v"})
	s.AddAppRole("role-1", "secret-1", vaulttest.ReadOnly)

	c := tokenClient(t, s, vault.Config{RoleID: "role-1", SecretID: "secret-1"})
	if _, err := c.ReadKVData(t.Context(), "x"); err != nil {
		t.Fatalf("read after AppRole login: %v", err)
	}
	if err := c.WriteKVData(t.Context(), "secret_v2", "x", map[string]any{"k": "w"}); err == nil ||
		!strings.Contains(err.Error(), "permission denied") {
		t.Errorf("write with a read-only role = %v, want a permission-denied error", err)
	}

	_, err := vault.New(t.Context(), vault.Config{Addr: s.URL, RoleID: "role-1", SecretID: "wrong"})
	if err == nil || !strings.Contains(err.Error(), "invalid role or secret ID") {
		t.Errorf("login with a wrong secret-id = %v, want vault's own message", err)
	}
}

func TestClient_SoftDeletedLatestReadsAsNotFound(t *testing.T) {
	s := vaulttest.New(t)
	s.Put("", "secret_v2", "x", map[string]any{"k": "v"})
	s.SoftDelete("", "secret_v2", "x")
	c := tokenClient(t, s, vault.Config{})

	if _, err := c.ReadKVData(t.Context(), "x"); !errors.Is(err, vault.ErrNotFound) {
		t.Errorf("read of a deleted latest version = %v, want ErrNotFound", err)
	}
	// The metadata still exists, so the secret is not absent.
	if ok, err := c.HasKV(t.Context(), "secret_v2", "x"); err != nil || !ok {
		t.Errorf("HasKV(deleted) = %v, %v; want true", ok, err)
	}
}

func TestClient_NamespaceIsolatesSecrets(t *testing.T) {
	s := vaulttest.New(t, vaulttest.WithNamespace("team"))
	s.Put("team", "secret_v2", "x", map[string]any{"k": "team"})
	s.Put("", "secret_v2", "x", map[string]any{"k": "root"})

	c := tokenClient(t, s, vault.Config{Namespace: "team"})
	got, err := c.ReadKVData(t.Context(), "x")
	if err != nil || got["k"] != "team" {
		t.Fatalf("namespaced read = %v, %v; want the team namespace's secret", got, err)
	}
	for _, r := range s.Requests() {
		if r.Namespace != "team" {
			t.Errorf("request %s %s sent namespace %q, want team", r.Method, r.Path, r.Namespace)
		}
	}
}

// A wrong namespace or mount is a 404 whose body says so, but koc keeps only
// the status: both read as a plain "not found". Recorded as today's behaviour.
func TestClient_404BodyIsDropped(t *testing.T) {
	s := vaulttest.New(t)
	s.Put("", "secret_v2", "x", map[string]any{"k": "v"})

	c := tokenClient(t, s, vault.Config{Namespace: "nosuch"})
	if _, err := c.ReadKVData(t.Context(), "x"); !errors.Is(err, vault.ErrNotFound) {
		t.Errorf("unknown namespace = %v, want ErrNotFound", err)
	}
	c = tokenClient(t, s, vault.Config{KVMount: "nosuch"})
	if _, err := c.ReadKVData(t.Context(), "x"); !errors.Is(err, vault.ErrNotFound) {
		t.Errorf("unknown mount = %v, want ErrNotFound", err)
	}
}

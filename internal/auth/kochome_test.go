package auth

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ftarasenko/go-openstackclient/internal/kochome"
)

// strayDirs points HOME, XDG_CONFIG_HOME, XDG_CONFIG_DIRS, the working
// directory and KOC_HOME at separate temporary directories, plants a decoy
// clouds.yaml / .vault-token in each one koc must not read, and returns
// KOC_HOME plus a func returning every path the resolver opened.
func strayDirs(t *testing.T) (home string, opened func() []string) {
	t.Helper()
	decoy := isolateConfig(t)
	home = t.TempDir()
	t.Setenv(kochome.EnvHome, home)
	t.Setenv("VAULT_TOKEN_FILE", "")
	t.Setenv("KUBECONFIG", "")
	for _, p := range []string{
		filepath.Join(decoy, ".config", "openstack", "clouds.yaml"),
		filepath.Join(decoy, "xdg", "openstack", "clouds.yaml"),
		filepath.Join(decoy, ".vault-token"),
	} {
		writeConfig(t, p, "clouds: {decoy: {auth: {auth_url: https://decoy.example.com}}}\n")
	}

	var mu sync.Mutex
	var paths []string
	restore := kochome.SetOpenHookForTest(func(p string) {
		mu.Lock()
		defer mu.Unlock()
		paths = append(paths, p)
	})
	t.Cleanup(restore)
	return home, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(paths)
	}
}

func assertInside(t *testing.T, home string, paths []string) {
	t.Helper()
	for _, p := range paths {
		if rel, err := filepath.Rel(home, p); err != nil || strings.HasPrefix(rel, "..") {
			t.Errorf("opened %q outside KOC_HOME %q", p, home)
		}
	}
}

// TestKOCHome_ConfinesConfigReads: with KOC_HOME set, clouds.yaml, secure.yaml
// and ~/.vault-token are looked up only there.
func TestKOCHome_ConfinesConfigReads(t *testing.T) {
	if kochome.BuiltEmbedded() {
		t.Skip("a koc_embedded build is always the embedded profile")
	}
	home, opened := strayDirs(t)
	t.Setenv(kochome.EnvProfile, "")
	writeConfig(t, filepath.Join(home, "clouds.yaml"),
		"clouds:\n  inside:\n    auth:\n      auth_url: https://keystone.example.com/v3\n")

	cf, err := findCloudsFile()
	if err != nil {
		t.Fatal(err)
	}
	if cf == nil || cf.path != filepath.Join(home, "clouds.yaml") || !slices.Equal(cf.names, []string{"inside"}) {
		t.Fatalf("findCloudsFile = %+v, want the KOC_HOME clouds.yaml", cf)
	}
	if tok := readVaultTokenFile(); tok != "" {
		t.Errorf("readVaultTokenFile = %q, want nothing (no .vault-token in KOC_HOME)", tok)
	}
	if len(opened()) == 0 {
		t.Fatal("the open hook saw nothing; the lookups are not routed through kochome")
	}
	assertInside(t, home, opened())
}

// TestKOCHome_EmbeddedOpensNothingOutside runs the embedded profile's lookups —
// a named cloud, the Vault token cache, a whole Authenticate — with every
// conventional location holding a decoy, and asserts nothing outside KOC_HOME
// is opened.
func TestKOCHome_EmbeddedOpensNothingOutside(t *testing.T) {
	home, opened := strayDirs(t)
	t.Setenv(kochome.EnvProfile, kochome.ProfileEmbedded)
	t.Setenv("OS_CLIENT_CONFIG_FILE", filepath.Join(t.TempDir(), "clouds.yaml"))
	t.Setenv("OS_CLOUD", "decoy")

	if cf, err := findCloudsFile(); err != nil || cf != nil {
		t.Fatalf("findCloudsFile = %+v, %v; the embedded profile reads no clouds.yaml", cf, err)
	}
	if tok := readVaultTokenFile(); tok != "" {
		t.Errorf("readVaultTokenFile = %q, want nothing", tok)
	}
	_, err := envOptions(t).Authenticate(context.Background())
	if err == nil || !strings.Contains(err.Error(), "console") {
		t.Errorf("Authenticate with OS_CLOUD = %v, want the console's clouds.yaml refusal", err)
	}
	assertInside(t, home, opened())
}

package kube

import (
	"path/filepath"
	"testing"

	"github.com/ftarasenko/go-openstackclient/internal/kochome"
)

func TestResolveKubeconfigPath_KOCHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv(kochome.EnvHome, home)
	t.Setenv(kochome.EnvProfile, "")
	t.Setenv("KUBECONFIG", "")
	if got, want := resolveKubeconfigPath(""), filepath.Join(home, ".kube", "config"); got != want {
		t.Errorf("under KOC_HOME: %q, want %q", got, want)
	}

	explicit := filepath.Join(t.TempDir(), "kc")
	t.Setenv("KUBECONFIG", explicit)
	if got := resolveKubeconfigPath(""); got != explicit {
		t.Errorf("KUBECONFIG under KOC_HOME (default profile): %q, want the explicit %q", got, explicit)
	}

	t.Setenv(kochome.EnvProfile, kochome.ProfileEmbedded)
	if got, want := resolveKubeconfigPath(""), filepath.Join(home, ".kube", "config"); got != want {
		t.Errorf("embedded profile: %q, want KUBECONFIG ignored and %q", got, want)
	}
}

package auth

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// autoDiscoverVault decides whether a failed LCM auto-discovery ends the command
// or is merely noted. That decision is the difference between a node operator
// getting a usable error and a workstation run being killed by a cluster it was
// never going to reach.

// A kubeconfig path that cannot exist, so discovery always fails.
func unreachableKubeconfig(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "no-such-kubeconfig")
}

// Nothing is configured, so a failed discovery leaves nothing to fall back to.
func TestAutoDiscoverVault_FatalWithoutAddr(t *testing.T) {
	o := &Options{Kubeconfig: unreachableKubeconfig(t)}
	err := o.autoDiscoverVault(context.Background())
	if err == nil {
		t.Fatal("autoDiscoverVault() = nil, want a fatal error when no address is known")
	}
	if !strings.Contains(err.Error(), "vault not configured and cluster auto-discovery failed") {
		t.Errorf("unexpected error: %v", err)
	}
	// The message has to carry the way out, since discovery cannot.
	for _, want := range []string{"--vault-addr", "--kubeconfig"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %s: %v", want, err)
		}
	}
}

// An address is known but the credential is not, so discovery still runs — and
// its failure must not end a run that a VAULT_TOKEN in the environment, or the
// default KV mount, may yet complete.
func TestAutoDiscoverVault_NonFatalWithAddr(t *testing.T) {
	o := &Options{
		VaultAddr:  "https://vault.example.com:8200",
		Kubeconfig: unreachableKubeconfig(t),
	}
	out := captureStderr(t, func() {
		if err := o.autoDiscoverVault(context.Background()); err != nil {
			t.Fatalf("autoDiscoverVault() = %v, want nil once an address is known", err)
		}
	})
	if out != "" {
		t.Errorf("discovery failure was reported without --debug:\n%s", out)
	}
}

// Without --debug the failure is silent; with it the operator gets the reason.
func TestAutoDiscoverVault_DebugReportsTheFailure(t *testing.T) {
	o := &Options{
		VaultAddr:  "https://vault.example.com:8200",
		Kubeconfig: unreachableKubeconfig(t),
		Debug:      true,
	}
	out := captureStderr(t, func() {
		if err := o.autoDiscoverVault(context.Background()); err != nil {
			t.Fatalf("autoDiscoverVault() = %v, want nil once an address is known", err)
		}
	})
	if !strings.Contains(out, "vault: cluster auto-discovery:") {
		t.Errorf("--debug did not report the discovery failure:\n%s", out)
	}
}

// A fully-configured Vault needs no discovery, so the cluster is never touched —
// an unreachable kubeconfig must not even be opened.
func TestAutoDiscoverVault_SkippedWhenFullyConfigured(t *testing.T) {
	for _, tc := range []struct {
		name string
		o    *Options
	}{
		{
			name: "an address and a token",
			o:    &Options{VaultAddr: "https://vault.example.com:8200", VaultToken: "s.faketoken"},
		},
		{
			name: "an address and a complete AppRole",
			o: &Options{
				VaultAddr:     "https://vault.example.com:8200",
				VaultRoleID:   "11111111-1111-1111-1111-111111111111",
				VaultSecretID: "22222222-2222-2222-2222-222222222222",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.o.Kubeconfig = unreachableKubeconfig(t)
			tc.o.Debug = true
			out := captureStderr(t, func() {
				if err := tc.o.autoDiscoverVault(context.Background()); err != nil {
					t.Fatalf("autoDiscoverVault() = %v, want nil", err)
				}
			})
			if out != "" {
				t.Errorf("discovery ran for a fully-configured Vault:\n%s", out)
			}
		})
	}
}

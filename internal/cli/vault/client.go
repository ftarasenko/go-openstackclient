// Package vaultcli implements the koc-specific "koc vault ..." command group: a
// thin KV v2 client (list/get) plus a Vault→Vault copy. It has no
// python-openstackclient equivalent — Vault is not an OpenStack service — and it
// deliberately never authenticates against Keystone, so `koc vault kv ...` works
// with Vault credentials alone.
//
// The destination (and only) Vault is described by the global --vault-* flags,
// which already support VAULT_* env defaults, the ~/.vault-token cache and LCM
// cluster auto-discovery (see internal/auth/credsfrom.go). "kv copy" adds
// --src-vault-* overrides for the source side; each unset override inherits the
// destination's value, so a copy within one Vault needs no extra flags.
package vaultcli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/pflag"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/vault"
)

// srcFlags holds the --src-vault-* connection overrides for "kv copy". The env
// names match the variables the KeyStack e2e pipeline already exports for
// vault-helper.py (VAULT_SRC_ADDR/TOKEN/ENGINE/PREFIX), so a job can swap the
// script for koc without rewiring its variables.
type srcFlags struct {
	addr      string
	namespace string
	token     string
	roleID    string
	secretID  string
	kvMount   string
	kvPrefix  string
	cacert    string
	insecure  bool

	fs *pflag.FlagSet // to tell "unset" from "explicitly empty/false"
}

func (s *srcFlags) addTo(fs *pflag.FlagSet) {
	s.fs = fs
	fs.StringVar(&s.addr, "src-vault-addr", os.Getenv("VAULT_SRC_ADDR"),
		"source Vault address (env VAULT_SRC_ADDR; default: the destination's)")
	fs.StringVar(&s.namespace, "src-vault-namespace", os.Getenv("VAULT_SRC_NAMESPACE"),
		"source Vault namespace (env VAULT_SRC_NAMESPACE; default: the destination's)")
	fs.StringVar(&s.token, "src-vault-token", os.Getenv("VAULT_SRC_TOKEN"),
		"source Vault token (env VAULT_SRC_TOKEN; default: the destination's credentials)")
	fs.StringVar(&s.roleID, "src-vault-role-id", os.Getenv("VAULT_SRC_ROLE_ID"),
		"source Vault AppRole role_id (env VAULT_SRC_ROLE_ID)")
	fs.StringVar(&s.secretID, "src-vault-secret-id", os.Getenv("VAULT_SRC_SECRET_ID"),
		"source Vault AppRole secret_id (env VAULT_SRC_SECRET_ID)")
	fs.StringVar(&s.kvMount, "src-vault-kv-mount", os.Getenv("VAULT_SRC_ENGINE"),
		"source Vault KV v2 mount (env VAULT_SRC_ENGINE; default: the destination's)")
	fs.StringVar(&s.kvPrefix, "src-vault-kv-prefix", os.Getenv("VAULT_SRC_PREFIX"),
		"path prefix for a relative source path (env VAULT_SRC_PREFIX; default: the destination's)")
	fs.StringVar(&s.cacert, "src-vault-cacert", os.Getenv("VAULT_SRC_CACERT"),
		"CA bundle for the source Vault endpoint (env VAULT_SRC_CACERT; default: the destination's)")
	fs.BoolVar(&s.insecure, "insecure-src-vault", envBool("VAULT_SRC_SKIP_VERIFY"),
		"disable TLS verification for the source Vault (env VAULT_SRC_SKIP_VERIFY)")
}

// explicit reports whether a source override was set on the command line or via
// its environment variable — the two ways a user asks for a value, including an
// intentionally empty one (e.g. clearing an inherited namespace).
func (s *srcFlags) explicit(flag, env string) bool {
	if s.fs != nil && s.fs.Changed(flag) {
		return true
	}
	return os.Getenv(env) != ""
}

func (s *srcFlags) str(flag, env, val, dst string) string {
	if s.explicit(flag, env) {
		return val
	}
	return dst
}

// config derives the source Vault config from the destination's, applying the
// --src-vault-* overrides. Credentials are replaced as a group: if any of
// token/role_id/secret_id is given for the source, none of the destination's are
// inherited, otherwise an inherited token would silently win over an explicit
// source AppRole.
func (s *srcFlags) config(dst vault.Config) (vault.Config, error) {
	cfg := dst
	cfg.Addr = s.str("src-vault-addr", "VAULT_SRC_ADDR", s.addr, dst.Addr)
	cfg.Namespace = s.str("src-vault-namespace", "VAULT_SRC_NAMESPACE", s.namespace, dst.Namespace)
	cfg.KVMount = s.str("src-vault-kv-mount", "VAULT_SRC_ENGINE", s.kvMount, dst.KVMount)

	if s.explicit("src-vault-token", "VAULT_SRC_TOKEN") ||
		s.explicit("src-vault-role-id", "VAULT_SRC_ROLE_ID") ||
		s.explicit("src-vault-secret-id", "VAULT_SRC_SECRET_ID") {
		cfg.Token, cfg.RoleID, cfg.SecretID = s.token, s.roleID, s.secretID
	}

	if s.explicit("src-vault-cacert", "VAULT_SRC_CACERT") {
		b, err := os.ReadFile(s.cacert)
		if err != nil {
			return vault.Config{}, fmt.Errorf("reading --src-vault-cacert %q: %w", s.cacert, err)
		}
		cfg.CACertPEM = b
	}
	if s.explicit("insecure-src-vault", "VAULT_SRC_SKIP_VERIFY") {
		cfg.Insecure = s.insecure
	}
	return cfg, nil
}

// newVaultClient builds the client for the Vault described by the global
// --vault-* flags. Unlike every other koc command group this performs no
// Keystone authentication.
func newVaultClient(ctx context.Context, a *auth.Options) (*vault.Client, error) {
	return a.VaultClient(ctx)
}

// newVaultClientPair builds the destination client from the global --vault-*
// flags and the source client from that same config with the --src-vault-*
// overrides applied.
func newVaultClientPair(ctx context.Context, a *auth.Options, s *srcFlags) (src, dst *vault.Client, err error) {
	dstCfg, err := a.VaultConfig(ctx)
	if err != nil {
		return nil, nil, err
	}
	srcCfg, err := s.config(dstCfg)
	if err != nil {
		return nil, nil, err
	}
	if src, err = vault.New(ctx, srcCfg); err != nil {
		return nil, nil, fmt.Errorf("source vault: %w", err)
	}
	if dst, err = vault.New(ctx, dstCfg); err != nil {
		return nil, nil, fmt.Errorf("destination vault: %w", err)
	}
	return src, dst, nil
}

// envBool mirrors auth's lax OS_*/VAULT_* boolean handling: any non-empty,
// non-parseable value is truthy.
func envBool(key string) bool {
	switch os.Getenv(key) {
	case "", "0", "false", "FALSE", "False", "no", "off":
		return false
	}
	return true
}

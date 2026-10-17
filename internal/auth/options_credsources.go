//go:build !koc_embedded

package auth

import (
	"os"

	"github.com/spf13/pflag"
)

// addCredentialSourceFlags registers the koc-specific credential sources and
// the Kubernetes and Vault connection flags behind them. A koc_embedded build
// compiles none of that code, so it registers none of these flags.
func (o *Options) addCredentialSourceFlags(fs *pflag.FlagSet) {
	// koc-specific credential sources. UNVERIFIED against KeyStack: these have no
	// python-openstackclient equivalent; they load credentials from the LCM
	// (k0s) cluster / Vault so operators can skip clouds.yaml/OS_* setup.
	fs.StringVar(&o.CredsFromNS, "creds-from-ns", os.Getenv("KOC_CREDS_FROM_NS"),
		"load a standalone Ironic's basic-auth credentials from this Kubernetes namespace (baremetal only)")
	fs.StringVar(&o.CredsFromVault, "creds-from-vault", os.Getenv("KOC_CREDS_FROM_VAULT"),
		"load OpenStack credentials from this Vault KV v2 openrc secret; path may start with the mount (secret_v2/…) or / for absolute, else it is relative to --vault-kv-prefix")

	fs.StringVar(&o.Kubeconfig, "kubeconfig", os.Getenv("KUBECONFIG"),
		"path to the kubeconfig for --creds-from-ns (env KUBECONFIG; default ~/.kube/config)")
	fs.StringVar(&o.KubeContext, "kube-context", os.Getenv("KUBE_CONTEXT"),
		"kubeconfig context for --creds-from-ns (default: current-context)")

	// The --vault-* flags describe the Vault used by --creds-from-vault and by the
	// "koc vault kv" command group (where they name the copy destination).
	fs.StringVar(&o.VaultAddr, "vault-addr", os.Getenv("VAULT_ADDR"),
		"Vault address (env VAULT_ADDR)")
	fs.StringVar(&o.VaultNamespace, "vault-namespace", os.Getenv("VAULT_NAMESPACE"),
		"Vault Enterprise namespace, sent as X-Vault-Namespace (env VAULT_NAMESPACE)")
	fs.StringVar(&o.VaultToken, "vault-token", os.Getenv("VAULT_TOKEN"),
		"Vault token; if set, AppRole login is skipped (env VAULT_TOKEN; falls back to ~/.vault-token from `vault login`)")
	fs.StringVar(&o.VaultRoleID, "vault-role-id", os.Getenv("VAULT_ROLE_ID"),
		"Vault AppRole role_id (env VAULT_ROLE_ID)")
	fs.StringVar(&o.VaultSecretID, "vault-secret-id", os.Getenv("VAULT_SECRET_ID"),
		"Vault AppRole secret_id (env VAULT_SECRET_ID)")
	fs.StringVar(&o.VaultApprolePath, "vault-approle-path", envOr("VAULT_APPROLE_PATH", "approle"),
		"Vault AppRole auth mount path (env VAULT_APPROLE_PATH)")
	// VAULT_ENGINE / VAULT_PREFIX are accepted as fallbacks: they are the
	// destination-side counterparts of the VAULT_SRC_ENGINE / VAULT_SRC_PREFIX
	// names the KeyStack e2e pipeline exports for "vault kv copy", so both sides
	// of a copy can be configured with symmetric variable names. The koc-native
	// VAULT_KV_* names win when both are set.
	fs.StringVar(&o.VaultKVMount, "vault-kv-mount", envOr("VAULT_KV_MOUNT", envOr("VAULT_ENGINE", "secret_v2")),
		"Vault KV v2 mount (env VAULT_KV_MOUNT, or VAULT_ENGINE)")
	fs.StringVar(&o.VaultKVPrefix, "vault-kv-prefix", envOr("VAULT_KV_PREFIX", os.Getenv("VAULT_PREFIX")),
		"default path prefix prepended to a relative Vault KV path (env VAULT_KV_PREFIX, or VAULT_PREFIX)")
	fs.StringVar(&o.VaultCACert, "vault-cacert", os.Getenv("VAULT_CACERT"),
		"path to a CA bundle for the Vault TLS endpoint (env VAULT_CACERT)")
	// --insecure-vault skips TLS verification for the Vault endpoint used by
	// --creds-from-vault. The global --insecure governs only the OpenStack/
	// Keystone TLS, not Vault, so this is a separate opt-out. --vault-insecure is
	// kept as a hidden back-compat alias (both bind to the same value).
	fs.BoolVar(&o.VaultInsecure, "insecure-vault", EnvBool("VAULT_SKIP_VERIFY"),
		"disable TLS verification for the Vault endpoint (env VAULT_SKIP_VERIFY)")
	fs.BoolVar(&o.VaultInsecure, "vault-insecure", EnvBool("VAULT_SKIP_VERIFY"),
		"deprecated alias of --insecure-vault (env VAULT_SKIP_VERIFY)")

	// Advanced knobs for the Vault / Kubernetes credential sources. They stay
	// fully functional (and env-defaulted, and documented in the README) but are
	// hidden from --help so the everyday flag list is not buried — they matter
	// only alongside --creds-from-vault / --creds-from-ns.
	for _, name := range []string{
		"vault-addr", "vault-namespace", "vault-token", "vault-role-id",
		"vault-secret-id", "vault-approle-path", "vault-kv-mount",
		"vault-kv-prefix", "vault-cacert", "vault-insecure",
		"kubeconfig", "kube-context",
	} {
		_ = fs.MarkHidden(name)
	}
}

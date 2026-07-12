// Package auth builds a single authenticated gophercloud ProviderClient per
// invocation and derives per-service clients from it. It implements the
// cross-cutting auth, TLS and microversion requirements shared by every koc
// command.
//
// Authentication precedence (highest first):
//
//  1. --os-cloud / OS_CLOUD  → clouds.yaml (via gophercloud config/clouds.Parse)
//  2. OS_* environment variables
//  3. Application credentials (OS_APPLICATION_CREDENTIAL_ID / _SECRET),
//     which are honored through either of the two paths above.
//
// TLS is always wired explicitly into the provider so behavior matches OSC:
// custom CA bundle (OS_CACERT / --os-cacert), mutual TLS client cert+key
// (OS_CERT+OS_KEY / --os-cert+--os-key), hostname verification on by default
// with an explicit --insecure / OS_INSECURE opt-out, and a TLS 1.2 minimum.
package auth

import (
	"os"
	"strconv"

	"github.com/spf13/pflag"
)

// Default microversions negotiated per service. "latest" instructs the endpoint
// to serve the highest microversion it supports; operators can pin a specific
// version per service via flag or environment variable.
const (
	defaultBaremetalMicroversion = "latest"
	defaultComputeMicroversion   = "latest"
	defaultVolumeMicroversion    = "latest"
)

// Options carries every global auth/TLS/microversion/debug flag. It is
// registered once on the root command's persistent flags and shared with all
// subcommands, which turn it into service clients via the factory methods.
type Options struct {
	// clouds.yaml selection.
	Cloud string

	// OS_* auth overrides. When a flag is left at its (env-derived) default we
	// defer to clouds.yaml / AuthOptionsFromEnv; explicit flags win.
	AuthURL           string
	Username          string
	UserID            string
	Password          string
	ProjectName       string
	ProjectID         string
	ProjectDomainName string
	UserDomainName    string
	DomainName        string
	RegionName        string
	Interface         string

	// Application credentials.
	AppCredID     string
	AppCredName   string
	AppCredSecret string

	// TLS.
	CACert     string
	ClientCert string
	ClientKey  string
	Insecure   bool

	// In-memory client certificate/key (PEM), used when the material comes from a
	// source other than a file — notably --creds-from-vault, which loads the
	// openrc's mTLS cert/key from the sibling ssl_certificates KV secret. When
	// set, these take precedence over the ClientCert/ClientKey file paths.
	ClientCertPEM []byte
	ClientKeyPEM  []byte

	// Per-service microversions.
	BaremetalAPIVersion string
	ComputeAPIVersion   string
	VolumeAPIVersion    string

	// KeyVRM (in-house service registered in the Keystone catalog as type
	// "keyvrm"). An explicit endpoint override bypasses catalog discovery,
	// following OSC's OS_<SERVICE>_ENDPOINT_OVERRIDE convention.
	KeyVRMEndpoint string

	// koc-specific credential sources (no python-openstackclient equivalent).
	// These are mutually exclusive. CredsFromNS reads a standalone Ironic's
	// basic-auth secret from a Kubernetes namespace (baremetal only, no
	// Keystone); CredsFromVault reads an openrc-style KV v2 secret from Vault and
	// feeds the normal Keystone flow. See internal/auth/credsfrom.go.
	CredsFromNS    string
	CredsFromVault string

	// Kubernetes access (for CredsFromNS).
	Kubeconfig  string
	KubeContext string

	// Vault access (for CredsFromVault). Names mirror the standard VAULT_* CLI.
	VaultAddr        string
	VaultNamespace   string
	VaultToken       string
	VaultRoleID      string
	VaultSecretID    string
	VaultApprolePath string
	VaultKVMount     string
	VaultKVPrefix    string
	VaultCACert      string
	VaultInsecure    bool

	// Diagnostics.
	Debug bool

	// fs is retained so factory methods can distinguish an explicitly-set flag
	// from an env-derived default (notably for --insecure).
	fs *pflag.FlagSet
}

// AddFlags registers the global auth/TLS/microversion flags. Defaults are drawn
// from the corresponding OS_* environment variables so that flag-or-env
// precedence matches python-openstackclient.
func (o *Options) AddFlags(fs *pflag.FlagSet) {
	o.fs = fs

	fs.StringVar(&o.Cloud, "os-cloud", os.Getenv("OS_CLOUD"),
		"named cloud from clouds.yaml (env OS_CLOUD)")

	fs.StringVar(&o.AuthURL, "os-auth-url", os.Getenv("OS_AUTH_URL"),
		"authentication URL (env OS_AUTH_URL)")
	fs.StringVar(&o.Username, "os-username", os.Getenv("OS_USERNAME"),
		"username (env OS_USERNAME)")
	fs.StringVar(&o.UserID, "os-user-id", os.Getenv("OS_USER_ID"),
		"user ID (env OS_USER_ID)")
	fs.StringVar(&o.Password, "os-password", os.Getenv("OS_PASSWORD"),
		"password (env OS_PASSWORD)")
	fs.StringVar(&o.ProjectName, "os-project-name", os.Getenv("OS_PROJECT_NAME"),
		"project name (env OS_PROJECT_NAME)")
	fs.StringVar(&o.ProjectID, "os-project-id", os.Getenv("OS_PROJECT_ID"),
		"project ID (env OS_PROJECT_ID)")
	fs.StringVar(&o.ProjectDomainName, "os-project-domain-name", os.Getenv("OS_PROJECT_DOMAIN_NAME"),
		"project domain name (env OS_PROJECT_DOMAIN_NAME)")
	fs.StringVar(&o.UserDomainName, "os-user-domain-name", os.Getenv("OS_USER_DOMAIN_NAME"),
		"user domain name (env OS_USER_DOMAIN_NAME)")
	fs.StringVar(&o.DomainName, "os-domain-name", os.Getenv("OS_DOMAIN_NAME"),
		"domain name for domain-scoped tokens (env OS_DOMAIN_NAME)")
	fs.StringVar(&o.RegionName, "os-region-name", os.Getenv("OS_REGION_NAME"),
		"region name (env OS_REGION_NAME)")
	fs.StringVar(&o.Interface, "os-interface", os.Getenv("OS_INTERFACE"),
		"endpoint interface: public, internal or admin (env OS_INTERFACE)")

	fs.StringVar(&o.AppCredID, "os-application-credential-id", os.Getenv("OS_APPLICATION_CREDENTIAL_ID"),
		"application credential ID (env OS_APPLICATION_CREDENTIAL_ID)")
	fs.StringVar(&o.AppCredName, "os-application-credential-name", os.Getenv("OS_APPLICATION_CREDENTIAL_NAME"),
		"application credential name (env OS_APPLICATION_CREDENTIAL_NAME)")
	fs.StringVar(&o.AppCredSecret, "os-application-credential-secret", os.Getenv("OS_APPLICATION_CREDENTIAL_SECRET"),
		"application credential secret (env OS_APPLICATION_CREDENTIAL_SECRET)")

	fs.StringVar(&o.CACert, "os-cacert", os.Getenv("OS_CACERT"),
		"path to a custom CA bundle (env OS_CACERT)")
	fs.StringVar(&o.ClientCert, "os-cert", os.Getenv("OS_CERT"),
		"path to a client certificate for mutual TLS (env OS_CERT)")
	fs.StringVar(&o.ClientKey, "os-key", os.Getenv("OS_KEY"),
		"path to the client certificate key for mutual TLS (env OS_KEY)")
	fs.BoolVar(&o.Insecure, "insecure", envBool("OS_INSECURE"),
		"disable TLS certificate verification (env OS_INSECURE); logs a warning")

	fs.StringVar(&o.BaremetalAPIVersion, "os-baremetal-api-version", envOr("OS_BAREMETAL_API_VERSION", defaultBaremetalMicroversion),
		"baremetal (ironic) API microversion (env OS_BAREMETAL_API_VERSION)")
	fs.StringVar(&o.ComputeAPIVersion, "os-compute-api-version", envOr("OS_COMPUTE_API_VERSION", defaultComputeMicroversion),
		"compute (nova) API microversion (env OS_COMPUTE_API_VERSION)")
	fs.StringVar(&o.VolumeAPIVersion, "os-volume-api-version", envOr("OS_VOLUME_API_VERSION", defaultVolumeMicroversion),
		"volume (cinder) API microversion (env OS_VOLUME_API_VERSION)")

	fs.StringVar(&o.KeyVRMEndpoint, "keyvrm-endpoint", os.Getenv("OS_KEYVRM_ENDPOINT_OVERRIDE"),
		"override the KeyVRM endpoint instead of catalog discovery (env OS_KEYVRM_ENDPOINT_OVERRIDE)")

	fs.BoolVar(&o.Debug, "debug", envBool("OS_DEBUG"),
		"log HTTP requests and responses to stderr (tokens redacted)")

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

	fs.StringVar(&o.VaultAddr, "vault-addr", os.Getenv("VAULT_ADDR"),
		"Vault address for --creds-from-vault (env VAULT_ADDR)")
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
	fs.StringVar(&o.VaultKVMount, "vault-kv-mount", envOr("VAULT_KV_MOUNT", "secret_v2"),
		"Vault KV v2 mount for --creds-from-vault (env VAULT_KV_MOUNT)")
	fs.StringVar(&o.VaultKVPrefix, "vault-kv-prefix", os.Getenv("VAULT_KV_PREFIX"),
		"default path prefix prepended to a relative --creds-from-vault path (env VAULT_KV_PREFIX)")
	fs.StringVar(&o.VaultCACert, "vault-cacert", os.Getenv("VAULT_CACERT"),
		"path to a CA bundle for the Vault TLS endpoint (env VAULT_CACERT)")
	fs.BoolVar(&o.VaultInsecure, "vault-insecure", envBool("VAULT_SKIP_VERIFY"),
		"disable TLS verification for Vault (env VAULT_SKIP_VERIFY)")
}

// insecureExplicit reports whether --insecure or OS_INSECURE was explicitly
// provided, so we can avoid clobbering a clouds.yaml "verify" setting.
func (o *Options) insecureExplicit() bool {
	if o.fs != nil && o.fs.Changed("insecure") {
		return true
	}
	return os.Getenv("OS_INSECURE") != ""
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envBool(key string) bool {
	v := os.Getenv(key)
	if v == "" {
		return false
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		// Treat any non-empty, non-parseable value as truthy, matching the lax
		// behavior of most OS_* boolean toggles.
		return true
	}
	return b
}

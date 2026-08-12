// Package auth builds a single authenticated gophercloud ProviderClient per
// invocation and derives per-service clients from it. It implements the
// cross-cutting auth, TLS and microversion requirements shared by every koc
// command.
//
// Authentication precedence (highest first):
//
//  1. Explicitly given --os-* flags
//  2. --os-cloud / OS_CLOUD  → clouds.yaml (via gophercloud config/clouds.Parse)
//  3. OS_* environment variables
//  4. Application credentials (OS_APPLICATION_CREDENTIAL_ID / _SECRET),
//     which are honored through either of the two paths above.
//
// Naming a cloud selects it wholesale: because every auth flag defaults to its
// OS_* variable, a sourced openrc would otherwise override the named cloud
// field by field and silently send the command — credentials included — to the
// environment's cloud instead. Only flags the operator actually typed (and
// values from a --creds-from-vault openrc) outrank clouds.yaml.
//
// TLS is always wired explicitly into the provider so behavior matches OSC:
// custom CA bundle (OS_CACERT / --os-cacert), mutual TLS client cert+key
// (OS_CERT+OS_KEY / --os-cert+--os-key), hostname verification on by default
// with an explicit --insecure / OS_INSECURE opt-out, and a TLS 1.2 minimum.
package auth

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

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

// defaultHTTPTimeout is the default whole-exchange cap, and it is deliberately
// 0 (unbounded). The failure it would guard against — an endpoint that accepts
// the connection and then answers nothing — is already bounded by
// responseHeaderTimeout, which fires without also capping a legitimate transfer.
// A whole-exchange default would silently break the long ones (image save/create
// over a slow link), so a hard cap stays opt-in via --timeout.
const defaultHTTPTimeout = 0

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

	// SystemScope requests a system-scoped token. OSC spells this
	// --os-system-scope and only accepts the value "all"; it is mutually
	// exclusive with project and domain scope.
	SystemScope string

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
	PlacementAPIVersion string

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

	// Vault access, used by CredsFromVault and by the "koc vault kv" command
	// group. Names mirror the standard VAULT_* CLI.
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

	// Timeout caps a single HTTP request/response exchange on every client koc
	// builds (OpenStack, standalone ironic, Vault, Kubernetes). Zero disables the
	// cap. It is per request, not per command, so the --wait polling loops — which
	// issue discrete requests — are unaffected.
	Timeout time.Duration

	// Diagnostics.
	Debug bool

	// Timing prints the wall-clock duration of each HTTP round trip to stderr.
	// It is independent of Debug: timings without the full body dumps are what
	// answer "why is this slow".
	Timing bool

	// fs is retained so factory methods can distinguish an explicitly-set flag
	// from an env-derived default (notably for --insecure, and for keeping a
	// stray OS_* out of an explicitly named cloud — see Options.override).
	fs *pflag.FlagSet

	// forced names flags whose value came from a source that outranks
	// clouds.yaml even though pflag never saw them on the command line —
	// currently only the --creds-from-vault openrc.
	forced map[string]bool
}

// markForced records that flag's value was supplied by a source pflag cannot
// see but which still outranks clouds.yaml.
func (o *Options) markForced(flag string) {
	if o.forced == nil {
		o.forced = make(map[string]bool)
	}
	o.forced[flag] = true
}

// ComputeAPIVersionPinnable reports whether the compute microversion is still
// koc's negotiated default, so a command may lower it to the least version that
// answers the request. An operator who named a microversion gets exactly that
// one.
func (o *Options) ComputeAPIVersionPinnable() bool {
	if o.explicitlySet("os-compute-api-version") {
		return false
	}
	// AddFlags defaults the flag from OS_COMPUTE_API_VERSION, which pflag
	// cannot tell apart from koc's own default, so the variable is consulted
	// directly rather than through fs.Changed.
	return os.Getenv("OS_COMPUTE_API_VERSION") == "" &&
		o.ComputeAPIVersion == defaultComputeMicroversion
}

// explicitlySet reports whether the operator actually supplied this flag, as
// opposed to it sitting at the OS_*-derived default AddFlags installed.
func (o *Options) explicitlySet(flag string) bool {
	if o.forced[flag] {
		return true
	}
	if o.fs == nil {
		// Built programmatically rather than from a command line: every
		// populated field was set deliberately, so there is no env-derived
		// default to tell it apart from.
		return true
	}
	return o.fs.Changed(flag)
}

// override returns v only when it may legitimately be layered over the auth
// options the base path produced.
//
// On the env path o's fields *are* the configuration, so everything applies. On
// the clouds.yaml path they are not: AddFlags defaults every auth flag to its
// OS_* variable, so a sourced openrc leaves them all populated even when the
// operator typed nothing but --os-cloud. Layering those over the named cloud
// silently redirects the command — credentials included — at whatever the
// environment happened to hold, so there only a value the operator actually
// supplied wins.
func (o *Options) override(flag, v string) string {
	if v == "" || o.Cloud == "" || o.explicitlySet(flag) {
		return v
	}
	return ""
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
	fs.StringVar(&o.SystemScope, "os-system-scope", os.Getenv("OS_SYSTEM_SCOPE"),
		`request a system-scoped token; the only value Keystone defines is "all" (env OS_SYSTEM_SCOPE)`)

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
	fs.BoolVar(&o.Insecure, "insecure", EnvBool("OS_INSECURE"),
		"disable TLS certificate verification (env OS_INSECURE); logs a warning")

	// UNVERIFIED against KeyStack: python-openstackclient has no --timeout, it is
	// a keystoneauth session setting. koc exposes it because a zero timeout is not
	// a safe default for a CLI.
	fs.DurationVar(&o.Timeout, "timeout", envDuration("OS_TIMEOUT", defaultHTTPTimeout),
		"whole-exchange HTTP timeout, e.g. 90s; 0 (the default) leaves transfers unbounded — a wedged endpoint is caught by the 60s response-header timeout either way (env OS_TIMEOUT)")

	fs.StringVar(&o.BaremetalAPIVersion, "os-baremetal-api-version", envOr("OS_BAREMETAL_API_VERSION", defaultBaremetalMicroversion),
		"baremetal (ironic) API microversion (env OS_BAREMETAL_API_VERSION)")
	fs.StringVar(&o.ComputeAPIVersion, "os-compute-api-version", envOr("OS_COMPUTE_API_VERSION", defaultComputeMicroversion),
		"compute (nova) API microversion (env OS_COMPUTE_API_VERSION)")
	fs.StringVar(&o.VolumeAPIVersion, "os-volume-api-version", envOr("OS_VOLUME_API_VERSION", defaultVolumeMicroversion),
		"volume (cinder) API microversion (env OS_VOLUME_API_VERSION)")
	fs.StringVar(&o.PlacementAPIVersion, "os-placement-api-version", envOr("OS_PLACEMENT_API_VERSION", defaultPlacementMicroversion),
		"placement API microversion (env OS_PLACEMENT_API_VERSION)")

	fs.StringVar(&o.KeyVRMEndpoint, "keyvrm-endpoint", os.Getenv("OS_KEYVRM_ENDPOINT_OVERRIDE"),
		"override the KeyVRM endpoint instead of catalog discovery (env OS_KEYVRM_ENDPOINT_OVERRIDE)")

	fs.BoolVar(&o.Debug, "debug", EnvBool("OS_DEBUG"),
		"log HTTP requests and responses to stderr (tokens redacted)")
	fs.BoolVar(&o.Timing, "timing", false,
		"print the wall-clock duration of each API call to stderr")

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
	fs.StringVar(&o.VaultKVMount, "vault-kv-mount", envOr("VAULT_KV_MOUNT", "secret_v2"),
		"Vault KV v2 mount (env VAULT_KV_MOUNT)")
	fs.StringVar(&o.VaultKVPrefix, "vault-kv-prefix", os.Getenv("VAULT_KV_PREFIX"),
		"default path prefix prepended to a relative Vault KV path (env VAULT_KV_PREFIX)")
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

// EnvBool parses a boolean environment variable used as a flag default. It is
// the single implementation for the whole binary — internal/cli/vault calls it
// too — so a given spelling means the same thing everywhere.
//
// It fails CLOSED. These variables gate security behavior (OS_INSECURE,
// VAULT_SKIP_VERIFY, VAULT_SRC_SKIP_VERIFY), so a value koc does not understand
// must never be read as "yes, turn certificate verification off".
// strconv.ParseBool alone is not enough: it rejects the spellings operators
// actually write (no, off, disabled), and treating an unparseable value as
// truthy — as koc used to — made OS_INSECURE=no *disable* TLS verification.
//
// An unrecognised value is surfaced twice: a warning on stderr immediately
// (flag defaults are computed before any command runs, so this is the only
// place that can still name the variable in context) and a hard error from
// Authenticate / VaultConfig via envError, so it cannot pass unnoticed.
func EnvBool(key string) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return false
	}
	b, err := parseBoolLax(v)
	if err != nil {
		recordBadEnv(key, v, "expected a boolean: true/false, yes/no, on/off, 1/0")
		return false
	}
	return b
}

// parseBoolLax accepts the shell spellings of a boolean in addition to the ones
// strconv.ParseBool knows, and rejects everything else.
func parseBoolLax(v string) (bool, error) {
	switch strings.ToLower(v) {
	case "1", "t", "true", "y", "yes", "on", "enable", "enabled":
		return true, nil
	case "0", "f", "false", "n", "no", "off", "disable", "disabled":
		return false, nil
	}
	return false, fmt.Errorf("not a boolean: %q", v)
}

// envDuration parses a duration environment variable, accepting both a Go
// duration ("90s", "2m") and a bare number of seconds ("90"), and falls back to
// def — recording the same visible error as EnvBool — when it is neither.
func envDuration(key string, def time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	if d, err := time.ParseDuration(v); err == nil && d >= 0 {
		return d
	}
	if secs, err := strconv.ParseFloat(v, 64); err == nil && secs >= 0 {
		return time.Duration(secs * float64(time.Second))
	}
	recordBadEnv(key, v, "expected a duration (90s, 2m) or a number of seconds")
	return def
}

// badEnv collects the environment variables whose value koc could not parse, so
// the failure is reported as an error by the first command that authenticates
// instead of only as a warning nobody scrolled back to.
var badEnv struct {
	mu   sync.Mutex
	seen map[string]bool
	msgs []string
}

func recordBadEnv(key, val, hint string) {
	msg := fmt.Sprintf("%s=%q: %s", key, val, hint)

	badEnv.mu.Lock()
	defer badEnv.mu.Unlock()
	if badEnv.seen[msg] {
		return
	}
	if badEnv.seen == nil {
		badEnv.seen = map[string]bool{}
	}
	badEnv.seen[msg] = true
	badEnv.msgs = append(badEnv.msgs, msg)
	fmt.Fprintf(os.Stderr, "WARNING: %s; ignoring it\n", msg)
}

// envError reports the unparseable environment variables recorded so far. It is
// checked before any credential is used: running on a safe default the operator
// did not ask for is a silent surprise, and for OS_INSECURE that surprise used
// to go the unsafe way.
func envError() error {
	badEnv.mu.Lock()
	defer badEnv.mu.Unlock()
	if len(badEnv.msgs) == 0 {
		return nil
	}
	return fmt.Errorf("unusable environment variable(s): %s", strings.Join(badEnv.msgs, "; "))
}

// resetBadEnv clears the recorded environment errors. Tests only: the recorder
// is process-global because flag defaults are.
func resetBadEnv() {
	badEnv.mu.Lock()
	defer badEnv.mu.Unlock()
	badEnv.seen, badEnv.msgs = nil, nil
}

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
// The password has two further sources, both in password.go and neither a
// separate precedence tier: --os-password-stdin (koc-native) reads it from
// standard input instead of a flag or the environment, and a run that reaches
// authentication without one is prompted on the terminal, as osc-lib does.
//
// As in python-openstackclient, the OS_* variables outrank a named cloud field
// by field: every auth flag defaults to its variable, and the flags are layered
// over the clouds.yaml entry (openstacksdk's get_one does the same with
// osc-lib's argparse values). So a cloud that stores no password takes
// OS_PASSWORD, and an exported OS_REGION_NAME picks the region.
//
// TLS is always wired explicitly into the provider so behavior matches OSC:
// custom CA bundle (OS_CACERT / --os-cacert), mutual TLS client cert+key
// (OS_CERT+OS_KEY / --os-cert+--os-key), hostname verification on by default
// with an explicit --insecure / OS_INSECURE opt-out, and a TLS 1.2 minimum.
package auth

import (
	"context"
	"fmt"
	"io"
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
	AuthURL       string
	Username      string
	UserID        string
	Password      string
	PasswordStdin bool
	// Token is a pre-issued Keystone token (--os-token / OS_TOKEN), used by the
	// token auth type instead of a password.
	Token string
	// AuthType is osc-lib's --os-auth-type / OS_AUTH_TYPE. Empty selects one
	// from what was supplied, as osc-lib does: a username means password, a
	// token alone means token. See applyAuthType.
	AuthType string
	// Passcode is a TOTP passcode, for the v3totp and v3multifactor auth types
	// (--os-passcode / OS_PASSCODE).
	Passcode string
	// TrustID requests a trust-scoped token (--os-trust-id / OS_TRUST_ID).
	TrustID string
	// Endpoint is the service URL of the Keystone-less auth types — none /
	// noauth, admin_token / token_endpoint and http_basic — which have no
	// catalog to look one up in (--os-endpoint / OS_ENDPOINT).
	Endpoint string

	// tenantName / tenantID back the hidden v2-era --os-tenant-name /
	// --os-tenant-id spellings osc-lib still accepts; applyTenantAliases folds
	// them into ProjectName / ProjectID.
	tenantName, tenantID string

	// authKind is the auth type resolveAuth settled on (canonicalAuthType's
	// value; empty when nothing chose one).
	authKind string
	// endpointOverrides maps a service type to the URL that replaces its
	// catalog entry: OS_<TYPE>_ENDPOINT_OVERRIDE and clouds.yaml
	// <type>_endpoint_override. See endpoints.go.
	endpointOverrides map[string]string

	ProjectName       string
	ProjectID         string
	ProjectDomainName string
	UserDomainName    string
	DomainName        string
	// The same three domains named by ID, OSC's --os-*-domain-id. Each pair is
	// one domain: an ID wins over a name for the same domain unless only the
	// name was given explicitly (see domainOption).
	ProjectDomainID string
	UserDomainID    string
	DomainID        string
	// DefaultDomain qualifies a username or project name no domain flag,
	// variable or clouds.yaml key qualifies: osc-lib's --os-default-domain.
	DefaultDomain string

	RegionName string
	Interface  string

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

	// DebugFormat is the layout of the --debug dump: DebugFormatHTTP (the raw
	// messages) or DebugFormatCurl (keystoneauth's `REQ: curl …` lines, as
	// `openstack --debug` prints them).
	DebugFormat string

	// Timing prints the wall-clock duration of each HTTP round trip to stderr.
	// It is independent of Debug: timings without the full body dumps are what
	// answer "why is this slow".
	Timing bool

	// fs is retained so factory methods can distinguish an explicitly-set flag
	// from an env-derived default (notably for --insecure, and for a typed
	// domain name beating an exported domain ID — see domainOption).
	fs *pflag.FlagSet

	// forced names flags whose value came from a source as deliberate as the
	// command line even though pflag never saw it — the --creds-from-vault
	// openrc, and a password read from stdin or the prompt.
	forced map[string]bool

	// authenticate overrides Authenticate in tests so a command's RunE can be
	// executed end-to-end against a mock endpoint. Nil in every non-test build;
	// see testhooks.go for why the hook is here rather than in an
	// export_test.go.
	authenticate func(context.Context) (*Client, error)

	// authDone/authClient/authErr memoize the one authentication an invocation
	// performs. See services.go authenticated for why the result — failure
	// included — is cached rather than recomputed per service client.
	//
	// Deliberately a plain flag rather than a sync.Once: a Once would make
	// Options uncopyable for every holder of one, and nothing needs it.
	// Authentication happens on the command's own goroutine, at the top of
	// RunE, before any fan-out; the two commands that do fan out
	// (`hypervisor list --placement`, `--check-actual`) hand their workers an
	// already-built service client. -race in CI is the check on that staying
	// true.
	authDone   bool
	authClient *Client
	authErr    error

	// passwordStdinSrc and promptPassword seam the two terminal password
	// sources (see password.go). Nil in every non-test build, meaning the real
	// os.Stdin and an unechoed read from it.
	passwordStdinSrc io.Reader
	promptPassword   func(io.Writer) (string, error)
}

// markForced records that flag's value was supplied by a source pflag cannot
// see but which counts as explicitly given.
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
	// A bare major version ("2", however it was supplied) names no
	// microversion and resolves to the default — see apiMicroversion.
	if isMajorOnly(o.ComputeAPIVersion, computeAPIMajor) {
		return true
	}
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
	fs.StringVar(&o.Password, flagOSPassword, os.Getenv("OS_PASSWORD"),
		"password (env OS_PASSWORD)")
	// UNVERIFIED against KeyStack: koc-native, python-openstackclient has no
	// equivalent (it only prompts). Deliberately flag-only — reading a secret
	// from stdin is a thing a run must opt into visibly, not something an
	// exported variable can turn on under a command that wanted stdin for
	// something else.
	fs.BoolVar(&o.PasswordStdin, flagOSPasswordStdin, false,
		"read the password from standard input instead of --os-password, which is visible in ps and the shell history")
	fs.StringVar(&o.Token, flagOSToken, os.Getenv("OS_TOKEN"),
		"pre-issued Keystone token, for the token auth type (env OS_TOKEN)")
	fs.StringVar(&o.AuthType, flagOSAuthType, os.Getenv("OS_AUTH_TYPE"),
		"authentication type: password, token, v3applicationcredential, v3totp, v3multifactor, none, admin_token or http_basic (and their v3 spellings); default: chosen from --os-username / --os-token (env OS_AUTH_TYPE)")
	fs.StringVar(&o.Passcode, "os-passcode", os.Getenv("OS_PASSCODE"),
		"TOTP passcode, for the v3totp and v3multifactor auth types (env OS_PASSCODE)")
	fs.StringVar(&o.TrustID, "os-trust-id", os.Getenv("OS_TRUST_ID"),
		"request a trust-scoped token for this trust (env OS_TRUST_ID)")
	fs.StringVar(&o.Endpoint, "os-endpoint", os.Getenv("OS_ENDPOINT"),
		"service URL for the auth types that use no Keystone: none, admin_token, http_basic (env OS_ENDPOINT)")
	// OS_TENANT_NAME / OS_TENANT_ID are the identity-v2 spellings osc-lib still
	// reads as fallbacks (osc_lib/api/auth.py build_auth_plugins_option_parser);
	// old openrc files export only those.
	fs.StringVar(&o.ProjectName, flagOSProjectName, envOr("OS_PROJECT_NAME", os.Getenv("OS_TENANT_NAME")),
		"project name (env OS_PROJECT_NAME, or OS_TENANT_NAME)")
	fs.StringVar(&o.ProjectID, flagOSProjectID, envOr("OS_PROJECT_ID", os.Getenv("OS_TENANT_ID")),
		"project ID (env OS_PROJECT_ID, or OS_TENANT_ID)")
	fs.StringVar(&o.tenantName, flagOSTenantName, "", "deprecated spelling of --os-project-name")
	fs.StringVar(&o.tenantID, flagOSTenantID, "", "deprecated spelling of --os-project-id")
	_ = fs.MarkHidden(flagOSTenantName)
	_ = fs.MarkHidden(flagOSTenantID)
	fs.StringVar(&o.ProjectDomainName, "os-project-domain-name", os.Getenv("OS_PROJECT_DOMAIN_NAME"),
		"project domain name (env OS_PROJECT_DOMAIN_NAME)")
	fs.StringVar(&o.UserDomainName, "os-user-domain-name", os.Getenv("OS_USER_DOMAIN_NAME"),
		"user domain name (env OS_USER_DOMAIN_NAME)")
	fs.StringVar(&o.DomainName, "os-domain-name", os.Getenv("OS_DOMAIN_NAME"),
		"domain name for domain-scoped tokens (env OS_DOMAIN_NAME)")
	// The *-id spellings upstream OSC (keystoneauth) takes alongside the names;
	// devstack's openrc, for one, exports only OS_{USER,PROJECT}_DOMAIN_ID.
	fs.StringVar(&o.ProjectDomainID, "os-project-domain-id", os.Getenv("OS_PROJECT_DOMAIN_ID"),
		"project domain ID (env OS_PROJECT_DOMAIN_ID)")
	fs.StringVar(&o.UserDomainID, "os-user-domain-id", os.Getenv("OS_USER_DOMAIN_ID"),
		"user domain ID (env OS_USER_DOMAIN_ID)")
	fs.StringVar(&o.DomainID, "os-domain-id", os.Getenv("OS_DOMAIN_ID"),
		"domain ID for domain-scoped tokens (env OS_DOMAIN_ID)")
	fs.StringVar(&o.DefaultDomain, "os-default-domain", envOr("OS_DEFAULT_DOMAIN", "default"),
		"domain ID for a username or project name given without one (env OS_DEFAULT_DOMAIN)")
	fs.StringVar(&o.RegionName, "os-region-name", os.Getenv("OS_REGION_NAME"),
		"region name (env OS_REGION_NAME)")
	// OS_ENDPOINT_TYPE is the pre-interface spelling, which openstacksdk still
	// maps onto interface (loader.py _fix_backwards_interface); its values are
	// often publicURL / internalURL, which normalizeInterface accepts.
	fs.StringVar(&o.Interface, "os-interface", envOr("OS_INTERFACE", os.Getenv("OS_ENDPOINT_TYPE")),
		"endpoint interface: public, internal or admin (env OS_INTERFACE, or OS_ENDPOINT_TYPE)")
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
		"baremetal (ironic) API microversion; a bare major version such as 1 means latest (env OS_BAREMETAL_API_VERSION)")
	fs.StringVar(&o.ComputeAPIVersion, "os-compute-api-version", envOr("OS_COMPUTE_API_VERSION", defaultComputeMicroversion),
		"compute (nova) API microversion; a bare major version such as 2 means latest (env OS_COMPUTE_API_VERSION)")
	fs.StringVar(&o.VolumeAPIVersion, "os-volume-api-version", envOr("OS_VOLUME_API_VERSION", defaultVolumeMicroversion),
		"volume (cinder) API microversion; a bare major version such as 3 means latest (env OS_VOLUME_API_VERSION)")
	fs.StringVar(&o.PlacementAPIVersion, "os-placement-api-version", envOr("OS_PLACEMENT_API_VERSION", defaultPlacementMicroversion),
		"placement API microversion; a bare major version such as 1 means latest (env OS_PLACEMENT_API_VERSION)")

	fs.StringVar(&o.KeyVRMEndpoint, "keyvrm-endpoint", os.Getenv("OS_KEYVRM_ENDPOINT_OVERRIDE"),
		"override the KeyVRM endpoint instead of catalog discovery (env OS_KEYVRM_ENDPOINT_OVERRIDE)")

	fs.BoolVar(&o.Debug, "debug", EnvBool("OS_DEBUG"),
		"log HTTP requests and responses to stderr (tokens redacted)")
	// UNVERIFIED against KeyStack: python-openstackclient has no --debug-format;
	// its --debug always prints keystoneauth's curl layout, which is what
	// --debug-format curl reproduces.
	o.DebugFormat = envDebugFormat("KOC_DEBUG_FORMAT")
	fs.Var((*debugFormatValue)(&o.DebugFormat), "debug-format",
		"layout of the --debug dump: http (raw HTTP messages) or curl (curl commands, like openstack --debug) (env KOC_DEBUG_FORMAT)")
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

// debugFormatValue is --debug-format: a string that accepts only the two
// layouts, so a typo fails at parse time instead of silently dumping http.
type debugFormatValue string

func (v *debugFormatValue) String() string { return string(*v) }

func (v *debugFormatValue) Set(s string) error {
	switch s {
	case DebugFormatHTTP, DebugFormatCurl:
		*v = debugFormatValue(s)
		return nil
	}
	return fmt.Errorf("must be %s or %s", DebugFormatHTTP, DebugFormatCurl)
}

func (v *debugFormatValue) Type() string { return "format" }

// envDebugFormat reads KOC_DEBUG_FORMAT, defaulting to http; an unknown value
// is recorded like any other unusable variable.
func envDebugFormat(key string) string {
	v := os.Getenv(key)
	switch v {
	case "":
		return DebugFormatHTTP
	case DebugFormatHTTP, DebugFormatCurl:
		return v
	}
	recordBadEnv(key, v, "must be "+DebugFormatHTTP+" or "+DebugFormatCurl)
	return DebugFormatHTTP
}

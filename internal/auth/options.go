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

	// Per-service microversions.
	BaremetalAPIVersion string
	ComputeAPIVersion   string
	VolumeAPIVersion    string

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

	fs.BoolVar(&o.Debug, "debug", envBool("OS_DEBUG"),
		"log HTTP requests and responses to stderr (tokens redacted)")
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

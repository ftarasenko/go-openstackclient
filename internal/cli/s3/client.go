// Package s3cli implements the koc-specific "koc s3 ..." command group: list
// buckets and objects, and move files in and out of an S3-compatible store.
//
// It has no python-openstackclient equivalent. S3 is not an OpenStack service —
// upstream's object-store commands speak Swift, which KeyStack does not deploy —
// and like "koc vault kv" this group deliberately never authenticates against
// Keystone: S3 credentials alone are used.
//
// The store koc is aimed at is the LCM cluster's Garage, which holds GitLab's
// object storage and the MariaDB backups the "backup-db" scheduled pipeline
// uploads. That pipeline drives s3cmd with --host/--host-bucket set to the same
// hostname, i.e. path-style addressing, so path-style is this group's default.
package s3cli

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/pflag"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/kube"
	"github.com/ftarasenko/go-openstackclient/internal/s3"
)

// defaultCredsSecret is the Secret --s3-creds-from-ns reads when only a
// namespace is given. It is GitLab's object-storage connection secret, the one
// place on an LCM cluster where an S3 key is stored as a k8s Secret at all (see
// the group's Long help for where the other keys live).
const defaultCredsSecret = "gitlab-object-storage" //nolint:gosec // G101: name of a k8s Secret object, not a credential value

// connFlags holds the "koc s3" connection and credential flags. They are
// persistent flags on the group rather than global ones: every other koc command
// needs Keystone, none of them needs these, and koc --help is long enough.
type connFlags struct {
	endpoint    string
	accessKey   string
	secretKey   string
	region      string
	cacert      string
	credsFromNS string
	insecure    bool
	noPathStyle bool

	fs *pflag.FlagSet // to tell "unset" from "explicitly empty"
}

// The env names are three families on purpose: AWS_* so an existing aws/boto
// environment works unchanged, S3_* as the neutral spelling, and the lower-case
// s3_* the KeyStack installer writes as GitLab group CI/CD variables
// (s3_host/s3_access_key/s3_secret_key/s3_region — see installer/upload.sh), so
// a pipeline job can call koc with no S3 flags at all.
func (f *connFlags) addTo(fs *pflag.FlagSet) {
	f.fs = fs
	fs.StringVar(&f.endpoint, "s3-endpoint", envOr("AWS_ENDPOINT_URL", "S3_ENDPOINT", "s3_host"),
		"S3 endpoint URL or host (env AWS_ENDPOINT_URL, S3_ENDPOINT, s3_host)")
	fs.StringVar(&f.accessKey, "s3-access-key", envOr("AWS_ACCESS_KEY_ID", "S3_ACCESS_KEY", "s3_access_key"),
		"S3 access key ID (env AWS_ACCESS_KEY_ID, S3_ACCESS_KEY, s3_access_key)")
	fs.StringVar(&f.secretKey, "s3-secret-key", envOr("AWS_SECRET_ACCESS_KEY", "S3_SECRET_KEY", "s3_secret_key"),
		"S3 secret access key (env AWS_SECRET_ACCESS_KEY, S3_SECRET_KEY, s3_secret_key); prefer the environment over the flag, an argument is visible in the process list")
	fs.StringVar(&f.region, "s3-region", envOr("AWS_REGION", "AWS_DEFAULT_REGION", "S3_REGION", "s3_region"),
		"S3 signing region (env AWS_REGION, AWS_DEFAULT_REGION, S3_REGION, s3_region; default "+s3.DefaultRegion+")")
	fs.StringVar(&f.cacert, "s3-cacert", envOr("AWS_CA_BUNDLE", "S3_CACERT"),
		"CA bundle for the S3 endpoint's TLS certificate (env AWS_CA_BUNDLE, S3_CACERT)")
	fs.StringVar(&f.credsFromNS, "s3-creds-from-ns", os.Getenv("KOC_S3_CREDS_FROM_NS"),
		"read S3 credentials from a Kubernetes Secret, as <namespace>[/<secret>][:<key>] (env KOC_S3_CREDS_FROM_NS)")
	fs.BoolVar(&f.insecure, "insecure-s3", auth.EnvBool("S3_SKIP_VERIFY"),
		"disable TLS verification for the S3 endpoint (env S3_SKIP_VERIFY)")
	fs.BoolVar(&f.noPathStyle, "no-path-style", false,
		"address buckets as <bucket>.<endpoint> instead of <endpoint>/<bucket>; needs a wildcard DNS record")
}

// client builds the S3 client. --timeout, --debug, --insecure, --kubeconfig and
// --kube-context come from the global flags; everything else is this group's.
func (f *connFlags) client(ctx context.Context, a *auth.Options) (*s3.Client, error) {
	cfg := s3.Config{
		Endpoint:  f.endpoint,
		Region:    f.region,
		AccessKey: f.accessKey,
		SecretKey: f.secretKey,
		PathStyle: !f.noPathStyle,
		// The global --insecure is honoured too: a user who typed it once meant
		// it for the endpoint they are talking to.
		Insecure: f.insecure || a.Insecure,
		Timeout:  a.Timeout,
		Debug:    a.Debug,
	}

	if f.credsFromNS != "" {
		if err := f.applyClusterCreds(ctx, a, &cfg); err != nil {
			return nil, err
		}
	}
	if f.cacert != "" {
		b, err := os.ReadFile(f.cacert)
		if err != nil {
			return nil, fmt.Errorf("reading --s3-cacert %q: %w", f.cacert, err)
		}
		cfg.CACertPEM = b
	}
	return s3.New(cfg)
}

// applyClusterCreds fills the config from a Kubernetes Secret. An explicitly
// given flag or environment variable always wins, so the Secret supplies only
// what the operator did not.
func (f *connFlags) applyClusterCreds(ctx context.Context, a *auth.Options, cfg *s3.Config) error {
	namespace, name, key := parseCredsRef(f.credsFromNS)

	kc, err := kube.Load(kube.Options{Kubeconfig: a.Kubeconfig, Context: a.KubeContext, Debug: a.Debug, Timeout: a.Timeout})
	if err != nil {
		return fmt.Errorf("--s3-creds-from-ns: %w", err)
	}
	data, err := kc.GetSecret(ctx, namespace, name)
	if err != nil {
		return fmt.Errorf("--s3-creds-from-ns %s/%s: %w", namespace, name, err)
	}
	creds, err := credsFromSecret(data, key)
	if err != nil {
		return fmt.Errorf("--s3-creds-from-ns %s/%s: %w", namespace, name, err)
	}

	f.fill(&cfg.AccessKey, creds.accessKey, "s3-access-key", "AWS_ACCESS_KEY_ID", "S3_ACCESS_KEY", "s3_access_key")
	f.fill(&cfg.SecretKey, creds.secretKey, "s3-secret-key", "AWS_SECRET_ACCESS_KEY", "S3_SECRET_KEY", "s3_secret_key")
	f.fill(&cfg.Endpoint, creds.endpoint, "s3-endpoint", "AWS_ENDPOINT_URL", "S3_ENDPOINT", "s3_host")
	f.fill(&cfg.Region, creds.region, "s3-region", "AWS_REGION", "AWS_DEFAULT_REGION", "S3_REGION", "s3_region")
	if creds.pathStyle != nil && (f.fs == nil || !f.fs.Changed("no-path-style")) {
		cfg.PathStyle = *creds.pathStyle
	}

	return nil
}

// fill sets dst from the cluster-supplied value unless the operator asked for
// one explicitly, on the command line or through any of the env names.
func (f *connFlags) fill(dst *string, val, flag string, envs ...string) {
	if val == "" {
		return
	}
	if f.fs != nil && f.fs.Changed(flag) {
		return
	}
	for _, e := range envs {
		if os.Getenv(e) != "" {
			return
		}
	}
	*dst = val
}

// parseCredsRef splits "<namespace>[/<secret>][:<key>]". The secret name
// defaults to defaultCredsSecret and the data key to "every key in the Secret".
func parseCredsRef(ref string) (namespace, name, key string) {
	if i := strings.LastIndex(ref, ":"); i >= 0 {
		ref, key = ref[:i], ref[i+1:]
	}
	namespace, name = ref, defaultCredsSecret
	if i := strings.Index(ref, "/"); i >= 0 {
		namespace, name = ref[:i], ref[i+1:]
	}
	return namespace, name, key
}

// secretCreds is what a Kubernetes Secret can contribute.
type secretCreds struct {
	accessKey string
	secretKey string
	endpoint  string
	region    string
	pathStyle *bool
}

// Alias sets for the field names S3 credentials appear under. They cover the
// three shapes actually found in the wild: a Secret with one key per value
// (Rook/RGW, hand-made ones), the AWS environment spelling, and a Secret holding
// a whole connection config as one value — GitLab's "config" key, an rclone.conf
// or an .s3cfg. All comparisons are lower-case.
var (
	accessKeyAliases = []string{"access_key", "access-key", "accesskey", "access_key_id", "aws_access_key_id", "s3_access_key"}
	secretKeyAliases = []string{"secret_key", "secret-key", "secretkey", "secret_access_key", "aws_secret_access_key", "s3_secret_key"}
	endpointAliases  = []string{"endpoint", "endpoint_url", "aws_endpoint_url", "s3_endpoint", "s3_host", "host_base"}
	regionAliases    = []string{"region", "aws_region", "aws_default_region", "s3_region", "bucket_location"}
	pathStyleAliases = []string{"path_style", "path-style", "pathstyle", "force_path_style", "use_path_style"}
)

// credsFromSecret pulls credentials out of a Secret's data. Every value is
// treated both as a discrete field and as a possible config blob, which is why
// one function handles all three Secret shapes.
func credsFromSecret(data map[string][]byte, wantKey string) (secretCreds, error) {
	if wantKey != "" {
		if _, ok := data[wantKey]; !ok {
			return secretCreds{}, fmt.Errorf("no key %q in secret", wantKey)
		}
	}

	flat := map[string]string{}
	add := func(k, v string) {
		k = strings.ToLower(strings.TrimSpace(k))
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		if k != "" && v != "" && flat[k] == "" {
			flat[k] = v
		}
	}

	// Sorted so two keys offering the same alias resolve the same way every run.
	names := make([]string, 0, len(data))
	for k := range data {
		names = append(names, k)
	}
	sort.Strings(names)

	for _, k := range names {
		if wantKey != "" && k != wantKey {
			continue
		}
		val := string(data[k])
		add(k, val)
		scanConfigLines(val, add)
	}

	creds := secretCreds{
		accessKey: pick(flat, accessKeyAliases),
		secretKey: pick(flat, secretKeyAliases),
		endpoint:  pick(flat, endpointAliases),
		region:    pick(flat, regionAliases),
	}
	if v := pick(flat, pathStyleAliases); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			creds.pathStyle = &b
		}
	}
	if creds.accessKey == "" || creds.secretKey == "" {
		return secretCreds{}, fmt.Errorf("no S3 access/secret key found (looked for %s and %s, in the Secret's keys and in any config value it holds)",
			strings.Join(accessKeyAliases, "/"), strings.Join(secretKeyAliases, "/"))
	}
	return creds, nil
}

// scanConfigLines reads "key: value", "key = value" and "key=value" lines out of
// a config blob. A value that is not a config — an access key on its own — has
// no separator and contributes nothing.
func scanConfigLines(blob string, add func(k, v string)) {
	for _, line := range strings.Split(blob, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "- "))
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "[") {
			continue
		}
		// The earliest of ":" and "=" is the separator, so an endpoint URL's own
		// "https://host:443" colon cannot be mistaken for one.
		sep := -1
		for _, c := range []string{":", "="} {
			if i := strings.Index(line, c); i >= 0 && (sep < 0 || i < sep) {
				sep = i
			}
		}
		if sep <= 0 {
			continue
		}
		add(line[:sep], line[sep+1:])
	}
}

// pick returns the first alias present in flat.
func pick(flat map[string]string, aliases []string) string {
	for _, a := range aliases {
		if v := flat[a]; v != "" {
			return v
		}
	}
	return ""
}

// parseRef splits an object reference: "bucket", "bucket/key" or
// "s3://bucket/key". The s3:// form is accepted because it is what s3cmd and the
// aws CLI take, and what the backup pipeline's own commands are written with.
func parseRef(ref string) (bucket, key string, err error) {
	trimmed := strings.TrimPrefix(strings.TrimPrefix(ref, "s3://"), "/")
	bucket, key = trimmed, ""
	if i := strings.Index(trimmed, "/"); i >= 0 {
		bucket, key = trimmed[:i], trimmed[i+1:]
	}
	if bucket == "" {
		return "", "", fmt.Errorf("%q names no bucket: expected <bucket>/<key> or s3://<bucket>/<key>", ref)
	}
	return bucket, key, nil
}

// parseObjectRef is parseRef where the key is mandatory.
func parseObjectRef(ref string) (bucket, key string, err error) {
	bucket, key, err = parseRef(ref)
	if err != nil {
		return "", "", err
	}
	if key == "" {
		return "", "", fmt.Errorf("%q names no object: expected <bucket>/<key>", ref)
	}
	return bucket, key, nil
}

// envOr returns the first non-empty environment variable from names.
func envOr(names ...string) string {
	for _, n := range names {
		if v := os.Getenv(n); v != "" {
			return v
		}
	}
	return ""
}

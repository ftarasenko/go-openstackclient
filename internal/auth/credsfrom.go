package auth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"gopkg.in/yaml.v3"

	"github.com/ftarasenko/go-openstackclient/internal/kube"
	"github.com/ftarasenko/go-openstackclient/internal/vault"
)

// LCM deployment conventions for Vault connection auto-discovery: the operator's
// lcm-config ConfigMap and the AppRole secret-id Secret. These let
// --creds-from-vault run with no --vault-* flags on a cluster node.
//
// These names are a deliberate, reviewed exception to the "no private data in a
// public repo" rule (AGENTS.md): they are load-bearing code, not examples — the
// discovery cannot find anything without them — and they name generic Kubernetes
// objects of the LCM operator, no cloud, host, region or tenant. Do not add
// anything cloud-specific here.
const (
	lcmConfigNamespace    = "k0s-system"
	lcmConfigName         = "lcm-config"
	lcmConfigKey          = "lcm-config.yaml"
	vaultApproleSecretNS  = "cert-manager"
	vaultApproleSecret    = "vault-approle" //nolint:gosec // G101: name of a k8s Secret object, not a credential value
	vaultApproleSecretKey = "secret-id"
)

// defaultIronicAPIPort is used when a standalone Ironic CR omits an explicit API
// port (the ironic-standalone-operator default).
const defaultIronicAPIPort = 6385

// ironicCreds holds everything needed to build a standalone, basic-auth Ironic
// service client from a Kubernetes-hosted metal3 ironic-standalone-operator
// deployment. It carries no Keystone token — the API is fronted by HTTP basic
// auth over TLS, not a service catalog.
type ironicCreds struct {
	endpoint   string // e.g. "https://10.0.0.1:6385/" (trailing slash)
	username   string
	password   string
	caPEM      []byte // CA trusted for the endpoint (empty → system roots)
	serverName string // cert SAN to verify while dialing by IP
	insecure   bool
	debug      bool
	timing     bool
	timeout    time.Duration
}

// loadIronicCreds reads the Ironic instance in o.CredsFromNS and its API secret
// over the Kubernetes API, resolving the live credentials secret from the CR's
// spec.apiCredentialsName (never guessing among rotated secrets).
func (o *Options) loadIronicCreds(ctx context.Context) (*ironicCreds, error) {
	kc, err := kube.Load(kube.Options{Kubeconfig: o.Kubeconfig, Context: o.KubeContext, Debug: o.Debug, Timeout: o.Timeout})
	if err != nil {
		return nil, err
	}
	ns := o.CredsFromNS

	api, err := kc.GetIronic(ctx, ns)
	if err != nil {
		return nil, err
	}
	if api.IPAddress == "" {
		return nil, fmt.Errorf("ironic in namespace %q has no spec.networking.ipAddress to reach the API", ns)
	}
	port := api.APIPort
	if port == 0 {
		port = defaultIronicAPIPort
	}

	sec, err := kc.GetSecret(ctx, ns, api.APICredentialsName)
	if err != nil {
		return nil, err
	}
	user, pass := string(sec["username"]), string(sec["password"])
	if user == "" || pass == "" {
		return nil, fmt.Errorf("secret %s/%s is missing username/password", ns, api.APICredentialsName)
	}

	ic := &ironicCreds{
		username: user,
		password: pass,
		insecure: o.Insecure,
		debug:    o.Debug,
		timing:   o.Timing,
		timeout:  o.Timeout,
	}

	scheme := "https"
	if api.TLSCertificateName != "" {
		tlsSec, err := kc.GetSecret(ctx, ns, api.TLSCertificateName)
		if err != nil {
			return nil, err
		}
		ic.caPEM = tlsSec["ca.crt"]
		// Verify against the cert's DNS SAN while dialing the VIP directly, so it
		// works without depending on cluster/lab DNS resolving the FQDN.
		ic.serverName = firstCertDNSName(tlsSec["tls.crt"])
	} else {
		scheme = "http"
	}

	ic.endpoint = fmt.Sprintf("%s://%s:%d/", scheme, api.IPAddress, port)

	// This path returns before the Keystone branch's warnings, so it emits its own:
	// --insecure is honored here too, and an Ironic CR without a TLS certificate
	// leaves the API on plain http — with the basic-auth credentials on it.
	if ic.insecure {
		warnInsecure("the standalone ironic API at " + ic.endpoint + " (--insecure)")
	}
	warnCleartext("the standalone ironic API", ic.endpoint)
	return ic, nil
}

// baremetalClient builds a gophercloud baremetal (ironic v1) service client that
// authenticates with HTTP basic auth and talks straight to the standalone
// endpoint. Type is set to "baremetal" so gophercloud emits the
// X-OpenStack-Ironic-API-Version header.
func (ic *ironicCreds) baremetalClient(microversion string) (*gophercloud.ServiceClient, error) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	switch {
	case ic.insecure:
		tlsCfg.InsecureSkipVerify = true
	default:
		if len(ic.caPEM) > 0 {
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(ic.caPEM) {
				return nil, fmt.Errorf("no certificates parsed from the ironic CA")
			}
			tlsCfg.RootCAs = pool
		}
		if ic.serverName != "" {
			tlsCfg.ServerName = ic.serverName
		}
	}

	endpointURL, err := url.Parse(ic.endpoint)
	if err != nil {
		return nil, fmt.Errorf("ironic endpoint %q: %w", ic.endpoint, err)
	}

	// Basic auth is injected by the RoundTripper; --debug wraps it on the OUTSIDE
	// so the Authorization header is never present when the request is logged.
	// (redact() covers Authorization as well now, so this is belt and braces.)
	var rt http.RoundTripper = &basicAuthTransport{
		base:     newHTTPTransport(tlsCfg),
		host:     endpointURL.Host,
		username: ic.username,
		password: ic.password,
	}
	if ic.debug {
		rt = newDebugTransport(rt)
	}
	if ic.timing {
		rt = newTimingTransport(rt, os.Stderr)
	}

	pc := &gophercloud.ProviderClient{}
	pc.HTTPClient = http.Client{
		Transport:     rt,
		Timeout:       ic.timeout,
		CheckRedirect: sameHostRedirect,
	}
	pc.UserAgent.Prepend("koc")

	return &gophercloud.ServiceClient{
		ProviderClient: pc,
		Endpoint:       ic.endpoint,
		ResourceBase:   ic.endpoint + "v1/",
		Type:           "baremetal",
		Microversion:   microversion,
	}, nil
}

// basicAuthTransport injects HTTP Basic credentials onto every request bound for
// the configured host.
//
// The host check is the security-relevant part: injecting at the RoundTripper
// runs on every request the client makes *including each redirected one*, which
// re-adds the credentials after net/http has stripped them for a cross-host hop.
// Restricting injection to the endpoint's own host means a 302 to somewhere else
// cannot collect the standalone ironic password.
type basicAuthTransport struct {
	base     http.RoundTripper
	host     string // host:port of the ironic endpoint; other hosts get no credentials
	username string
	password string
}

func (t *basicAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.host != "" && req.URL.Host != t.host {
		return t.base.RoundTrip(req)
	}
	r := req.Clone(req.Context())
	r.SetBasicAuth(t.username, t.password)
	return t.base.RoundTrip(r)
}

// firstCertDNSName returns the first DNS SAN of the first certificate in a PEM
// bundle, used as the TLS ServerName when dialing the API by IP.
func firstCertDNSName(pemBytes []byte) string {
	for len(pemBytes) > 0 {
		var block *pem.Block
		block, pemBytes = pem.Decode(pemBytes)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		if cert, err := x509.ParseCertificate(block.Bytes); err == nil && len(cert.DNSNames) > 0 {
			return cert.DNSNames[0]
		}
	}
	return ""
}

// VaultConfig resolves the Vault connection settings from the --vault-* flags,
// their VAULT_* env vars, the ~/.vault-token cache and — when those are
// incomplete — LCM cluster auto-discovery. It is exported because `koc vault kv`
// needs the same resolved config without going through Keystone auth at all.
func (o *Options) VaultConfig(ctx context.Context) (vault.Config, error) {
	// "koc vault kv" never authenticates to Keystone, so this is where an
	// unparseable VAULT_* boolean has to be reported for that command group.
	if err := envError(); err != nil {
		return vault.Config{}, err
	}

	// Token precedence, matching the Vault CLI: --vault-token / VAULT_TOKEN, then
	// the cached ~/.vault-token from `vault login`. A cached token is only used
	// when no explicit AppRole was given, so `--vault-role-id/-secret-id` still
	// take priority when the operator wants AppRole.
	if o.VaultToken == "" &&
		!o.vaultOptionExplicit("vault-role-id", "VAULT_ROLE_ID") &&
		!o.vaultOptionExplicit("vault-secret-id", "VAULT_SECRET_ID") {
		if tok := readVaultTokenFile(); tok != "" {
			o.VaultToken = tok
		}
	}

	// When Vault connection details are not fully supplied, discover them from
	// the LCM cluster (ConfigMap + AppRole secret) using the same kubeconfig as
	// --creds-from-ns, so a node operator needs no --vault-* flags.
	if o.vaultNeedsDiscovery() {
		if err := o.discoverVaultFromCluster(ctx); err != nil {
			if o.VaultAddr == "" {
				return vault.Config{}, fmt.Errorf("vault not configured and cluster auto-discovery failed: %w (set --vault-addr and an AppRole/token, or provide a reachable --kubeconfig)", err)
			}
			if o.Debug {
				fmt.Fprintf(os.Stderr, "vault: cluster auto-discovery: %v\n", err)
			}
		}
	}

	var caPEM []byte
	if o.VaultCACert != "" {
		b, err := os.ReadFile(o.VaultCACert)
		if err != nil {
			return vault.Config{}, fmt.Errorf("reading --vault-cacert %q: %w", o.VaultCACert, err)
		}
		caPEM = b
	}

	return vault.Config{
		Addr:        o.VaultAddr,
		Namespace:   o.VaultNamespace,
		Token:       o.VaultToken,
		RoleID:      o.VaultRoleID,
		SecretID:    o.VaultSecretID,
		ApprolePath: o.VaultApprolePath,
		KVMount:     o.VaultKVMount,
		CACertPEM:   caPEM,
		Insecure:    o.VaultInsecure,
		Timeout:     o.Timeout,
		Debug:       o.Debug,
	}, nil
}

// VaultClient resolves the Vault settings and returns an authenticated client.
func (o *Options) VaultClient(ctx context.Context) (*vault.Client, error) {
	cfg, err := o.VaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	return vault.New(ctx, cfg)
}

// applyVaultOpenrc reads the openrc KV v2 secret named by o.CredsFromVault and
// applies its OS_* variables to o, so the normal Keystone flow authenticates
// with those credentials. Explicit CLI flags still win over the fetched values.
func (o *Options) applyVaultOpenrc(ctx context.Context) error {
	vc, err := o.VaultClient(ctx)
	if err != nil {
		return err
	}

	path := vault.ResolvePath(o.VaultKVPrefix, o.VaultKVMount, o.CredsFromVault)
	data, err := vc.ReadKVData(ctx, path)
	if err != nil {
		return err
	}
	openrc, err := openrcFromKV(data)
	if err != nil {
		return fmt.Errorf("vault secret %q: %w", path, err)
	}
	o.applyOpenrcVars(parseOpenrc(openrc))

	// The openrc may point OS_CERT/OS_KEY at kolla control-node file paths that do
	// not exist where koc runs. When so, load the mTLS client cert/key from the
	// sibling ssl_certificates KV secret.
	if err := o.loadVaultClientCert(ctx, vc, path); err != nil {
		return err
	}
	return nil
}

// vaultSSLCertsSecret is the KV secret (sibling of the openrc) holding the
// deployment's PEM material.
const vaultSSLCertsSecret = "ssl_certificates"

// loadVaultClientCert loads the mTLS client certificate/key referenced by the
// openrc (OS_CERT/OS_KEY) from the sibling ssl_certificates secret, into memory,
// when the referenced files are not present locally. Explicit --os-cert/--os-key
// flags are left untouched, and existing local files (a kolla control node) win.
func (o *Options) loadVaultClientCert(ctx context.Context, vc *vault.Client, openrcPath string) error {
	certFromOpenrc := o.ClientCert != "" && (o.fs == nil || !o.fs.Changed("os-cert"))
	keyFromOpenrc := o.ClientKey != "" && (o.fs == nil || !o.fs.Changed("os-key"))
	if !certFromOpenrc && !keyFromOpenrc {
		return nil
	}
	if fileExists(o.ClientCert) && fileExists(o.ClientKey) {
		return nil // running where the referenced files exist (e.g. a control node)
	}

	sibling := siblingVaultPath(openrcPath, vaultSSLCertsSecret)
	data, err := vc.ReadKVData(ctx, sibling)
	if err != nil {
		return fmt.Errorf("openrc references a client cert but reading %q failed: %w", sibling, err)
	}

	certName, keyName := certKeyKVNames(o.ClientCert, o.ClientKey)
	certPEM := pemFromKV(data, certName, "backend_pem")
	keyPEM := pemFromKV(data, keyName, "backend_key_pem")
	if len(certPEM) == 0 || len(keyPEM) == 0 {
		return fmt.Errorf("%q has no client cert/key (looked for %q/%q; available keys: %v)",
			sibling, certName, keyName, kvKeys(data))
	}

	o.ClientCertPEM = certPEM
	o.ClientKeyPEM = keyPEM
	o.ClientCert = "" // switch the TLS layer to the in-memory material
	o.ClientKey = ""
	return nil
}

// siblingVaultPath replaces the last path segment of a KV path with name.
func siblingVaultPath(path, name string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[:i+1] + name
	}
	return name
}

// certKeyKVNames derives the ssl_certificates key names from the openrc cert/key
// file basenames, following the kolla convention "<prefix>_pem" (cert) and
// "<prefix>_key_pem" (key), e.g. backend-cert.pem → backend_pem / backend_key_pem.
func certKeyKVNames(certPath, keyPath string) (certName, keyName string) {
	prefix := ""
	switch {
	case certPath != "":
		prefix = strings.TrimSuffix(strings.TrimSuffix(baseName(certPath), ".pem"), "-cert")
	case keyPath != "":
		prefix = strings.TrimSuffix(strings.TrimSuffix(baseName(keyPath), ".pem"), "-key")
	}
	if prefix == "" {
		return "", ""
	}
	return prefix + "_pem", prefix + "_key_pem"
}

func baseName(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// pemFromKV returns the first named string value from a KV data map that looks
// like PEM, trying each candidate name in order.
func pemFromKV(data map[string]any, names ...string) []byte {
	for _, n := range names {
		if n == "" {
			continue
		}
		if v, ok := data[n].(string); ok && v != "" {
			return []byte(v)
		}
	}
	return nil
}

func kvKeys(data map[string]any) []string {
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// readVaultTokenFile returns the token cached by `vault login` in ~/.vault-token,
// honoring the VAULT_TOKEN_FILE override, or "" if none is present.
func readVaultTokenFile() string {
	path := os.Getenv("VAULT_TOKEN_FILE")
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		path = filepath.Join(home, ".vault-token")
	}
	b, err := os.ReadFile(path) //nolint:gosec // G304: operator-controlled Vault token path (flag/env/~/.vault-token)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// vaultNeedsDiscovery reports whether Vault connection details are incomplete
// and cluster auto-discovery should be attempted.
func (o *Options) vaultNeedsDiscovery() bool {
	if o.VaultAddr == "" {
		return true
	}
	if o.VaultToken != "" {
		return false
	}
	return o.VaultRoleID == "" || o.VaultSecretID == ""
}

// lcmVaultConfig is the subset of the LCM lcm-config document describing how to
// reach Vault and where the region KV lives.
type lcmVaultConfig struct {
	Addr      string `yaml:"vault_addr"`
	Namespace string `yaml:"vault_namespace"`
	RoleID    string `yaml:"vault_app_role_id"`
	KVEngine  string `yaml:"vault_kv_region_engine"`
	KVPrefix  string `yaml:"vault_kv_region_prefix"`
}

// discoverVaultFromCluster fills any unset Vault connection option from the LCM
// deployment: the lcm-config ConfigMap (address, namespace, role_id, KV mount,
// region prefix) and the AppRole secret-id Secret. Explicit --vault-* flags and
// VAULT_* env values always win. TLS uses the system roots (the LCM Vault
// endpoint presents a publicly-trusted certificate).
func (o *Options) discoverVaultFromCluster(ctx context.Context) error {
	kc, err := kube.Load(kube.Options{Kubeconfig: o.Kubeconfig, Context: o.KubeContext, Debug: o.Debug, Timeout: o.Timeout})
	if err != nil {
		return err
	}

	cm, err := kc.GetConfigMap(ctx, lcmConfigNamespace, lcmConfigName)
	if err != nil {
		return err
	}
	var lc lcmVaultConfig
	if err := yaml.Unmarshal([]byte(cm[lcmConfigKey]), &lc); err != nil {
		return fmt.Errorf("parsing %s/%s: %w", lcmConfigNamespace, lcmConfigName, err)
	}

	o.fillVaultOption("vault-addr", &o.VaultAddr, lc.Addr, "VAULT_ADDR")
	o.fillVaultOption("vault-namespace", &o.VaultNamespace, lc.Namespace, "VAULT_NAMESPACE")
	o.fillVaultOption("vault-role-id", &o.VaultRoleID, lc.RoleID, "VAULT_ROLE_ID")
	o.fillVaultOption("vault-kv-mount", &o.VaultKVMount, lc.KVEngine, "VAULT_KV_MOUNT", "VAULT_ENGINE")
	o.fillVaultOption("vault-kv-prefix", &o.VaultKVPrefix, lc.KVPrefix, "VAULT_KV_PREFIX", "VAULT_PREFIX")

	if o.VaultSecretID == "" && !o.vaultOptionExplicit("vault-secret-id", "VAULT_SECRET_ID") {
		sec, err := kc.GetSecret(ctx, vaultApproleSecretNS, vaultApproleSecret)
		if err != nil {
			return err
		}
		o.VaultSecretID = string(sec[vaultApproleSecretKey])
	}
	return nil
}

// fillVaultOption sets *dst to val unless the flag was explicitly set, one of
// its env vars is present, or val is empty.
func (o *Options) fillVaultOption(flag string, dst *string, val string, envs ...string) {
	if val == "" || o.vaultOptionExplicit(flag, envs...) {
		return
	}
	*dst = val
}

// vaultOptionExplicit reports whether a Vault option was set on the command line
// or via one of its environment variables (either of which must win over
// discovery). Options with a fallback env name (VAULT_ENGINE, VAULT_PREFIX)
// list it after the canonical one.
func (o *Options) vaultOptionExplicit(flag string, envs ...string) bool {
	if o.fs != nil && o.fs.Changed(flag) {
		return true
	}
	for _, env := range envs {
		if os.Getenv(env) != "" {
			return true
		}
	}
	return false
}

// openrcFromKV extracts an openrc script from a KV v2 data map. It prefers the
// plaintext "value" field, then a (possibly base64-encoded) "openrc" field, and
// finally synthesizes one from any flat OS_* keys.
func openrcFromKV(data map[string]any) (string, error) {
	if v, ok := data["value"].(string); ok && strings.Contains(v, "OS_") {
		return v, nil
	}
	if v, ok := data["openrc"].(string); ok && v != "" {
		if dec, err := base64.StdEncoding.DecodeString(strings.TrimSpace(v)); err == nil && strings.Contains(string(dec), "OS_") {
			return string(dec), nil
		}
		return v, nil
	}
	var b strings.Builder
	for k, val := range data {
		if s, ok := val.(string); ok && strings.HasPrefix(k, "OS_") {
			fmt.Fprintf(&b, "%s=%s\n", k, s)
		}
	}
	if b.Len() > 0 {
		return b.String(), nil
	}
	return "", fmt.Errorf("no 'value'/'openrc' field or OS_* keys found")
}

// parseOpenrc parses `export OS_X=value` lines (quotes stripped) into a map.
func parseOpenrc(s string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		if !strings.HasPrefix(key, "OS_") {
			continue
		}
		out[key] = trimQuotes(strings.TrimSpace(line[eq+1:]))
	}
	return out
}

func trimQuotes(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// applyOpenrcVars maps openrc OS_* variables onto o. A field is only set when its
// flag was not explicitly provided, so explicit CLI flags override Vault values.
func (o *Options) applyOpenrcVars(kv map[string]string) {
	set := func(flag string, dst *string, keys ...string) {
		if o.fs != nil && o.fs.Changed(flag) {
			return
		}
		for _, k := range keys {
			if v := kv[k]; v != "" {
				*dst = v
				// pflag never saw this value, but it still outranks
				// clouds.yaml — mark it so Options.override keeps it.
				o.markForced(flag)
				return
			}
		}
	}
	set("os-auth-url", &o.AuthURL, "OS_AUTH_URL")
	set("os-username", &o.Username, "OS_USERNAME")
	set("os-user-id", &o.UserID, "OS_USER_ID")
	set("os-password", &o.Password, "OS_PASSWORD")
	set(flagOSProjectName, &o.ProjectName, "OS_PROJECT_NAME", "OS_TENANT_NAME")
	set(flagOSProjectID, &o.ProjectID, "OS_PROJECT_ID", "OS_TENANT_ID")
	set("os-project-domain-name", &o.ProjectDomainName, "OS_PROJECT_DOMAIN_NAME")
	set("os-user-domain-name", &o.UserDomainName, "OS_USER_DOMAIN_NAME")
	set("os-domain-name", &o.DomainName, "OS_DOMAIN_NAME")
	set("os-region-name", &o.RegionName, "OS_REGION_NAME")
	set("os-interface", &o.Interface, "OS_INTERFACE", "OS_ENDPOINT_TYPE")
	set("os-cacert", &o.CACert, "OS_CACERT")
	set("os-cert", &o.ClientCert, "OS_CERT")
	set("os-key", &o.ClientKey, "OS_KEY")
	set("os-application-credential-id", &o.AppCredID, "OS_APPLICATION_CREDENTIAL_ID")
	set("os-application-credential-name", &o.AppCredName, "OS_APPLICATION_CREDENTIAL_NAME")
	set("os-application-credential-secret", &o.AppCredSecret, "OS_APPLICATION_CREDENTIAL_SECRET")

	// OS_ENDPOINT_TYPE is sometimes "publicURL"; normalize to the interface name.
	if o.fs == nil || !o.fs.Changed("os-interface") {
		o.Interface = strings.TrimSuffix(o.Interface, "URL")
	}
}

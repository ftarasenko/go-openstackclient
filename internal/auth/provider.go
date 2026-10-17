package auth

import (
	"context"
	"crypto/tls"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/config/clouds"

	"github.com/ftarasenko/go-openstackclient/pkg/openstack"
)

// Client bundles the authenticated provider with the resolved endpoint options
// and microversion settings, and acts as the factory for per-service clients.
// It is built once per invocation and reused to derive every service client.
type Client struct {
	Provider *gophercloud.ProviderClient
	Endpoint gophercloud.EndpointOpts

	opts *Options

	// ironic is set only in --creds-from-ns mode: a standalone basic-auth Ironic
	// client with no Keystone provider. When non-nil, Provider is nil and only
	// Baremetal() is available.
	ironic *ironicCreds
}

// Authenticate builds a single authenticated ProviderClient following the
// documented precedence (clouds.yaml → OS_* env → application credentials) and
// wires the resolved TLS config into it.
func (o *Options) Authenticate(ctx context.Context) (*Client, error) {
	// An environment variable koc could not parse is reported before any
	// credential is used: the affected toggles are security ones, so silently
	// running on a default the operator did not choose is not acceptable.
	if err := envError(); err != nil {
		return nil, err
	}
	if o.CredsFromNS != "" && o.CredsFromVault != "" {
		return nil, fmt.Errorf("--creds-from-ns and --creds-from-vault are mutually exclusive")
	}
	o.applyTenantAliases()
	if err := o.validateSystemScope(); err != nil {
		return nil, err
	}
	// --os-password-stdin is read here, before the first network round trip, so
	// a run that pipes its password in fails on a conflicting source rather than
	// on a rejected token request.
	if err := o.applyPasswordStdin(); err != nil {
		return nil, err
	}

	// Vault: fetch an openrc secret and fold its OS_* values into o, then fall
	// through to the normal Keystone flow below (works for every service).
	if o.CredsFromVault != "" {
		if err := o.applyVaultOpenrc(ctx); err != nil {
			return nil, fmt.Errorf("loading credentials from vault: %w", err)
		}
	}

	// Namespace: a standalone metal3 Ironic uses HTTP basic auth, not Keystone,
	// so short-circuit and return a baremetal-only client.
	if o.CredsFromNS != "" {
		ic, err := o.loadIronicCreds(ctx)
		if err != nil {
			return nil, fmt.Errorf("loading ironic credentials from namespace %q: %w", o.CredsFromNS, err)
		}
		return &Client{ironic: ic, opts: o}, nil
	}

	ao, eo, baseTLS, err := o.resolveAuth()
	if err != nil {
		return nil, err
	}
	// Nothing supplied a password: ask for it the way osc-lib does, on the
	// terminal. AllowReauth below then replays whatever was typed, so a token
	// expiring mid-command does not ask twice. Only the auth types that take a
	// password are asked.
	if o.authTakesPassword() {
		if err := o.promptMissingPassword(&ao, os.Stderr); err != nil {
			return nil, err
		}
	}
	// gophercloud refuses to re-authenticate from a pre-issued token without a
	// new scope, and an expired token cannot mint a fresh one anyway.
	ao.AllowReauth = ao.TokenID == ""

	tlsCfg, insecure, err := o.resolveTLSConfig(baseTLS)
	if err != nil {
		return nil, err
	}
	if insecure {
		warnInsecure("the OpenStack API (--insecure)")
	}
	if keystoneless(o.authKind) {
		if o.authKind != authTypeNone {
			warnCleartext("the service endpoint", o.Endpoint)
		}
		return o.keystonelessClient(ao, eo, tlsCfg), nil
	}
	warnCleartext("the OpenStack identity endpoint", ao.IdentityEndpoint)

	// The HTTP client is built here and handed to gophercloud, rather than letting
	// config.WithTLSConfig build one, for three reasons: WithTLSConfig replaces
	// Transport wholesale (dropping the debug/timing wrappers and the proxy-aware
	// DefaultTransport clone), a zero-value http.Client has no timeout at all, and
	// NewProviderClient POSTs /v3/auth/tokens *inside* the call — so anything
	// attached afterwards cannot see the token request, the returned catalog, or an
	// authentication failure.
	//
	// The token request itself is the public API's (pkg/openstack.New), handed
	// the AuthOptions this package resolved from flags, env and clouds.yaml, so
	// the CLI authenticates exactly the way an importer does.
	hc := o.httpClient(tlsCfg)
	session, err := openstack.New(ctx, openstack.Config{
		AuthOptions: &ao,
		Region:      eo.Region,
		Interface:   string(eo.Availability),
		HTTPClient:  &hc,
	})
	if err != nil {
		return nil, err
	}
	provider := session.Provider()
	if len(o.endpointOverrides) > 0 {
		provider.EndpointLocator = o.endpointLocator(provider.EndpointLocator)
	}

	return &Client{Provider: provider, Endpoint: session.EndpointOpts(), opts: o}, nil
}

// resolveAuth produces the AuthOptions, EndpointOpts and (optional) clouds.yaml
// TLS config, selecting the clouds.yaml path when a cloud name is present and
// otherwise falling back to OS_* environment variables. Explicit CLI flags then
// override individual auth fields.
func (o *Options) resolveAuth() (gophercloud.AuthOptions, gophercloud.EndpointOpts, *tls.Config, error) {
	var ao gophercloud.AuthOptions
	var eo gophercloud.EndpointOpts
	var baseTLS *tls.Config

	iface, err := normalizeInterface(o.Interface)
	if err != nil {
		return ao, eo, nil, err
	}
	envKind, err := canonicalAuthType(o.AuthType)
	if err != nil {
		return ao, eo, nil, err
	}
	// A Keystone-less auth type or an --os-endpoint makes this the
	// environment's cloud, as upstream's "envvars" cloud would be.
	if o.Endpoint == "" && !keystoneless(envKind) {
		if err := o.selectDefaultCloud(); err != nil {
			return ao, eo, nil, err
		}
	}

	var extra cloudExtra
	if o.Cloud != "" {
		ao, eo, baseTLS, extra, err = o.parseCloud(iface)
		if err != nil {
			return ao, eo, nil, err
		}
		reconcileCloudDomains(&ao)
	} else if keystoneless(envKind) {
		eo = gophercloud.EndpointOpts{Region: o.RegionName}
	} else {
		// Build the auth options from OS_* / flags directly rather than via
		// gophercloud's AuthOptionsFromEnv, which only understands OS_DOMAIN_NAME
		// and rejects the standard split OS_USER_DOMAIN_NAME / OS_PROJECT_DOMAIN_NAME
		// openrc (wrongly demanding OS_PROJECT_ID when only a project name +
		// project domain are given). applyAuthOverrides and applyDomainScope below
		// populate ao from o's fields.
		if o.AuthURL == "" {
			return ao, eo, nil, fmt.Errorf("no credentials found: set --os-cloud / OS_CLOUD (a clouds.yaml entry), or OS_AUTH_URL and the related OS_* variables")
		}
		if o.Password == "" && o.AppCredID == "" && o.AppCredName == "" && o.Token == "" && o.Passcode == "" && !o.willPromptForPassword() {
			return ao, eo, nil, fmt.Errorf("no credentials found: set OS_PASSWORD, application credentials (OS_APPLICATION_CREDENTIAL_ID/_SECRET) or OS_TOKEN, pass --os-password-stdin, or run interactively to be prompted")
		}
		eo = gophercloud.EndpointOpts{Region: o.RegionName}
	}

	o.applyAuthOverrides(&ao)
	ao.Passcode = firstNonEmpty(o.Passcode, extra.passcode)
	if err := o.applyAuthType(&ao, extra.authType); err != nil {
		return ao, eo, nil, err
	}
	o.Endpoint = firstNonEmpty(o.Endpoint, extra.endpoint)
	o.endpointOverrides = collectEndpointOverrides(extra.overrides)
	if keystoneless(o.authKind) && o.Endpoint == "" && len(o.endpointOverrides) == 0 {
		return ao, eo, nil, fmt.Errorf("auth type %s uses no Keystone, so it needs the service URL: set --os-endpoint / OS_ENDPOINT or an OS_<SERVICE>_ENDPOINT_OVERRIDE", o.authKind)
	}
	o.applyEndpointOverrides(&eo)
	return ao, eo, baseTLS, nil
}

// applyTenantAliases folds the hidden --os-tenant-name / --os-tenant-id into
// the project flags, as osc-lib's own aliases do; the project spelling wins
// when both are typed.
func (o *Options) applyTenantAliases() {
	if o.fs == nil {
		return
	}
	for _, a := range []struct {
		alias, flag string
		v           string
		dst         *string
	}{
		{flagOSTenantName, flagOSProjectName, o.tenantName, &o.ProjectName},
		{flagOSTenantID, flagOSProjectID, o.tenantID, &o.ProjectID},
	} {
		if o.fs.Changed(a.alias) && !o.fs.Changed(a.flag) {
			*a.dst = a.v
			o.markForced(a.flag)
		}
	}
}

// selectDefaultCloud picks the lone cloud of a clouds.yaml when nothing named
// one and nothing else says where Keystone is, as openstacksdk does: with
// OS_CLIENT_CONFIG_FILE pointing at a one-cloud file, OS_CLOUD is optional.
// OS_AUTH_URL (or --os-auth-url) keeps the environment path, the way upstream's
// environment "envvars" cloud does.
func (o *Options) selectDefaultCloud() error {
	if o.Cloud != "" || o.AuthURL != "" {
		return nil
	}
	cf, err := findCloudsFile()
	if err != nil || cf == nil {
		return err
	}
	switch len(cf.names) {
	case 0:
		return nil
	case 1:
		o.Cloud = cf.names[0]
		return nil
	}
	sort.Strings(cf.names)
	return fmt.Errorf("no credentials found: %s defines several clouds (%s); choose one with --os-cloud or OS_CLOUD",
		cf.path, strings.Join(cf.names, ", "))
}

// parseCloud loads the named cloud from the clouds.yaml / secure.yaml koc's
// search found (see cloudsfile.go), returning alongside it the settings
// clouds.Parse does not model (auth_type, endpoint overrides, …).
//
// Region and interface are handed to clouds.Parse from o — whose fields
// already default to OS_REGION_NAME / OS_INTERFACE (or OS_ENDPOINT_TYPE) — so
// the normalized interface and any typed flag reach it, rather than leaving
// clouds.Parse to read the raw variables itself. Either way the environment
// beats the cloud's own region_name / interface, as upstream's does.
func (o *Options) parseCloud(iface string) (gophercloud.AuthOptions, gophercloud.EndpointOpts, *tls.Config, cloudExtra, error) {
	cf, err := findCloudsFile()
	if err != nil {
		return gophercloud.AuthOptions{}, gophercloud.EndpointOpts{}, nil, cloudExtra{}, err
	}
	if cf == nil {
		return gophercloud.AuthOptions{}, gophercloud.EndpointOpts{}, nil, cloudExtra{}, fmt.Errorf("loading cloud %q: %w", o.Cloud, errNoCloudsFile())
	}
	opts, done, err := cf.parseOptions(o.Cloud)
	if err != nil {
		return gophercloud.AuthOptions{}, gophercloud.EndpointOpts{}, nil, cloudExtra{}, err
	}
	defer done()
	opts = append(opts,
		clouds.WithCloudName(o.Cloud),
		clouds.WithRegion(o.RegionName),
		clouds.WithEndpointType(iface),
	)
	ao, eo, tlsCfg, err := clouds.Parse(opts...)
	if err != nil {
		return ao, eo, nil, cloudExtra{}, fmt.Errorf("loading cloud %q from %s: %w", o.Cloud, cf.path, err)
	}
	return ao, eo, tlsCfg, cf.extras[o.Cloud], nil
}

// reconcileCloudDomains restores the domain coalescing clouds.Parse used to do
// for itself, for a clouds.yaml that names only one of the two domains.
//
// Keystone qualifies two different things by domain: the *user* being
// authenticated, and the *project* being scoped to. gophercloud v2.14.0 and
// earlier folded them together — AuthOptions.DomainName fell back through
// user_domain_name, project_domain_name, domain_name, and Scope was left nil
// for gophercloud to derive from TenantName and that one domain. v2.15.0 split
// them: DomainName no longer falls back to project_domain_name, and Parse
// returns an explicit Scope carrying the project's domain. A clouds.yaml that
// sets only one of the pair therefore loses the other half:
//
//   - only project_domain_name → the user is unqualified, and Keystone rejects
//     the password grant as ambiguous;
//   - only user_domain_name → the project-by-name scope is unqualified, and
//     Keystone cannot tell which project of that name is meant.
//
// Single-domain clouds — the common case, and what the fleet runs — write only
// one of the two and mean it for both. So each half falls back to the other,
// which is what v2.14.0 did and what upstream OSC does. An explicit --os-*-domain
// flag or OS_*_DOMAIN_* variable still wins: applyDomainScope runs after this and
// overwrites both.
//
// Inert against gophercloud v2.14.0, which returns a nil Scope here.
func reconcileCloudDomains(ao *gophercloud.AuthOptions) {
	// A system- or trust-scoped token is not domain-qualified at all, and a
	// project-by-ID scope needs no domain, so there is nothing to reconcile.
	if ao.Scope == nil || ao.Scope.System || ao.Scope.TrustID != "" {
		return
	}
	if ao.DomainID == "" && ao.DomainName == "" {
		ao.DomainID, ao.DomainName = ao.Scope.DomainID, ao.Scope.DomainName
	}
	if ao.Scope.ProjectName != "" && ao.Scope.ProjectID == "" &&
		ao.Scope.DomainID == "" && ao.Scope.DomainName == "" {
		ao.Scope.DomainID, ao.Scope.DomainName = ao.DomainID, ao.DomainName
	}
}

// applyAuthOverrides layers the auth flags over whatever the clouds.yaml / env
// path produced. A flag that was not typed carries its OS_* variable, so the
// environment outranks a named cloud field by field, as upstream does:
// openstacksdk's OpenStackConfig.get_one merges osc-lib's argparse values —
// whose defaults are the OS_* variables — over the cloud's settings. A
// clouds.yaml that leaves its password out therefore takes OS_PASSWORD, and a
// sourced openrc's OS_PROJECT_NAME rescopes a named cloud.
func (o *Options) applyAuthOverrides(ao *gophercloud.AuthOptions) {
	setIf := func(dst *string, v string) {
		if v != "" {
			*dst = v
		}
	}
	setIf(&ao.IdentityEndpoint, o.AuthURL)
	setIf(&ao.Username, o.Username)
	setIf(&ao.UserID, o.UserID)
	setIf(&ao.Password, o.Password)
	setIf(&ao.TokenID, o.Token)
	setIf(&ao.TenantName, o.ProjectName)
	setIf(&ao.TenantID, o.ProjectID)
	setIf(&ao.ApplicationCredentialID, o.AppCredID)
	setIf(&ao.ApplicationCredentialName, o.AppCredName)
	setIf(&ao.ApplicationCredentialSecret, o.AppCredSecret)

	o.applyProjectScope(ao)
	o.applyDomainScope(ao)
	o.applySystemScope(ao)
	o.applyTrustScope(ao)
	o.applyDefaultDomain(ao)
}

// applyDefaultDomain qualifies, with --os-default-domain, a username and a
// project-by-name scope that nothing else qualified, as osc-lib does
// (osc_lib/cli/client_config.py _auth_default_domain). Keystone otherwise
// rejects the password grant; devstack's zed clouds.yaml names its system
// admin with no user domain at all.
func (o *Options) applyDefaultDomain(ao *gophercloud.AuthOptions) {
	if o.DefaultDomain == "" || ao.ApplicationCredentialID != "" || ao.ApplicationCredentialName != "" {
		return
	}
	if ao.Username != "" && ao.UserID == "" && ao.DomainID == "" && ao.DomainName == "" {
		ao.DomainID = o.DefaultDomain
	}
	if s := ao.Scope; s != nil && s.ProjectName != "" && s.ProjectID == "" && s.DomainID == "" && s.DomainName == "" {
		s.DomainID = o.DefaultDomain
	}
}

// applyProjectScope moves an explicit project scope to the project the operator
// named. gophercloud reads the project from ao.Scope whenever it is set, and
// clouds.Parse (v2.15.0) always sets it for a cloud with a project, so the
// TenantName/TenantID applyAuthOverrides just wrote were never consulted:
// `--os-cloud x --os-project-name demo` stayed on the cloud's project. Upstream
// OSC re-scopes, for OS_PROJECT_* as much as for the flags.
//
// By name, the scope keeps its domain — the cloud's project domain, unless a
// domain flag replaces it in applyDomainScope. By ID, it needs none. A system or
// trust scope is not a project scope and is left alone.
func (o *Options) applyProjectScope(ao *gophercloud.AuthOptions) {
	if ao.Scope == nil || ao.Scope.System || ao.Scope.TrustID != "" {
		return
	}
	if id := o.ProjectID; id != "" {
		ao.Scope = &gophercloud.AuthScope{ProjectID: id}
		return
	}
	if name := o.ProjectName; name != "" {
		ao.Scope.ProjectName, ao.Scope.ProjectID = name, ""
	}
}

// systemScopeAll is the only system-scope value Keystone defines. OSC accepts
// `--os-system-scope all` and nothing else.
const systemScopeAll = "all"

// validateSystemScope rejects an unusable --os-system-scope before any network
// call. A system-scoped token cannot also be project- or domain-scoped, so
// asking for both on the same command line is an error rather than a silent
// precedence rule.
//
// Only *explicitly given* flags conflict: a project set by clouds.yaml, an
// openrc in Vault, or a leftover OS_PROJECT_NAME in the environment is
// background configuration the operator is overriding on purpose, and
// applySystemScope drops it.
func (o *Options) validateSystemScope() error {
	if o.SystemScope == "" {
		return nil
	}
	if o.SystemScope != systemScopeAll {
		return fmt.Errorf("--os-system-scope: unsupported value %q, the only value Keystone defines is %q", o.SystemScope, systemScopeAll)
	}
	if o.fs == nil || !o.fs.Changed("os-system-scope") {
		return nil
	}
	for _, name := range []string{flagOSProjectName, flagOSProjectID, "os-domain-name", "os-domain-id"} {
		if o.explicitlySet(name) {
			return fmt.Errorf("--os-system-scope and --%s are mutually exclusive: a token is scoped to the system, a domain or a project, not several", name)
		}
	}
	return nil
}

// applySystemScope replaces whatever project/domain scope the clouds.yaml, env
// or Vault openrc supplied with a system scope. It runs after applyDomainScope
// so it wins unconditionally, matching the flag's "scope this token to the whole
// deployment" meaning.
func (o *Options) applySystemScope(ao *gophercloud.AuthOptions) {
	if o.SystemScope == "" {
		return
	}
	ao.Scope = &gophercloud.AuthScope{System: true}
	// A system-scoped token carries no project. Clear the legacy tenant fields so
	// nothing downstream re-derives a project scope from them.
	ao.TenantName = ""
	ao.TenantID = ""
}

// applyDomainScope wires the user's identity domain and the token scope
// independently, so a user in one domain can scope to a project in another.
//
// gophercloud uses ao.DomainName for BOTH the user's identity domain and — when
// ao.Scope is nil — the project scope's domain (ToTokenV3ScopeMap), which
// conflates the two. We therefore set ao.Scope explicitly whenever a domain flag
// is supplied. When no koc domain flag is given we leave scoping untouched so
// the clouds.yaml / AuthOptionsFromEnv defaults are preserved.
//
// Each of the three domains may be named by ID or by name (OSC's
// --os-*-domain-id / --os-*-domain-name); the fallbacks between them are the
// same either way.
func (o *Options) applyDomainScope(ao *gophercloud.AuthOptions) {
	user := o.domainOption("os-user-domain-id", o.UserDomainID, "os-user-domain-name", o.UserDomainName)
	project := o.domainOption("os-project-domain-id", o.ProjectDomainID, "os-project-domain-name", o.ProjectDomainName)
	domain := o.domainOption("os-domain-id", o.DomainID, "os-domain-name", o.DomainName)

	if user.empty() && project.empty() && domain.empty() {
		return
	}

	// The user's identity domain qualifies the username/user-id. Prefer an
	// explicit user domain, then a lone --os-domain-*, then the project
	// domain (single-domain clouds set only one of these).
	if ud := firstDomain(user, domain, project); !ud.empty() {
		ao.DomainID, ao.DomainName = ud.ID, ud.Name
	}

	projectName := firstNonEmpty(o.ProjectName, ao.TenantName)
	projectID := firstNonEmpty(o.ProjectID, ao.TenantID)

	switch {
	case projectID != "":
		// Project-by-ID scope needs no domain qualifier.
		ao.Scope = &gophercloud.AuthScope{ProjectID: projectID}
	case projectName != "":
		// Project-by-name must be qualified by the project's own domain.
		pd := firstDomain(project, user, domain)
		ao.Scope = &gophercloud.AuthScope{ProjectName: projectName, DomainID: pd.ID, DomainName: pd.Name}
	case !domain.empty():
		// Domain-scoped token (no project).
		ao.Scope = &gophercloud.AuthScope{DomainID: domain.ID, DomainName: domain.Name}
	}
}

// domainRef is one Keystone domain, named by exactly one of ID or Name —
// gophercloud rejects a scope or user carrying both ("exactly one of DomainID
// or DomainName").
type domainRef struct{ ID, Name string }

func (d domainRef) empty() bool { return d.ID == "" && d.Name == "" }

// domainOption reads one domain from its -id/-name flag pair. When both are
// present the ID wins — it cannot be ambiguous — unless the name was given
// explicitly and the ID only came from the environment: a sourced openrc
// exporting OS_USER_DOMAIN_ID must not outrank --os-user-domain-name typed on
// the command line.
func (o *Options) domainOption(idFlag, id, nameFlag, name string) domainRef {
	d := domainRef{ID: id, Name: name}
	if d.ID != "" && d.Name != "" {
		if o.explicitlySet(nameFlag) && !o.explicitlySet(idFlag) {
			d.ID = ""
		} else {
			d.Name = ""
		}
	}
	return d
}

func firstDomain(refs ...domainRef) domainRef {
	for _, d := range refs {
		if !d.empty() {
			return d
		}
	}
	return domainRef{}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func (o *Options) applyEndpointOverrides(eo *gophercloud.EndpointOpts) {
	if region := o.RegionName; region != "" {
		eo.Region = region
	}
	// resolveAuth has already rejected a value normalizeInterface refuses.
	iface, _ := normalizeInterface(o.Interface)
	switch iface {
	case "public":
		eo.Availability = gophercloud.AvailabilityPublic
	case "internal":
		eo.Availability = gophercloud.AvailabilityInternal
	case "admin":
		eo.Availability = gophercloud.AvailabilityAdmin
	}
}

// normalizeInterface accepts the endpoint interface spellings the Python
// clients do: public / internal / admin in any case, and the identity-v2
// publicURL / internalURL / adminURL that OS_ENDPOINT_TYPE usually carries.
// Anything else is an error rather than a silent fall back to public.
func normalizeInterface(v string) (string, error) {
	if v == "" {
		return "", nil
	}
	n := strings.ToLower(strings.TrimSuffix(strings.TrimSuffix(v, "URL"), "url"))
	switch n {
	case "public", "internal", "admin":
		return n, nil
	}
	return "", fmt.Errorf("--os-interface / OS_INTERFACE: unsupported value %q, want public, internal or admin", v)
}

// Auth types koc implements, by osc-lib / keystoneauth plugin name.
const (
	authTypePassword   = "password"
	authTypeToken      = "token"
	authTypeAppCred    = "v3applicationcredential"
	authTypeTOTP       = "v3totp"
	authTypeMFA        = "v3multifactor"
	authTypeNone       = "none"
	authTypeAdminToken = "admin_token"
	authTypeHTTPBasic  = "http_basic"
)

// canonicalAuthType maps a keystoneauth plugin name onto the ones koc
// implements. koc speaks identity v3 only, so the v3-prefixed names and the
// version-discovering generic ones mean the same thing here.
func canonicalAuthType(v string) (string, error) {
	switch strings.ToLower(strings.ReplaceAll(v, "-", "_")) {
	case "":
		return "", nil
	case "password", "v3password":
		return authTypePassword, nil
	case "token", "v3token":
		return authTypeToken, nil
	case "v3applicationcredential", "applicationcredential", "v3_application_credential", "application_credential":
		return authTypeAppCred, nil
	case "v3totp", "totp":
		return authTypeTOTP, nil
	case "v3multifactor", "multifactor":
		return authTypeMFA, nil
	case "none", "noauth":
		return authTypeNone, nil
	case "admin_token", "token_endpoint":
		return authTypeAdminToken, nil
	case "http_basic":
		return authTypeHTTPBasic, nil
	}
	return "", fmt.Errorf("--os-auth-type / OS_AUTH_TYPE: unsupported auth type %q; koc supports password, token, v3applicationcredential, v3totp, v3multifactor, none (noauth), admin_token (token_endpoint) and http_basic", v)
}

// authTakesPassword reports whether the settled auth type is one a missing
// password is prompted for.
func (o *Options) authTakesPassword() bool {
	switch o.authKind {
	case "", authTypePassword, authTypeMFA, authTypeHTTPBasic:
		return true
	}
	return false
}

// applyAuthType settles which auth method the request carries and records it
// in o.authKind. gophercloud picks by field precedence (password and/or TOTP,
// then token, then application credential) and rejects a token alongside any
// user field, so leftovers of the other methods are cleared here.
//
// With no auth type given — by flag, OS_AUTH_TYPE or the cloud's auth_type —
// it is chosen the way osc-lib does (_auth_select_default_plugin): a user means
// password (or an application credential, which gophercloud tells apart), and
// a token only when nothing else was supplied. Neither inference picks TOTP:
// as upstream, a passcode is used only by v3totp / v3multifactor.
func (o *Options) applyAuthType(ao *gophercloud.AuthOptions, cloudAuthType string) error {
	t, err := canonicalAuthType(firstNonEmpty(o.AuthType, cloudAuthType))
	if err != nil {
		return err
	}
	o.authKind = t
	appCred := ao.ApplicationCredentialID != "" || ao.ApplicationCredentialName != ""
	clearToken := func() { ao.TokenID = "" }
	clearAppCred := func() {
		ao.ApplicationCredentialID, ao.ApplicationCredentialName, ao.ApplicationCredentialSecret = "", "", ""
	}
	if t != authTypeTOTP && t != authTypeMFA {
		ao.Passcode = ""
	}
	noUser := ao.Username == "" && ao.UserID == ""

	switch t {
	case "":
		if ao.TokenID == "" {
			return nil
		}
		if !noUser || ao.Password != "" || appCred {
			clearToken()
			return nil
		}
		// A token and nothing else: token auth.
		o.authKind = authTypeToken
		tokenAuth(ao)
	case authTypePassword:
		clearToken()
		clearAppCred()
	case authTypeAppCred:
		if !appCred {
			return fmt.Errorf("auth type %s needs --os-application-credential-id or --os-application-credential-name (env OS_APPLICATION_CREDENTIAL_ID / _NAME)", authTypeAppCred)
		}
		clearToken()
		ao.Password = ""
	case authTypeToken:
		if ao.TokenID == "" {
			return fmt.Errorf("auth type token needs a token: set --%s or OS_TOKEN", flagOSToken)
		}
		clearAppCred()
		tokenAuth(ao)
	case authTypeTOTP, authTypeMFA:
		if ao.Passcode == "" {
			return fmt.Errorf("auth type %s needs a passcode: set --os-passcode or OS_PASSCODE", t)
		}
		if noUser {
			return fmt.Errorf("auth type %s needs a user: set --os-username or --os-user-id", t)
		}
		clearToken()
		clearAppCred()
		if t == authTypeTOTP {
			ao.Password = ""
		}
	case authTypeAdminToken:
		if ao.TokenID == "" {
			return fmt.Errorf("auth type %s needs a token: set --%s or OS_TOKEN", t, flagOSToken)
		}
	case authTypeHTTPBasic:
		if noUser {
			return fmt.Errorf("auth type %s needs --os-username", t)
		}
	}
	return nil
}

// applyTrustScope requests a trust-scoped token for --os-trust-id, which
// replaces any project, domain or system scope, as keystoneauth's trust_id does.
func (o *Options) applyTrustScope(ao *gophercloud.AuthOptions) {
	if o.TrustID == "" {
		return
	}
	ao.Scope = &gophercloud.AuthScope{TrustID: o.TrustID}
	ao.TenantName, ao.TenantID = "", ""
}

// tokenAuth turns ao into a token request. A token already names its user, and
// gophercloud rejects any user field sent with one — including the user domain,
// which is also what qualifies a project named without a scope. So the scope is
// pinned first: with none, gophercloud would pass the token through unscoped
// and ignore the project, where keystoneauth's token plugin rescopes to it.
func tokenAuth(ao *gophercloud.AuthOptions) {
	if ao.Scope == nil {
		switch {
		case ao.TenantID != "":
			ao.Scope = &gophercloud.AuthScope{ProjectID: ao.TenantID}
		case ao.TenantName != "":
			ao.Scope = &gophercloud.AuthScope{ProjectName: ao.TenantName, DomainID: ao.DomainID, DomainName: ao.DomainName}
		}
	}
	ao.Username, ao.UserID, ao.Password, ao.DomainID, ao.DomainName = "", "", "", "", ""
}

package auth

import (
	"context"
	"crypto/tls"
	"fmt"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/config"
	"github.com/gophercloud/gophercloud/v2/openstack/config/clouds"
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
	if err := o.validateSystemScope(); err != nil {
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
	ao.AllowReauth = true

	tlsCfg, insecure, err := o.resolveTLSConfig(baseTLS)
	if err != nil {
		return nil, err
	}
	if insecure {
		warnInsecure("the OpenStack API (--insecure)")
	}
	warnCleartext("the OpenStack identity endpoint", ao.IdentityEndpoint)

	// The HTTP client is built here and handed to gophercloud, rather than letting
	// config.WithTLSConfig build one, for three reasons: WithTLSConfig replaces
	// Transport wholesale (dropping the debug/timing wrappers and the proxy-aware
	// DefaultTransport clone), a zero-value http.Client has no timeout at all, and
	// NewProviderClient POSTs /v3/auth/tokens *inside* the call — so anything
	// attached afterwards cannot see the token request, the returned catalog, or an
	// authentication failure.
	provider, err := config.NewProviderClient(ctx, ao, config.WithHTTPClient(o.httpClient(tlsCfg)))
	if err != nil {
		return nil, fmt.Errorf("authenticating to OpenStack: %w", err)
	}

	return &Client{Provider: provider, Endpoint: eo, opts: o}, nil
}

// resolveAuth produces the AuthOptions, EndpointOpts and (optional) clouds.yaml
// TLS config, selecting the clouds.yaml path when a cloud name is present and
// otherwise falling back to OS_* environment variables. Explicit CLI flags then
// override individual auth fields.
func (o *Options) resolveAuth() (gophercloud.AuthOptions, gophercloud.EndpointOpts, *tls.Config, error) {
	var ao gophercloud.AuthOptions
	var eo gophercloud.EndpointOpts
	var baseTLS *tls.Config

	if o.Cloud != "" {
		var err error
		ao, eo, baseTLS, err = clouds.Parse(clouds.WithCloudName(o.Cloud))
		if err != nil {
			return ao, eo, nil, fmt.Errorf("loading cloud %q from clouds.yaml: %w", o.Cloud, err)
		}
	} else {
		// Build the auth options from OS_* / flags directly rather than via
		// gophercloud's AuthOptionsFromEnv, which only understands OS_DOMAIN_NAME
		// and rejects the standard split OS_USER_DOMAIN_NAME / OS_PROJECT_DOMAIN_NAME
		// openrc (wrongly demanding OS_PROJECT_ID when only a project name +
		// project domain are given). applyAuthOverrides and applyDomainScope below
		// populate ao from o's fields.
		if o.AuthURL == "" {
			return ao, eo, nil, fmt.Errorf("no credentials found: set --os-cloud, or OS_AUTH_URL and the related OS_* variables")
		}
		if o.Password == "" && o.AppCredID == "" && o.AppCredName == "" {
			return ao, eo, nil, fmt.Errorf("no credentials found: set OS_PASSWORD or application credentials (OS_APPLICATION_CREDENTIAL_ID/_SECRET)")
		}
		eo = gophercloud.EndpointOpts{Region: o.RegionName}
	}

	o.applyAuthOverrides(&ao)
	o.applyEndpointOverrides(&eo)
	return ao, eo, baseTLS, nil
}

// applyAuthOverrides layers explicitly-set auth flags over whatever the
// clouds.yaml / env path produced. Every value goes through Options.override so
// an env-derived default cannot outrank an explicitly named cloud.
func (o *Options) applyAuthOverrides(ao *gophercloud.AuthOptions) {
	setIf := func(dst *string, v string) {
		if v != "" {
			*dst = v
		}
	}
	setIf(&ao.IdentityEndpoint, o.override("os-auth-url", o.AuthURL))
	setIf(&ao.Username, o.override("os-username", o.Username))
	setIf(&ao.UserID, o.override("os-user-id", o.UserID))
	setIf(&ao.Password, o.override("os-password", o.Password))
	setIf(&ao.TenantName, o.override(flagOSProjectName, o.ProjectName))
	setIf(&ao.TenantID, o.override(flagOSProjectID, o.ProjectID))
	setIf(&ao.ApplicationCredentialID, o.override("os-application-credential-id", o.AppCredID))
	setIf(&ao.ApplicationCredentialName, o.override("os-application-credential-name", o.AppCredName))
	setIf(&ao.ApplicationCredentialSecret, o.override("os-application-credential-secret", o.AppCredSecret))

	o.applyDomainScope(ao)
	o.applySystemScope(ao)
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
	for _, name := range []string{flagOSProjectName, flagOSProjectID, "os-domain-name"} {
		if o.fs.Changed(name) {
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
	if o.override("os-system-scope", o.SystemScope) == "" {
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
func (o *Options) applyDomainScope(ao *gophercloud.AuthOptions) {
	userDomainName := o.override("os-user-domain-name", o.UserDomainName)
	projectDomainName := o.override("os-project-domain-name", o.ProjectDomainName)
	domainName := o.override("os-domain-name", o.DomainName)

	if userDomainName == "" && projectDomainName == "" && domainName == "" {
		return
	}

	// The user's identity domain qualifies the username/user-id. Prefer an
	// explicit user domain, then a lone --os-domain-name, then the project
	// domain (single-domain clouds set only one of these).
	if userDomain := firstNonEmpty(userDomainName, domainName, projectDomainName); userDomain != "" {
		ao.DomainName = userDomain
		ao.DomainID = ""
	}

	projectName := firstNonEmpty(o.override(flagOSProjectName, o.ProjectName), ao.TenantName)
	projectID := firstNonEmpty(o.override(flagOSProjectID, o.ProjectID), ao.TenantID)

	switch {
	case projectID != "":
		// Project-by-ID scope needs no domain qualifier.
		ao.Scope = &gophercloud.AuthScope{ProjectID: projectID}
	case projectName != "":
		// Project-by-name must be qualified by the project's own domain.
		ao.Scope = &gophercloud.AuthScope{
			ProjectName: projectName,
			DomainName:  firstNonEmpty(projectDomainName, userDomainName, domainName),
		}
	case domainName != "":
		// Domain-scoped token (no project).
		ao.Scope = &gophercloud.AuthScope{DomainName: domainName}
	}
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
	if region := o.override("os-region-name", o.RegionName); region != "" {
		eo.Region = region
	}
	switch o.override("os-interface", o.Interface) {
	case "public":
		eo.Availability = gophercloud.AvailabilityPublic
	case "internal":
		eo.Availability = gophercloud.AvailabilityInternal
	case "admin":
		eo.Availability = gophercloud.AvailabilityAdmin
	}
}

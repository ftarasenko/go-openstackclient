package auth

import (
	"context"
	"crypto/tls"
	"fmt"
	"os"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack"
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
}

// Authenticate builds a single authenticated ProviderClient following the
// documented precedence (clouds.yaml → OS_* env → application credentials) and
// wires the resolved TLS config into it.
func (o *Options) Authenticate(ctx context.Context) (*Client, error) {
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
		fmt.Fprintln(os.Stderr, "WARNING: TLS certificate verification is disabled (--insecure); connections are not secure")
	}

	provider, err := config.NewProviderClient(ctx, ao, config.WithTLSConfig(tlsCfg))
	if err != nil {
		return nil, fmt.Errorf("authenticating to OpenStack: %w", err)
	}
	if o.Debug {
		provider.HTTPClient.Transport = newDebugTransport(provider.HTTPClient.Transport)
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
		var err error
		ao, err = openstack.AuthOptionsFromEnv()
		if err != nil {
			return ao, eo, nil, fmt.Errorf("reading credentials from environment: %w", err)
		}
		eo = gophercloud.EndpointOpts{Region: o.RegionName}
	}

	o.applyAuthOverrides(&ao)
	o.applyEndpointOverrides(&eo)
	return ao, eo, baseTLS, nil
}

// applyAuthOverrides layers explicitly-set auth flags over whatever the
// clouds.yaml / env path produced.
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
	setIf(&ao.TenantName, o.ProjectName)
	setIf(&ao.TenantID, o.ProjectID)
	setIf(&ao.ApplicationCredentialID, o.AppCredID)
	setIf(&ao.ApplicationCredentialName, o.AppCredName)
	setIf(&ao.ApplicationCredentialSecret, o.AppCredSecret)

	o.applyDomainScope(ao)
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
	if o.UserDomainName == "" && o.ProjectDomainName == "" && o.DomainName == "" {
		return
	}

	// The user's identity domain qualifies the username/user-id. Prefer an
	// explicit user domain, then a lone --os-domain-name, then the project
	// domain (single-domain clouds set only one of these).
	if userDomain := firstNonEmpty(o.UserDomainName, o.DomainName, o.ProjectDomainName); userDomain != "" {
		ao.DomainName = userDomain
		ao.DomainID = ""
	}

	projectName := firstNonEmpty(o.ProjectName, ao.TenantName)
	projectID := firstNonEmpty(o.ProjectID, ao.TenantID)

	switch {
	case projectID != "":
		// Project-by-ID scope needs no domain qualifier.
		ao.Scope = &gophercloud.AuthScope{ProjectID: projectID}
	case projectName != "":
		// Project-by-name must be qualified by the project's own domain.
		ao.Scope = &gophercloud.AuthScope{
			ProjectName: projectName,
			DomainName:  firstNonEmpty(o.ProjectDomainName, o.UserDomainName, o.DomainName),
		}
	case o.DomainName != "":
		// Domain-scoped token (no project).
		ao.Scope = &gophercloud.AuthScope{DomainName: o.DomainName}
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
	if o.RegionName != "" {
		eo.Region = o.RegionName
	}
	switch o.Interface {
	case "public":
		eo.Availability = gophercloud.AvailabilityPublic
	case "internal":
		eo.Availability = gophercloud.AvailabilityInternal
	case "admin":
		eo.Availability = gophercloud.AvailabilityAdmin
	}
}

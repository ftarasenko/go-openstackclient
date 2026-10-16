package auth

import (
	"crypto/tls"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
)

// Endpoint overrides and the Keystone-less auth types, as openstacksdk models
// them (openstack/config/cloud_region.py CloudRegion.get_endpoint):
//
//   - a per-service override — clouds.yaml <service>_endpoint_override (or
//     <service>_endpoint), env OS_<SERVICE>_ENDPOINT_OVERRIDE (or
//     OS_<SERVICE>_ENDPOINT) — replaces that service's catalog entry, keyed by
//     its service type or any alias (compute, block_storage / volumev3,
//     baremetal, …);
//   - the auth types none / noauth, admin_token / token_endpoint and
//     http_basic authenticate against no Keystone at all, so every service URL
//     comes from an override or, failing that, from --os-endpoint /
//     OS_ENDPOINT (auth.endpoint in clouds.yaml).
//
// Both are wired in through the provider's EndpointLocator, which every
// gophercloud service constructor consults, so the per-service factories in
// services.go need no knowledge of them.

// keystoneless reports whether kind authenticates without Keystone.
func keystoneless(kind string) bool {
	switch kind {
	case authTypeNone, authTypeAdminToken, authTypeHTTPBasic:
		return true
	}
	return false
}

// overrideKey is the config-key spelling of a service type: lower case, with
// dashes as underscores ("block-storage" → "block_storage").
func overrideKey(serviceType string) string {
	return strings.ToLower(strings.ReplaceAll(serviceType, "-", "_"))
}

// collectEndpointOverrides merges the clouds.yaml overrides with the
// environment's, the environment winning as it does for every other setting.
// OS_<X>_ENDPOINT_OVERRIDE beats OS_<X>_ENDPOINT, as the clouds.yaml spellings
// do.
func collectEndpointOverrides(fromCloud map[string]string) map[string]string {
	out := make(map[string]string, len(fromCloud))
	for k, v := range fromCloud {
		out[overrideKey(k)] = v
	}
	var plain, override [][2]string
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		name, ok := strings.CutPrefix(k, "OS_")
		if !ok || v == "" {
			continue
		}
		if svc, ok := strings.CutSuffix(name, "_ENDPOINT_OVERRIDE"); ok && svc != "" {
			override = append(override, [2]string{overrideKey(svc), v})
		} else if svc, ok := strings.CutSuffix(name, "_ENDPOINT"); ok && svc != "" {
			plain = append(plain, [2]string{overrideKey(svc), v})
		}
	}
	for _, kv := range append(plain, override...) {
		out[kv[0]] = kv[1]
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// lookupOverride finds the override for the service eo describes, under its
// type or any of its aliases.
func (o *Options) lookupOverride(eo gophercloud.EndpointOpts) (string, bool) {
	for _, t := range append([]string{eo.Type}, eo.Aliases...) {
		if u, ok := o.endpointOverrides[overrideKey(t)]; ok {
			return gophercloud.NormalizeURL(u), true
		}
	}
	return "", false
}

// endpointLocator wraps next — the catalog lookup, or nil for a Keystone-less
// auth type — with the overrides.
func (o *Options) endpointLocator(next gophercloud.EndpointLocator) gophercloud.EndpointLocator {
	return func(eo gophercloud.EndpointOpts) (string, error) {
		if u, ok := o.lookupOverride(eo); ok {
			return u, nil
		}
		if next != nil {
			return next(eo)
		}
		if o.Endpoint != "" {
			return gophercloud.NormalizeURL(o.Endpoint), nil
		}
		return "", fmt.Errorf("auth type %s has no service catalog: set --os-endpoint / OS_ENDPOINT, or OS_%s_ENDPOINT_OVERRIDE",
			o.authKind, strings.ToUpper(overrideKey(eo.Type)))
	}
}

// endpointHosts lists the hosts the Keystone-less service URLs point at, the
// only ones http_basic sends its credentials to.
func (o *Options) endpointHosts() []string {
	var hosts []string
	for _, raw := range append([]string{o.Endpoint}, mapValues(o.endpointOverrides)...) {
		if u, err := url.Parse(raw); err == nil && u.Host != "" {
			hosts = append(hosts, u.Host)
		}
	}
	return hosts
}

func mapValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

// keystonelessClient builds the Client for none, admin_token and http_basic: a
// provider with no Keystone token request behind it, whose every endpoint
// comes from endpointLocator.
func (o *Options) keystonelessClient(ao gophercloud.AuthOptions, eo gophercloud.EndpointOpts, tlsCfg *tls.Config) *Client {
	hc := o.httpClient(tlsCfg)
	if o.authKind == authTypeHTTPBasic {
		// Basic auth sits inside the debug/timing wrappers, so --debug never
		// sees the Authorization header it adds (redact covers it anyway).
		hc.Transport = o.instrument(&basicAuthTransport{
			base:     newHTTPTransport(tlsCfg),
			hosts:    o.endpointHosts(),
			username: ao.Username,
			password: ao.Password,
		})
	}
	pc := &gophercloud.ProviderClient{HTTPClient: hc}
	pc.UserAgent.Prepend("koc")
	if o.authKind == authTypeAdminToken {
		pc.SetToken(ao.TokenID)
	}
	pc.EndpointLocator = o.endpointLocator(nil)
	return &Client{Provider: pc, Endpoint: eo, opts: o}
}

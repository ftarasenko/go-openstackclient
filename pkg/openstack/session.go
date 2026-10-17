package openstack

import (
	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/tokens"
)

// DefaultMicroversion is the microversion a Session sends to every
// microversioned service (compute, volume, baremetal, placement) unless
// [Microversions] names another. It asks the endpoint for the highest
// microversion it supports, so the same code runs on every release koc
// supports, oldest to newest.
const DefaultMicroversion = "latest"

// Microversions pins the microversion sent to each microversioned service. An
// empty field means [DefaultMicroversion]. A value is sent as given ("2.79",
// "latest"); it is not validated here, so an unsupported one surfaces as the
// service's own error.
type Microversions struct {
	Compute   string
	Volume    string
	Baremetal string
	Placement string
}

// Options are the settings a Session applies to the service clients it
// builds. The zero value is the koc CLI's default behaviour.
type Options struct {
	Microversions Microversions

	// KeyVRMEndpoint, when set, is the KeyVRM base URL, used instead of the
	// catalog entry of type "keyvrm".
	KeyVRMEndpoint string
}

// Session is one authenticated ProviderClient plus the endpoint options its
// service clients are resolved with. It is the factory for every per-service
// gophercloud client koc uses.
//
// A Session is immutable once built and safe for concurrent use; the
// ProviderClient underneath is shared, so a token refresh on one goroutine is
// seen by all of them.
type Session struct {
	provider *gophercloud.ProviderClient
	endpoint gophercloud.EndpointOpts
	opts     Options
}

// FromProvider wraps a ProviderClient the caller authenticated itself. eo
// selects the region and interface the service clients are resolved with;
// its Type and Name are ignored, since each constructor names its own
// service.
func FromProvider(pc *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, opts Options) *Session {
	return &Session{provider: pc, endpoint: eo, opts: opts}
}

// ForRegion returns a Session for another region of the same cloud. It shares
// this Session's ProviderClient — one token, one re-authentication — and
// differs only in the region its catalog lookups ask for.
func (s *Session) ForRegion(region string) *Session {
	c := *s
	c.endpoint.Region = region
	return &c
}

// Provider is the authenticated ProviderClient.
func (s *Session) Provider() *gophercloud.ProviderClient { return s.provider }

// EndpointOpts is the region and interface service clients are resolved with.
func (s *Session) EndpointOpts() gophercloud.EndpointOpts { return s.endpoint }

// Region is the region service clients are resolved in ("" for the catalog's
// only or first one, as gophercloud does).
func (s *Session) Region() string { return s.endpoint.Region }

// Options are the settings this Session applies to the clients it builds.
func (s *Session) Options() Options { return s.opts }

// ScopedProjectID is the project the session's token is scoped to, read from
// the authentication result Keystone returned: "" for a domain- or
// system-scoped token, and for a ProviderClient that did not authenticate
// against Keystone.
func (s *Session) ScopedProjectID() string {
	if s == nil || s.provider == nil {
		return ""
	}
	r, ok := s.provider.GetAuthResult().(interface {
		ExtractProject() (*tokens.Project, error)
	})
	if !ok {
		return ""
	}
	p, err := r.ExtractProject()
	if err != nil || p == nil {
		return ""
	}
	return p.ID
}

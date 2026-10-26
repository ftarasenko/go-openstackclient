package openstack

import (
	"cmp"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack"
)

// The per-service constructors. Each returns a gophercloud ServiceClient whose
// Type is what gophercloud keys the microversion header on (ironic →
// X-OpenStack-Ironic-API-Version; nova, cinder, placement → the generic
// OpenStack-API-Version), with Microversion set for the services that use one
// and left empty for those that version by URL (identity, image, network, dns,
// load-balancer, the inspector).
//
// A failure to find the service in the catalog is a *[ServiceError].

// Baremetal returns an ironic (baremetal v1) client.
func (s *Session) Baremetal() (*gophercloud.ServiceClient, error) {
	sc, err := openstack.NewBareMetalV1(s.provider, s.endpoint)
	if err != nil {
		return nil, wrapService("baremetal", err)
	}
	sc.Microversion = cmp.Or(s.opts.Microversions.Baremetal, DefaultMicroversion)
	return sc, nil
}

// Introspection returns an ironic-inspector (baremetal-introspection v1)
// client. It is a separate catalog entry from ironic's own.
func (s *Session) Introspection() (*gophercloud.ServiceClient, error) {
	sc, err := openstack.NewBareMetalIntrospectionV1(s.provider, s.endpoint)
	if err != nil {
		return nil, wrapService("baremetal-introspection", err)
	}
	return sc, nil
}

// Compute returns a nova (compute v2) client.
func (s *Session) Compute() (*gophercloud.ServiceClient, error) {
	sc, err := openstack.NewComputeV2(s.provider, s.endpoint)
	if err != nil {
		return nil, wrapService("compute", err)
	}
	sc.Microversion = cmp.Or(s.opts.Microversions.Compute, DefaultMicroversion)
	return sc, nil
}

// Identity returns a keystone (identity v3) client.
func (s *Session) Identity() (*gophercloud.ServiceClient, error) {
	sc, err := openstack.NewIdentityV3(s.provider, s.endpoint)
	if err != nil {
		return nil, wrapService("identity", err)
	}
	return sc, nil
}

// Volume returns a cinder (block-storage v3) client.
func (s *Session) Volume() (*gophercloud.ServiceClient, error) {
	sc, err := openstack.NewBlockStorageV3(s.provider, s.endpoint)
	if err != nil {
		return nil, wrapService("volume", err)
	}
	sc.Microversion = cmp.Or(s.opts.Microversions.Volume, DefaultMicroversion)
	return sc, nil
}

// DNS returns a designate (dns v2) client.
func (s *Session) DNS() (*gophercloud.ServiceClient, error) {
	sc, err := openstack.NewDNSV2(s.provider, s.endpoint)
	if err != nil {
		return nil, wrapService("dns", err)
	}
	return sc, nil
}

// Image returns a glance (image v2) client.
func (s *Session) Image() (*gophercloud.ServiceClient, error) {
	sc, err := openstack.NewImageV2(s.provider, s.endpoint)
	if err != nil {
		return nil, wrapService("image", err)
	}
	return sc, nil
}

// Network returns a neutron (network v2) client.
func (s *Session) Network() (*gophercloud.ServiceClient, error) {
	sc, err := openstack.NewNetworkV2(s.provider, s.endpoint)
	if err != nil {
		return nil, wrapService("network", err)
	}
	return sc, nil
}

// Placement returns a placement (v1) client.
func (s *Session) Placement() (*gophercloud.ServiceClient, error) {
	sc, err := openstack.NewPlacementV1(s.provider, s.endpoint)
	if err != nil {
		return nil, wrapService("placement", err)
	}
	sc.Microversion = cmp.Or(s.opts.Microversions.Placement, DefaultMicroversion)
	return sc, nil
}

// LoadBalancer returns an octavia (load-balancer v2) client. The catalog type
// is "load-balancer", not "octavia".
func (s *Session) LoadBalancer() (*gophercloud.ServiceClient, error) {
	sc, err := openstack.NewLoadBalancerV2(s.provider, s.endpoint)
	if err != nil {
		return nil, wrapService("load-balancer", err)
	}
	return sc, nil
}

// KeyManagerMicroversion is the barbican microversion a [Session.KeyManager]
// client sends. 1.1 adds secret consumers and is the highest microversion
// barbican defines from Zed (15.0.0) through 2025.2 (21.0.0). It is pinned
// rather than negotiated because 1.2 changes what a DELETE of a secret with
// consumers does: below it the delete succeeds, at it the server refuses
// unless force is asked for.
const KeyManagerMicroversion = "1.1"

// KeyManager returns a barbican (key-manager v1) client, rooted at its v1 API
// and pinned to [KeyManagerMicroversion].
func (s *Session) KeyManager() (*gophercloud.ServiceClient, error) {
	sc, err := openstack.NewKeyManagerV1(s.provider, s.endpoint)
	if err != nil {
		return nil, wrapService("key-manager", err)
	}
	sc.Microversion = KeyManagerMicroversion
	return sc, nil
}

// KeyVRMServiceType is the Keystone catalog service type of KeyVRM.
const KeyVRMServiceType = "keyvrm"

// KeyVRM returns a client for KeyVRM, the KeyStack virtual resource manager,
// rooted at its v1 API. The endpoint is [Options.KeyVRMEndpoint] when set, and
// otherwise the catalog entry of type [KeyVRMServiceType]. KeyVRM takes the
// plain Keystone token and has no microversions. Package
// github.com/ftarasenko/go-openstackclient/pkg/keyvrm holds its requests.
func (s *Session) KeyVRM() (*gophercloud.ServiceClient, error) {
	if s.opts.KeyVRMEndpoint != "" {
		return s.keyvrmClientAt(s.opts.KeyVRMEndpoint), nil
	}
	eo := s.endpoint
	eo.Type = KeyVRMServiceType
	eo.ApplyDefaults(KeyVRMServiceType)
	url, err := s.provider.EndpointLocator(eo)
	if err != nil {
		return nil, wrapService(KeyVRMServiceType, err)
	}
	return s.keyvrmClientAt(url), nil
}

// keyvrmClientAt builds a KeyVRM client rooted at url, with the v1 API under
// ResourceBase.
func (s *Session) keyvrmClientAt(url string) *gophercloud.ServiceClient {
	if !strings.HasSuffix(url, "/") {
		url += "/"
	}
	return &gophercloud.ServiceClient{
		ProviderClient: s.provider,
		Endpoint:       url,
		ResourceBase:   url + "v1/",
		Type:           KeyVRMServiceType,
	}
}

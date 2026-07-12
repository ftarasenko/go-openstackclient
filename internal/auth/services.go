package auth

import (
	"context"
	"fmt"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack"
)

// NewServiceClient authenticates once (clouds.yaml / OS_* / --creds-from-*) and
// derives a single service client via derive — one of the Client factory
// methods below, passed as a method value, e.g.
// a.NewServiceClient(ctx, (*auth.Client).Compute). It is the shared body behind
// every command package's newXxxClient helper.
func (o *Options) NewServiceClient(ctx context.Context,
	derive func(*Client) (*gophercloud.ServiceClient, error),
) (*gophercloud.ServiceClient, error) {
	client, err := o.Authenticate(ctx)
	if err != nil {
		return nil, err
	}
	return derive(client)
}

// NewServiceSession is NewServiceClient for commands that also need the
// authenticated Client to lazily derive a second service (cross-service name→ID
// resolution), e.g. `server create --image`/`--network`.
func (o *Options) NewServiceSession(ctx context.Context,
	derive func(*Client) (*gophercloud.ServiceClient, error),
) (*gophercloud.ServiceClient, *Client, error) {
	client, err := o.Authenticate(ctx)
	if err != nil {
		return nil, nil, err
	}
	sc, err := derive(client)
	if err != nil {
		return nil, nil, err
	}
	return sc, client, nil
}

// This file is the single place per-service clients are derived from the one
// authenticated ProviderClient. Each factory sets the service microversion
// where the API uses one; gophercloud emits the correct header from
// client.Type (ironic → X-OpenStack-Ironic-API-Version; nova/cinder/placement →
// OpenStack-API-Version).

// defaultPlacementMicroversion negotiates the latest placement microversion the
// endpoint supports. Placement accepts the literal "latest".
const defaultPlacementMicroversion = "latest"

// Baremetal returns an ironic (baremetal v1) service client. In --creds-from-ns
// mode this is the standalone, basic-auth client built from the Kubernetes
// secret rather than a Keystone-catalog endpoint.
func (c *Client) Baremetal() (*gophercloud.ServiceClient, error) {
	if c.ironic != nil {
		sc, err := c.ironic.baremetalClient(c.opts.BaremetalAPIVersion)
		if err != nil {
			return nil, wrapService("baremetal", err)
		}
		return sc, nil
	}
	sc, err := openstack.NewBareMetalV1(c.Provider, c.Endpoint)
	if err != nil {
		return nil, wrapService("baremetal", err)
	}
	sc.Microversion = c.opts.BaremetalAPIVersion
	return sc, nil
}

// requireKeystone rejects a non-baremetal service in --creds-from-ns mode, where
// only standalone Ironic credentials are available.
func (c *Client) requireKeystone(service string) error {
	if c.ironic != nil {
		return fmt.Errorf("--creds-from-ns provides baremetal (ironic) credentials only; %s is not available", service)
	}
	return nil
}

// Compute returns a nova (compute v2) service client.
func (c *Client) Compute() (*gophercloud.ServiceClient, error) {
	if err := c.requireKeystone("compute"); err != nil {
		return nil, err
	}
	sc, err := openstack.NewComputeV2(c.Provider, c.Endpoint)
	if err != nil {
		return nil, wrapService("compute", err)
	}
	sc.Microversion = c.opts.ComputeAPIVersion
	return sc, nil
}

// Identity returns a keystone (identity v3) service client.
func (c *Client) Identity() (*gophercloud.ServiceClient, error) {
	if err := c.requireKeystone("identity"); err != nil {
		return nil, err
	}
	sc, err := openstack.NewIdentityV3(c.Provider, c.Endpoint)
	if err != nil {
		return nil, wrapService("identity", err)
	}
	return sc, nil
}

// Volume returns a cinder (block-storage v3) service client.
func (c *Client) Volume() (*gophercloud.ServiceClient, error) {
	if err := c.requireKeystone("volume"); err != nil {
		return nil, err
	}
	sc, err := openstack.NewBlockStorageV3(c.Provider, c.Endpoint)
	if err != nil {
		return nil, wrapService("volume", err)
	}
	sc.Microversion = c.opts.VolumeAPIVersion
	return sc, nil
}

// DNS returns a designate (dns v2) service client.
func (c *Client) DNS() (*gophercloud.ServiceClient, error) {
	if err := c.requireKeystone("dns"); err != nil {
		return nil, err
	}
	sc, err := openstack.NewDNSV2(c.Provider, c.Endpoint)
	if err != nil {
		return nil, wrapService("dns", err)
	}
	return sc, nil
}

// Image returns a glance (image v2) service client.
func (c *Client) Image() (*gophercloud.ServiceClient, error) {
	if err := c.requireKeystone("image"); err != nil {
		return nil, err
	}
	sc, err := openstack.NewImageV2(c.Provider, c.Endpoint)
	if err != nil {
		return nil, wrapService("image", err)
	}
	return sc, nil
}

// Network returns a neutron (network v2) service client.
func (c *Client) Network() (*gophercloud.ServiceClient, error) {
	if err := c.requireKeystone("network"); err != nil {
		return nil, err
	}
	sc, err := openstack.NewNetworkV2(c.Provider, c.Endpoint)
	if err != nil {
		return nil, wrapService("network", err)
	}
	return sc, nil
}

// Placement returns a placement (v1) service client. Placement uses the generic
// OpenStack-API-Version header keyed on client.Type == "placement".
func (c *Client) Placement() (*gophercloud.ServiceClient, error) {
	if err := c.requireKeystone("placement"); err != nil {
		return nil, err
	}
	sc, err := openstack.NewPlacementV1(c.Provider, c.Endpoint)
	if err != nil {
		return nil, wrapService("placement", err)
	}
	sc.Microversion = defaultPlacementMicroversion
	return sc, nil
}

// keyvrmServiceType is the Keystone catalog service type for KeyVRM.
const keyvrmServiceType = "keyvrm"

// KeyVRM returns a client for the in-house KeyVRM service. Its endpoint is
// resolved from the Keystone catalog (type "keyvrm"), unless an override is set.
// KeyVRM authenticates with the standard Keystone token, so no microversion or
// custom auth is involved.
func (c *Client) KeyVRM() (*gophercloud.ServiceClient, error) {
	if err := c.requireKeystone(keyvrmServiceType); err != nil {
		return nil, err
	}
	if c.opts.KeyVRMEndpoint != "" {
		return c.keyvrmClientAt(c.opts.KeyVRMEndpoint), nil
	}
	eo := c.Endpoint
	eo.Type = keyvrmServiceType
	eo.ApplyDefaults(keyvrmServiceType)
	url, err := c.Provider.EndpointLocator(eo)
	if err != nil {
		return nil, wrapService(keyvrmServiceType, err)
	}
	return c.keyvrmClientAt(url), nil
}

// keyvrmClientAt builds a KeyVRM service client rooted at url, exposing the v1
// API under ResourceBase.
func (c *Client) keyvrmClientAt(url string) *gophercloud.ServiceClient {
	if !strings.HasSuffix(url, "/") {
		url += "/"
	}
	return &gophercloud.ServiceClient{
		ProviderClient: c.Provider,
		Endpoint:       url,
		ResourceBase:   url + "v1/",
		Type:           keyvrmServiceType,
	}
}

func wrapService(name string, err error) error {
	return &ServiceError{Service: name, Err: err}
}

// ServiceError wraps a failure while constructing a service client, keeping the
// service name for a clear, consistent error message.
type ServiceError struct {
	Service string
	Err     error
}

func (e *ServiceError) Error() string {
	return "creating " + e.Service + " client: " + e.Err.Error()
}

func (e *ServiceError) Unwrap() error { return e.Err }

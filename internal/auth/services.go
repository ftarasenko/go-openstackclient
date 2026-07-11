package auth

import (
	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack"
)

// This file is the single place per-service clients are derived from the one
// authenticated ProviderClient. Each factory sets the service microversion
// where the API uses one; gophercloud emits the correct header from
// client.Type (ironic → X-OpenStack-Ironic-API-Version; nova/cinder/placement →
// OpenStack-API-Version).

// defaultPlacementMicroversion negotiates the latest placement microversion the
// endpoint supports. Placement accepts the literal "latest".
const defaultPlacementMicroversion = "latest"

// Baremetal returns an ironic (baremetal v1) service client.
func (c *Client) Baremetal() (*gophercloud.ServiceClient, error) {
	sc, err := openstack.NewBareMetalV1(c.Provider, c.Endpoint)
	if err != nil {
		return nil, wrapService("baremetal", err)
	}
	sc.Microversion = c.opts.BaremetalAPIVersion
	return sc, nil
}

// Compute returns a nova (compute v2) service client.
func (c *Client) Compute() (*gophercloud.ServiceClient, error) {
	sc, err := openstack.NewComputeV2(c.Provider, c.Endpoint)
	if err != nil {
		return nil, wrapService("compute", err)
	}
	sc.Microversion = c.opts.ComputeAPIVersion
	return sc, nil
}

// Identity returns a keystone (identity v3) service client.
func (c *Client) Identity() (*gophercloud.ServiceClient, error) {
	sc, err := openstack.NewIdentityV3(c.Provider, c.Endpoint)
	if err != nil {
		return nil, wrapService("identity", err)
	}
	return sc, nil
}

// Volume returns a cinder (block-storage v3) service client.
func (c *Client) Volume() (*gophercloud.ServiceClient, error) {
	sc, err := openstack.NewBlockStorageV3(c.Provider, c.Endpoint)
	if err != nil {
		return nil, wrapService("volume", err)
	}
	sc.Microversion = c.opts.VolumeAPIVersion
	return sc, nil
}

// DNS returns a designate (dns v2) service client.
func (c *Client) DNS() (*gophercloud.ServiceClient, error) {
	sc, err := openstack.NewDNSV2(c.Provider, c.Endpoint)
	if err != nil {
		return nil, wrapService("dns", err)
	}
	return sc, nil
}

// Image returns a glance (image v2) service client.
func (c *Client) Image() (*gophercloud.ServiceClient, error) {
	sc, err := openstack.NewImageV2(c.Provider, c.Endpoint)
	if err != nil {
		return nil, wrapService("image", err)
	}
	return sc, nil
}

// Network returns a neutron (network v2) service client.
func (c *Client) Network() (*gophercloud.ServiceClient, error) {
	sc, err := openstack.NewNetworkV2(c.Provider, c.Endpoint)
	if err != nil {
		return nil, wrapService("network", err)
	}
	return sc, nil
}

// Placement returns a placement (v1) service client. Placement uses the generic
// OpenStack-API-Version header keyed on client.Type == "placement".
func (c *Client) Placement() (*gophercloud.ServiceClient, error) {
	sc, err := openstack.NewPlacementV1(c.Provider, c.Endpoint)
	if err != nil {
		return nil, wrapService("placement", err)
	}
	sc.Microversion = defaultPlacementMicroversion
	return sc, nil
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

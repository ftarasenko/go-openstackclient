package auth

import (
	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack"
)

// Baremetal returns an ironic (baremetal v1) service client with the configured
// microversion. gophercloud emits the ironic-specific
// X-OpenStack-Ironic-API-Version header from client.Microversion.
func (c *Client) Baremetal() (*gophercloud.ServiceClient, error) {
	sc, err := openstack.NewBareMetalV1(c.Provider, c.Endpoint)
	if err != nil {
		return nil, wrapService("baremetal", err)
	}
	sc.Microversion = c.opts.BaremetalAPIVersion
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

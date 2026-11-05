package identity

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/endpoints"
)

// ListEndpoints lists the endpoints opts selects.
func ListEndpoints(ctx context.Context, c *gophercloud.ServiceClient, opts endpoints.ListOpts) ([]endpoints.Endpoint, error) {
	return listAll(ctx, endpoints.List(c, opts), endpoints.ExtractEndpoints, "listing endpoints")
}

// GetEndpoint reads one endpoint by ID.
func GetEndpoint(ctx context.Context, c *gophercloud.ServiceClient, id string) (*endpoints.Endpoint, error) {
	e, err := endpoints.Get(ctx, c, id).Extract()
	return e, wrap(err, "showing endpoint %s", id)
}

// CreateEndpoint creates an endpoint.
func CreateEndpoint(ctx context.Context, c *gophercloud.ServiceClient, opts endpoints.CreateOpts) (*endpoints.Endpoint, error) {
	e, err := endpoints.Create(ctx, c, opts).Extract()
	return e, wrap(err, "creating endpoint")
}

// UpdateEndpoint changes the fields opts sets on an endpoint.
func UpdateEndpoint(ctx context.Context, c *gophercloud.ServiceClient, id string, opts endpoints.UpdateOpts) (*endpoints.Endpoint, error) {
	e, err := endpoints.Update(ctx, c, id, opts).Extract()
	return e, wrap(err, "updating endpoint %s", id)
}

// DeleteEndpoint deletes an endpoint.
func DeleteEndpoint(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(endpoints.Delete(ctx, c, id).ExtractErr(), "deleting endpoint %s", id)
}

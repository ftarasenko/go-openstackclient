package identity

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/services"
)

// ListServices lists the catalog services opts selects. Keystone filters
// services by type only; a name has to be matched on the result.
func ListServices(ctx context.Context, c *gophercloud.ServiceClient, opts services.ListOpts) ([]services.Service, error) {
	return listAll(ctx, services.List(c, opts), services.ExtractServices, "listing services")
}

// GetService reads one catalog service by ID.
func GetService(ctx context.Context, c *gophercloud.ServiceClient, id string) (*services.Service, error) {
	s, err := services.Get(ctx, c, id).Extract()
	return s, wrap(err, "showing service %s", id)
}

// CreateService creates a catalog service.
func CreateService(ctx context.Context, c *gophercloud.ServiceClient, opts services.CreateOpts) (*services.Service, error) {
	s, err := services.Create(ctx, c, opts).Extract()
	return s, wrap(err, "creating service of type %q", opts.Type)
}

// UpdateService changes the fields opts sets on a catalog service.
func UpdateService(ctx context.Context, c *gophercloud.ServiceClient, id string, opts services.UpdateOpts) (*services.Service, error) {
	s, err := services.Update(ctx, c, id, opts).Extract()
	return s, wrap(err, "updating service %s", id)
}

// DeleteService deletes a catalog service.
func DeleteService(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(services.Delete(ctx, c, id).ExtractErr(), "deleting service %s", id)
}

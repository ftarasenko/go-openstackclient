package identity

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/regions"
)

// ListRegions lists the regions opts selects.
func ListRegions(ctx context.Context, c *gophercloud.ServiceClient, opts regions.ListOpts) ([]regions.Region, error) {
	return listAll(ctx, regions.List(c, opts), regions.ExtractRegions, "listing regions")
}

// GetRegion reads one region by ID.
func GetRegion(ctx context.Context, c *gophercloud.ServiceClient, id string) (*regions.Region, error) {
	r, err := regions.Get(ctx, c, id).Extract()
	return r, wrap(err, "showing region %s", id)
}

// CreateRegion creates a region. A region's ID is its name, so opts.ID is
// usually set; keystone generates one when it is not.
func CreateRegion(ctx context.Context, c *gophercloud.ServiceClient, opts regions.CreateOpts) (*regions.Region, error) {
	r, err := regions.Create(ctx, c, opts).Extract()
	return r, wrap(err, "creating region %q", opts.ID)
}

// UpdateRegion changes the fields opts sets on a region.
func UpdateRegion(ctx context.Context, c *gophercloud.ServiceClient, id string, opts regions.UpdateOpts) (*regions.Region, error) {
	r, err := regions.Update(ctx, c, id, opts).Extract()
	return r, wrap(err, "updating region %s", id)
}

// DeleteRegion deletes a region.
func DeleteRegion(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(regions.Delete(ctx, c, id).ExtractErr(), "deleting region %s", id)
}

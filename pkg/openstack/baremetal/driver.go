package baremetal

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/baremetal/v1/drivers"
)

// ListDrivers lists the enabled drivers.
func ListDrivers(ctx context.Context, c *gophercloud.ServiceClient, opts drivers.ListDriversOptsBuilder) ([]drivers.Driver, error) {
	pages, err := drivers.ListDrivers(c, opts).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing baremetal drivers")
	}
	all, err := drivers.ExtractDrivers(pages)
	return all, wrap(err, "parsing baremetal driver list")
}

// GetDriver reads one driver's details.
func GetDriver(ctx context.Context, c *gophercloud.ServiceClient, name string) (*drivers.Driver, error) {
	d, err := drivers.GetDriverDetails(ctx, c, name).Extract()
	return d, wrap(err, "showing baremetal driver %s", name)
}

// GetDriverProperties reads the properties a driver accepts on a node.
func GetDriverProperties(ctx context.Context, c *gophercloud.ServiceClient, name string) (*drivers.DriverProperties, error) {
	p, err := drivers.GetDriverProperties(ctx, c, name).Extract()
	return p, wrap(err, "listing properties of baremetal driver %s", name)
}

// GetDriverRAIDProperties reads a driver's RAID logical disk properties.
func GetDriverRAIDProperties(ctx context.Context, c *gophercloud.ServiceClient, name string) (*drivers.DiskProperties, error) {
	p, err := drivers.GetDriverDiskProperties(ctx, c, name).Extract()
	return p, wrap(err, "listing RAID properties of baremetal driver %s", name)
}

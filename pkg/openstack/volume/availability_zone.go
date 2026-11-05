package volume

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/availabilityzones"
)

// ListAvailabilityZones lists cinder's availability zones.
func ListAvailabilityZones(ctx context.Context, c *gophercloud.ServiceClient) ([]availabilityzones.AvailabilityZone, error) {
	pages, err := availabilityzones.List(c).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing volume availability zones")
	}
	list, err := availabilityzones.ExtractAvailabilityZones(pages)
	return list, wrap(err, "listing volume availability zones")
}

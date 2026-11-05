package compute

import (
	"context"
	"net/http"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/availabilityzones"
)

// ListAvailabilityZones lists nova's availability zones with their hosts and
// services. Where policy denies the detailed listing to the caller, it falls
// back to the plain one, whose zones carry no hosts.
func ListAvailabilityZones(ctx context.Context, c *gophercloud.ServiceClient) ([]availabilityzones.AvailabilityZone, error) {
	pages, err := availabilityzones.ListDetail(c).AllPages(ctx)
	if gophercloud.ResponseCodeIs(err, http.StatusForbidden) {
		pages, err = availabilityzones.List(c).AllPages(ctx)
	}
	if err != nil {
		return nil, wrap(err, "listing compute availability zones")
	}
	list, err := availabilityzones.ExtractAvailabilityZones(pages)
	return list, wrap(err, "listing compute availability zones")
}

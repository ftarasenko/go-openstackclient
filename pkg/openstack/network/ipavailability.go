package network

import (
	"context"
	"fmt"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/networkipavailabilities"
)

// IPAvailability is one network's IP availability plus
// ip_availability_details (total_ips_in_subnet, used_ips_in_allocation_pool
// and the like), which the network-ip-availability-details extension adds
// and gophercloud does not model; Details is nil on a cloud without it.
// networkipavailabilities.NetworkIPAvailability has its own UnmarshalJSON,
// which an IPAvailability inherits, so ListIPAvailabilities decodes the page
// twice.
type IPAvailability struct {
	networkipavailabilities.NetworkIPAvailability
	Details map[string]any `json:"-"`
}

// ListIPAvailabilities lists the IP availability of every network matching
// opts.
func ListIPAvailabilities(ctx context.Context, c *gophercloud.ServiceClient, opts networkipavailabilities.ListOptsBuilder) ([]IPAvailability, error) {
	pages, err := networkipavailabilities.List(c, opts).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing IP availability")
	}
	base, err := networkipavailabilities.ExtractNetworkIPAvailabilities(pages)
	if err != nil {
		return nil, wrap(err, "parsing the IP availability list")
	}
	page, ok := pages.(networkipavailabilities.NetworkIPAvailabilityPage)
	if !ok {
		return nil, fmt.Errorf("parsing the IP availability list: unexpected page type %T", pages)
	}
	var rows []struct {
		Details map[string]any `json:"ip_availability_details"`
	}
	if err := page.ExtractIntoSlicePtr(&rows, "network_ip_availabilities"); err != nil {
		return nil, wrap(err, "parsing the IP availability list")
	}
	out := make([]IPAvailability, len(base))
	for i := range base {
		out[i].NetworkIPAvailability = base[i]
		if i < len(rows) {
			out[i].Details = rows[i].Details
		}
	}
	return out, nil
}

// GetIPAvailability reads one network's IP availability, per subnet.
func GetIPAvailability(ctx context.Context, c *gophercloud.ServiceClient, networkID string) (*networkipavailabilities.NetworkIPAvailability, error) {
	av, err := networkipavailabilities.Get(ctx, c, networkID).Extract()
	return av, wrap(err, "showing IP availability for network %s", networkID)
}

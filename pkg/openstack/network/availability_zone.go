package network

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"
)

// AvailabilityZone is one of neutron's availability zones. Resource is what
// the zone schedules ("network" or "router"); State is "available" or
// "unavailable".
type AvailabilityZone struct {
	Name     string `json:"name"`
	Resource string `json:"resource"`
	State    string `json:"state"`
}

// ListAvailabilityZones lists neutron's availability zones. koc vendors no
// gophercloud package for them, so the call is sent raw.
func ListAvailabilityZones(ctx context.Context, c *gophercloud.ServiceClient) ([]AvailabilityZone, error) {
	var body struct {
		AvailabilityZones []AvailabilityZone `json:"availability_zones"`
	}
	if err := getJSON(ctx, c, c.ServiceURL("availability_zones"), &body); err != nil {
		return nil, wrap(err, "listing network availability zones")
	}
	return body.AvailabilityZones, nil
}

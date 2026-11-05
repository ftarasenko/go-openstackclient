package network

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/attributestags"
)

// ReplaceTags sets tags as the whole tag set of the resource of resourceType
// ("networks", "ports", "floatingips", "security-groups", …) with id, and
// returns the set neutron now reports.
func ReplaceTags(ctx context.Context, c *gophercloud.ServiceClient, resourceType, id string, tags []string) ([]string, error) {
	got, err := attributestags.ReplaceAll(ctx, c, resourceType, id, attributestags.ReplaceAllOpts{Tags: tags}).Extract()
	if err != nil {
		return nil, wrap(err, "setting tags on %s %s", resourceType, id)
	}
	return got, nil
}

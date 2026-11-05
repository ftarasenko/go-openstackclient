package compute

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/limits"

	"github.com/ftarasenko/go-openstackclient/pkg/openstack"
)

// GetLimits returns nova's absolute limits and usage for the token's
// project, or for projectID when it is not empty (an admin call).
func GetLimits(ctx context.Context, c *gophercloud.ServiceClient, projectID string) (*limits.Limits, error) {
	l, err := limits.Get(ctx, c, limits.GetOpts{TenantID: projectID}).Extract()
	if err == nil && l == nil {
		err = openstack.ErrNoObject
	}
	if err != nil {
		return nil, wrap(err, "getting compute limits")
	}
	return l, nil
}

package volume

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/limits"

	"github.com/ftarasenko/go-openstackclient/pkg/openstack"
)

// GetLimits returns cinder's absolute limits and usage for the token's
// project.
func GetLimits(ctx context.Context, c *gophercloud.ServiceClient) (*limits.Limits, error) {
	l, err := limits.Get(ctx, c).Extract()
	if err == nil && l == nil {
		err = openstack.ErrNoObject
	}
	if err != nil {
		return nil, wrap(err, "getting volume limits")
	}
	return l, nil
}

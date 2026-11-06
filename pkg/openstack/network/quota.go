package network

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/quotas"

	"github.com/ftarasenko/koc/pkg/openstack"
)

// GetQuota returns a project's neutron quotas.
func GetQuota(ctx context.Context, c *gophercloud.ServiceClient, projectID string) (*quotas.Quota, error) {
	q, err := quota(quotas.Get(ctx, c, projectID).Extract())
	return q, wrap(err, "showing network quotas for project %q", projectID)
}

// GetDefaultQuota returns the quotas neutron applies to a project that has
// none of its own. gophercloud has no call for quotas/{id}/default, so it is
// sent raw.
func GetDefaultQuota(ctx context.Context, c *gophercloud.ServiceClient, projectID string) (*quotas.Quota, error) {
	var body struct {
		Quota quotas.Quota `json:"quota"`
	}
	if err := getJSON(ctx, c, c.ServiceURL("quotas", projectID, "default"), &body); err != nil {
		return nil, wrap(err, "showing default network quotas for project %q", projectID)
	}
	return &body.Quota, nil
}

// UpdateQuota sets the quotas opts names and returns the project's quotas as
// neutron then reports them.
func UpdateQuota(ctx context.Context, c *gophercloud.ServiceClient, projectID string, opts quotas.UpdateOptsBuilder) (*quotas.Quota, error) {
	q, err := quota(quotas.Update(ctx, c, projectID, opts).Extract())
	return q, wrap(err, "setting network quotas for project %q", projectID)
}

// quota turns a success without a quota object into openstack.ErrNoObject.
func quota(q *quotas.Quota, err error) (*quotas.Quota, error) {
	if err == nil && q == nil {
		err = openstack.ErrNoObject
	}
	if err != nil {
		return nil, err
	}
	return q, nil
}

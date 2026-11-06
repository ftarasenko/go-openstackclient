package compute

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/quotasets"

	"github.com/ftarasenko/koc/pkg/openstack"
)

// GetQuotaSet returns a project's nova quotas.
func GetQuotaSet(ctx context.Context, c *gophercloud.ServiceClient, projectID string) (*quotasets.QuotaSet, error) {
	qs, err := quotaSet(quotasets.Get(ctx, c, projectID).Extract())
	return qs, wrap(err, "showing compute quotas for project %q", projectID)
}

// GetDefaultQuotaSet returns the quotas nova applies to a project that has
// none of its own. gophercloud has no call for os-quota-sets/{id}/defaults,
// so it is sent raw.
func GetDefaultQuotaSet(ctx context.Context, c *gophercloud.ServiceClient, projectID string) (*quotasets.QuotaSet, error) {
	var body struct {
		QuotaSet quotasets.QuotaSet `json:"quota_set"`
	}
	if err := getJSON(ctx, c, c.ServiceURL("os-quota-sets", projectID, "defaults"), &body); err != nil {
		return nil, wrap(err, "showing default compute quotas for project %q", projectID)
	}
	return &body.QuotaSet, nil
}

// UpdateQuotaSet sets the quotas opts names and returns the project's
// quotas as nova then reports them.
func UpdateQuotaSet(ctx context.Context, c *gophercloud.ServiceClient, projectID string, opts quotasets.UpdateOptsBuilder) (*quotasets.QuotaSet, error) {
	qs, err := quotaSet(quotasets.Update(ctx, c, projectID, opts).Extract())
	return qs, wrap(err, "setting compute quotas for project %q", projectID)
}

// quotaSet turns a success without a quota_set into openstack.ErrNoObject.
func quotaSet(qs *quotasets.QuotaSet, err error) (*quotasets.QuotaSet, error) {
	if err == nil && qs == nil {
		err = openstack.ErrNoObject
	}
	if err != nil {
		return nil, err
	}
	return qs, nil
}

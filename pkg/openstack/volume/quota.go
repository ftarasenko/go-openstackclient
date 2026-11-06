package volume

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/quotasets"

	"github.com/ftarasenko/koc/pkg/openstack"
)

// QuotaSet is a cinder quota set: gophercloud's typed quotas, plus the ones
// cinder keeps per volume type as <resource>_<type name> (gigabytes_ssd,
// volumes_ssd, snapshots_ssd), which gophercloud does not model.
type QuotaSet struct {
	quotasets.QuotaSet
	PerVolumeType map[string]int
}

// IsPerVolumeTypeQuota reports whether key names a per-volume-type quota:
// gigabytes_, snapshots_ or volumes_ followed by a type name.
func IsPerVolumeTypeQuota(key string) bool {
	for _, p := range []string{"gigabytes_", "snapshots_", "volumes_"} {
		if strings.HasPrefix(key, p) && len(key) > len(p) {
			return true
		}
	}
	return false
}

// GetQuotaSet returns a project's cinder quotas.
func GetQuotaSet(ctx context.Context, c *gophercloud.ServiceClient, projectID string) (*QuotaSet, error) {
	qs, err := extractQuotaSet(quotasets.Get(ctx, c, projectID))
	return qs, wrap(err, "showing volume quotas for project %q", projectID)
}

// GetDefaultQuotaSet returns the quotas cinder applies to a project that has
// none of its own.
func GetDefaultQuotaSet(ctx context.Context, c *gophercloud.ServiceClient, projectID string) (*QuotaSet, error) {
	qs, err := extractQuotaSet(quotasets.GetDefaults(ctx, c, projectID))
	return qs, wrap(err, "showing default volume quotas for project %q", projectID)
}

// UpdateQuotaSet sets the quotas opts names and returns the project's
// quotas as cinder then reports them.
func UpdateQuotaSet(ctx context.Context, c *gophercloud.ServiceClient, projectID string, opts quotasets.UpdateOptsBuilder) (*QuotaSet, error) {
	qs, err := extractQuotaSet(quotasets.Update(ctx, c, projectID, opts))
	return qs, wrap(err, "setting volume quotas for project %q", projectID)
}

// quotaResult is what quotasets.Get, GetDefaults and Update return.
type quotaResult interface {
	Extract() (*quotasets.QuotaSet, error)
	ExtractInto(to any) error
}

// extractQuotaSet decodes the typed quotas, then the reply again for the
// per-volume-type ones. A value that is not an integer is not a quota and
// is skipped.
func extractQuotaSet(res quotaResult) (*QuotaSet, error) {
	qs, err := res.Extract()
	if err == nil && qs == nil {
		err = openstack.ErrNoObject
	}
	if err != nil {
		return nil, err
	}
	var raw struct {
		QuotaSet map[string]json.RawMessage `json:"quota_set"`
	}
	if err := res.ExtractInto(&raw); err != nil {
		return nil, err
	}
	out := &QuotaSet{QuotaSet: *qs}
	for k, v := range raw.QuotaSet {
		var n int
		if !IsPerVolumeTypeQuota(k) || json.Unmarshal(v, &n) != nil {
			continue
		}
		if out.PerVolumeType == nil {
			out.PerVolumeType = map[string]int{}
		}
		out.PerVolumeType[k] = n
	}
	return out, nil
}

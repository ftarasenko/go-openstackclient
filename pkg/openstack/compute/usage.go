package compute

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/usage"
	"github.com/gophercloud/gophercloud/v2/pagination"
)

// ListUsage lists every project's usage over the window opts gives.
func ListUsage(ctx context.Context, c *gophercloud.ServiceClient, opts usage.AllTenantsOptsBuilder) ([]usage.TenantUsage, error) {
	pages, err := usage.AllTenants(c, opts).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing compute usage")
	}
	all, err := usage.ExtractAllTenants(pages)
	return all, wrap(err, "listing compute usage")
}

// GetProjectUsage returns one project's usage over the window opts gives,
// or nil when nova reports none. From microversion 2.40 nova pages the
// server usages: the result holds them all, and the totals, which cover the
// whole window, of the first page.
func GetProjectUsage(ctx context.Context, c *gophercloud.ServiceClient, projectID string, opts usage.SingleTenantOptsBuilder) (*usage.TenantUsage, error) {
	// EachPage, not AllPages: the body is one object rather than a list, and
	// AllPages merges pages by their top-level array, so ExtractSingleTenant
	// would find no tenant_usage key and return (nil, nil).
	var u *usage.TenantUsage
	err := usage.SingleTenant(c, projectID, opts).EachPage(ctx, func(_ context.Context, page pagination.Page) (bool, error) {
		got, err := usage.ExtractSingleTenant(page)
		switch {
		case err != nil:
			return false, err
		case got == nil:
		case u == nil:
			u = got
		default:
			u.ServerUsages = append(u.ServerUsages, got.ServerUsages...)
		}
		return true, nil
	})
	if err != nil {
		return nil, wrap(err, "getting compute usage for project %s", projectID)
	}
	return u, nil
}

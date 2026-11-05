package identity

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/roles"
)

// ListRoles lists the roles opts selects; opts.DomainID lists a domain's own
// roles instead of the global ones.
func ListRoles(ctx context.Context, c *gophercloud.ServiceClient, opts roles.ListOpts) ([]roles.Role, error) {
	return listAll(ctx, roles.List(c, opts), roles.ExtractRoles, "listing roles")
}

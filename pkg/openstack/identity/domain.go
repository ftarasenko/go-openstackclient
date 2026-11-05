package identity

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/domains"
)

// ListDomains lists the domains opts selects.
func ListDomains(ctx context.Context, c *gophercloud.ServiceClient, opts domains.ListOpts) ([]domains.Domain, error) {
	return listAll(ctx, domains.List(c, opts), domains.ExtractDomains, "listing domains")
}

// GetDomain reads one domain by ID.
func GetDomain(ctx context.Context, c *gophercloud.ServiceClient, id string) (*domains.Domain, error) {
	d, err := domains.Get(ctx, c, id).Extract()
	return d, wrap(err, "showing domain %s", id)
}

// CreateDomain creates a domain.
func CreateDomain(ctx context.Context, c *gophercloud.ServiceClient, opts domains.CreateOpts) (*domains.Domain, error) {
	d, err := domains.Create(ctx, c, opts).Extract()
	return d, wrap(err, "creating domain %q", opts.Name)
}

// UpdateDomain changes the fields opts sets on a domain.
func UpdateDomain(ctx context.Context, c *gophercloud.ServiceClient, id string, opts domains.UpdateOpts) (*domains.Domain, error) {
	d, err := domains.Update(ctx, c, id, opts).Extract()
	return d, wrap(err, "updating domain %s", id)
}

// DeleteDomain deletes a domain; keystone refuses an enabled one.
func DeleteDomain(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(domains.Delete(ctx, c, id).ExtractErr(), "deleting domain %s", id)
}

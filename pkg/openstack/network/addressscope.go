package network

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/layer3/addressscopes"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/security/addressgroups"
)

// ListAddressScopes lists every address scope matching opts.
func ListAddressScopes(ctx context.Context, c *gophercloud.ServiceClient, opts addressscopes.ListOptsBuilder) ([]addressscopes.AddressScope, error) {
	pages, err := addressscopes.List(c, opts).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing address scopes")
	}
	all, err := addressscopes.ExtractAddressScopes(pages)
	return all, wrap(err, "parsing the address scope list")
}

// GetAddressScope reads one address scope.
func GetAddressScope(ctx context.Context, c *gophercloud.ServiceClient, id string) (*addressscopes.AddressScope, error) {
	s, err := addressscopes.Get(ctx, c, id).Extract()
	return s, wrap(err, "showing address scope %s", id)
}

// CreateAddressScope creates an address scope. Wrap opts with
// WithAddressScopeCreateAttrs to send an explicit false shared, which
// addressscopes.CreateOpts drops.
func CreateAddressScope(ctx context.Context, c *gophercloud.ServiceClient, opts addressscopes.CreateOptsBuilder) (*addressscopes.AddressScope, error) {
	s, err := addressscopes.Create(ctx, c, opts).Extract()
	return s, wrap(err, "creating address scope")
}

// UpdateAddressScope updates an address scope.
func UpdateAddressScope(ctx context.Context, c *gophercloud.ServiceClient, id string, opts addressscopes.UpdateOptsBuilder) (*addressscopes.AddressScope, error) {
	s, err := addressscopes.Update(ctx, c, id, opts).Extract()
	return s, wrap(err, "updating address scope %s", id)
}

// DeleteAddressScope deletes an address scope.
func DeleteAddressScope(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(addressscopes.Delete(ctx, c, id).ExtractErr(), "deleting address scope %s", id)
}

// ListAddressGroups lists every address group matching opts.
func ListAddressGroups(ctx context.Context, c *gophercloud.ServiceClient, opts addressgroups.ListOptsBuilder) ([]addressgroups.AddressGroup, error) {
	pages, err := addressgroups.List(c, opts).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing address groups")
	}
	all, err := addressgroups.ExtractGroups(pages)
	return all, wrap(err, "parsing the address group list")
}

// GetAddressGroup reads one address group.
func GetAddressGroup(ctx context.Context, c *gophercloud.ServiceClient, id string) (*addressgroups.AddressGroup, error) {
	g, err := addressgroups.Get(ctx, c, id).Extract()
	return g, wrap(err, "showing address group %s", id)
}

// CreateAddressGroup creates an address group. neutron requires the
// addresses key even when empty, so give CreateOpts an empty, not a nil,
// slice.
func CreateAddressGroup(ctx context.Context, c *gophercloud.ServiceClient, opts addressgroups.CreateOptsBuilder) (*addressgroups.AddressGroup, error) {
	g, err := addressgroups.Create(ctx, c, opts).Extract()
	return g, wrap(err, "creating address group")
}

// UpdateAddressGroup updates an address group's name or description; its
// addresses change through AddAddressGroupAddresses and
// RemoveAddressGroupAddresses.
func UpdateAddressGroup(ctx context.Context, c *gophercloud.ServiceClient, id string, opts addressgroups.UpdateOptsBuilder) (*addressgroups.AddressGroup, error) {
	g, err := addressgroups.Update(ctx, c, id, opts).Extract()
	return g, wrap(err, "updating address group %s", id)
}

// DeleteAddressGroup deletes an address group.
func DeleteAddressGroup(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(addressgroups.Delete(ctx, c, id).ExtractErr(), "deleting address group %s", id)
}

// AddAddressGroupAddresses adds CIDRs to an address group.
func AddAddressGroupAddresses(ctx context.Context, c *gophercloud.ServiceClient, id string, addresses []string) (*addressgroups.AddressGroup, error) {
	g, err := addressgroups.AddAddresses(ctx, c, id, addressgroups.UpdateAddressesOpts{Addresses: addresses}).Extract()
	return g, wrap(err, "adding addresses to address group %s", id)
}

// RemoveAddressGroupAddresses removes CIDRs from an address group.
func RemoveAddressGroupAddresses(ctx context.Context, c *gophercloud.ServiceClient, id string, addresses []string) (*addressgroups.AddressGroup, error) {
	g, err := addressgroups.RemoveAddresses(ctx, c, id, addressgroups.UpdateAddressesOpts{Addresses: addresses}).Extract()
	return g, wrap(err, "removing addresses from address group %s", id)
}

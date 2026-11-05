package network

import (
	"context"
	"fmt"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/layer3/addressscopes"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/layer3/floatingips"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/layer3/routers"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/security/addressgroups"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/security/groups"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/networks"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/ports"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/subnets"
	"github.com/gophercloud/gophercloud/v2/pagination"

	"github.com/ftarasenko/go-openstackclient/pkg/openstack/resolve"
)

// The Find…ID lookups turn a name (or, for floating IPs, an address) into an
// ID. They are strict, as upstream openstacksdk's find_* are:
//
//   - a UUID is returned as-is, with no request;
//   - anything else, the empty string included, is looked up with the
//     filtered listing, and exactly one match yields its ID;
//   - no match is a *resolve.NotFoundError (errors.Is openstack.ErrNotFound);
//   - several are a *resolve.AmbiguousError;
//   - a failed listing is wrapped as "looking up <kind> <ref>".
//
// An empty ref is not special-cased: neutron drops the empty filter, so it
// resolves only when exactly one resource is visible.

// FindNetworkID resolves a network name or ID to an ID.
func FindNetworkID(ctx context.Context, c *gophercloud.ServiceClient, ref string) (string, error) {
	return findID(ctx, "network", ref, func(ctx context.Context) ([]networks.Network, error) {
		pages, err := networks.List(c, networks.ListOpts{Name: ref}).AllPages(ctx)
		return extracted(pages, err, networks.ExtractNetworks)
	}, func(n networks.Network) string { return n.ID })
}

// FindSubnetID resolves a subnet name or ID to an ID.
func FindSubnetID(ctx context.Context, c *gophercloud.ServiceClient, ref string) (string, error) {
	return findID(ctx, "subnet", ref, func(ctx context.Context) ([]subnets.Subnet, error) {
		pages, err := subnets.List(c, subnets.ListOpts{Name: ref}).AllPages(ctx)
		return extracted(pages, err, subnets.ExtractSubnets)
	}, func(s subnets.Subnet) string { return s.ID })
}

// FindRouterID resolves a router name or ID to an ID.
func FindRouterID(ctx context.Context, c *gophercloud.ServiceClient, ref string) (string, error) {
	return findID(ctx, "router", ref, func(ctx context.Context) ([]routers.Router, error) {
		pages, err := routers.List(c, routers.ListOpts{Name: ref}).AllPages(ctx)
		return extracted(pages, err, routers.ExtractRouters)
	}, func(r routers.Router) string { return r.ID })
}

// FindPortID resolves a port name or ID to an ID.
func FindPortID(ctx context.Context, c *gophercloud.ServiceClient, ref string) (string, error) {
	return findID(ctx, "port", ref, func(ctx context.Context) ([]ports.Port, error) {
		pages, err := ports.List(c, ports.ListOpts{Name: ref}).AllPages(ctx)
		return extracted(pages, err, ports.ExtractPorts)
	}, func(p ports.Port) string { return p.ID })
}

// FindSecurityGroupID resolves a security group name or ID to an ID.
func FindSecurityGroupID(ctx context.Context, c *gophercloud.ServiceClient, ref string) (string, error) {
	return findID(ctx, "security group", ref, func(ctx context.Context) ([]groups.SecGroup, error) {
		pages, err := groups.List(c, groups.ListOpts{Name: ref}).AllPages(ctx)
		return extracted(pages, err, groups.ExtractGroups)
	}, func(g groups.SecGroup) string { return g.ID })
}

// FindAddressScopeID resolves an address scope name or ID to an ID.
func FindAddressScopeID(ctx context.Context, c *gophercloud.ServiceClient, ref string) (string, error) {
	return findID(ctx, "address scope", ref, func(ctx context.Context) ([]addressscopes.AddressScope, error) {
		pages, err := addressscopes.List(c, addressscopes.ListOpts{Name: ref}).AllPages(ctx)
		return extracted(pages, err, addressscopes.ExtractAddressScopes)
	}, func(s addressscopes.AddressScope) string { return s.ID })
}

// FindAddressGroupID resolves an address group name or ID to an ID.
func FindAddressGroupID(ctx context.Context, c *gophercloud.ServiceClient, ref string) (string, error) {
	return findID(ctx, "address group", ref, func(ctx context.Context) ([]addressgroups.AddressGroup, error) {
		pages, err := addressgroups.List(c, addressgroups.ListOpts{Name: ref}).AllPages(ctx)
		return extracted(pages, err, addressgroups.ExtractGroups)
	}, func(g addressgroups.AddressGroup) string { return g.ID })
}

// FindFloatingIPID resolves a floating IP address or ID to an ID: anything
// but a UUID is matched against floating_ip_address (upstream's find_ip).
func FindFloatingIPID(ctx context.Context, c *gophercloud.ServiceClient, ref string) (string, error) {
	if resolve.IsUUID(ref) {
		return ref, nil
	}
	pages, err := floatingips.List(c, floatingips.ListOpts{FloatingIP: ref}).AllPages(ctx)
	if err != nil {
		return "", fmt.Errorf("looking up floating IP %q: %w", ref, err)
	}
	all, err := floatingips.ExtractFloatingIPs(pages)
	if err != nil {
		return "", fmt.Errorf("parsing floating IP lookup for %q: %w", ref, err)
	}
	return pick("floating IP", ref, all, func(f floatingips.FloatingIP) string { return f.ID })
}

// extracted is extract(page), or err when the listing failed.
func extracted[T any](page pagination.Page, err error, extract func(pagination.Page) ([]T, error)) ([]T, error) {
	if err != nil {
		return nil, err
	}
	return extract(page)
}

// findID is the policy every Find…ID shares.
func findID[T any](ctx context.Context, kind, ref string,
	list func(context.Context) ([]T, error), idOf func(T) string,
) (string, error) {
	if resolve.IsUUID(ref) {
		return ref, nil
	}
	all, err := list(ctx)
	if err != nil {
		return "", fmt.Errorf("looking up %s %q: %w", kind, ref, err)
	}
	return pick(kind, ref, all, idOf)
}

func pick[T any](kind, ref string, all []T, idOf func(T) string) (string, error) {
	switch len(all) {
	case 1:
		return idOf(all[0]), nil
	case 0:
		return "", &resolve.NotFoundError{Kind: kind, Ref: ref}
	default:
		return "", &resolve.AmbiguousError{Kind: kind, Ref: ref, Count: len(all)}
	}
}

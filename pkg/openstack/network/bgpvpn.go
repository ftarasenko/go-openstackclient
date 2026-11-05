package network

import (
	"context"
	"net/url"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/bgpvpns"
)

// BGP VPNs and their network, router and port associations: the
// networking-bgpvpn plugin's bgpvpn extension, plus bgpvpn-routes-control for
// port associations and a router association's advertise_extra_routes.

// bgpvpnName is a name filter, which bgpvpns.ListOpts does not carry.
type bgpvpnName string

func (n bgpvpnName) ToBGPVPNListQuery() (string, error) {
	return "?" + url.Values{"name": {string(n)}}.Encode(), nil
}

// FindBGPVPNID resolves a BGP VPN name or ID to an ID.
func FindBGPVPNID(ctx context.Context, c *gophercloud.ServiceClient, ref string) (string, error) {
	return findID(ctx, "BGP VPN", ref, func(ctx context.Context) ([]bgpvpns.BGPVPN, error) {
		pages, err := bgpvpns.List(c, bgpvpnName(ref)).AllPages(ctx)
		return extracted(pages, err, bgpvpns.ExtractBGPVPNs)
	}, func(b bgpvpns.BGPVPN) string { return b.ID })
}

// ListBGPVPNs lists every BGP VPN matching opts; nil lists them all.
func ListBGPVPNs(ctx context.Context, c *gophercloud.ServiceClient, opts bgpvpns.ListOptsBuilder) ([]bgpvpns.BGPVPN, error) {
	if opts == nil {
		opts = bgpvpns.ListOpts{}
	}
	pages, err := bgpvpns.List(c, opts).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing BGP VPNs")
	}
	all, err := bgpvpns.ExtractBGPVPNs(pages)
	return all, wrap(err, "parsing the BGP VPN list")
}

// GetBGPVPN reads one BGP VPN.
func GetBGPVPN(ctx context.Context, c *gophercloud.ServiceClient, id string) (*bgpvpns.BGPVPN, error) {
	b, err := bgpvpns.Get(ctx, c, id).Extract()
	return b, wrap(err, "getting BGP VPN %s", id)
}

// CreateBGPVPN creates a BGP VPN. bgpvpns.CreateOpts drops a zero vni or
// local_pref; pass a builder of its own to send one.
func CreateBGPVPN(ctx context.Context, c *gophercloud.ServiceClient, opts bgpvpns.CreateOptsBuilder) (*bgpvpns.BGPVPN, error) {
	b, err := bgpvpns.Create(ctx, c, opts).Extract()
	return b, wrap(err, "creating BGP VPN")
}

// UpdateBGPVPN updates a BGP VPN. Its target and distinguisher lists replace
// the VPN's, so to add one, send the current entries with it.
// bgpvpns.UpdateOpts has no vni.
func UpdateBGPVPN(ctx context.Context, c *gophercloud.ServiceClient, id string, opts bgpvpns.UpdateOptsBuilder) (*bgpvpns.BGPVPN, error) {
	b, err := bgpvpns.Update(ctx, c, id, opts).Extract()
	return b, wrap(err, "updating BGP VPN %s", id)
}

// DeleteBGPVPN deletes a BGP VPN.
func DeleteBGPVPN(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(bgpvpns.Delete(ctx, c, id).ExtractErr(), "deleting BGP VPN %s", id)
}

// ListBGPVPNNetworkAssociations lists a BGP VPN's network associations matching
// opts; nil lists them all.
func ListBGPVPNNetworkAssociations(ctx context.Context, c *gophercloud.ServiceClient, bgpvpnID string,
	opts bgpvpns.ListNetworkAssociationsOptsBuilder,
) ([]bgpvpns.NetworkAssociation, error) {
	if opts == nil {
		opts = bgpvpns.ListNetworkAssociationsOpts{}
	}
	pages, err := bgpvpns.ListNetworkAssociations(c, bgpvpnID, opts).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing network associations")
	}
	all, err := bgpvpns.ExtractNetworkAssociations(pages)
	return all, wrap(err, "parsing the network association list")
}

// GetBGPVPNNetworkAssociation reads one network association.
func GetBGPVPNNetworkAssociation(ctx context.Context, c *gophercloud.ServiceClient, bgpvpnID, id string) (*bgpvpns.NetworkAssociation, error) {
	n, err := bgpvpns.GetNetworkAssociation(ctx, c, bgpvpnID, id).Extract()
	return n, wrap(err, "getting network association %s", id)
}

// CreateBGPVPNNetworkAssociation associates a network with a BGP VPN.
func CreateBGPVPNNetworkAssociation(ctx context.Context, c *gophercloud.ServiceClient, bgpvpnID string,
	opts bgpvpns.CreateNetworkAssociationOptsBuilder,
) (*bgpvpns.NetworkAssociation, error) {
	n, err := bgpvpns.CreateNetworkAssociation(ctx, c, bgpvpnID, opts).Extract()
	return n, wrap(err, "creating network association")
}

// DeleteBGPVPNNetworkAssociation removes a network association.
func DeleteBGPVPNNetworkAssociation(ctx context.Context, c *gophercloud.ServiceClient, bgpvpnID, id string) error {
	return wrap(bgpvpns.DeleteNetworkAssociation(ctx, c, bgpvpnID, id).ExtractErr(), "deleting network association %s", id)
}

// ListBGPVPNRouterAssociations lists a BGP VPN's router associations matching
// opts; nil lists them all.
func ListBGPVPNRouterAssociations(ctx context.Context, c *gophercloud.ServiceClient, bgpvpnID string,
	opts bgpvpns.ListRouterAssociationsOptsBuilder,
) ([]bgpvpns.RouterAssociation, error) {
	if opts == nil {
		opts = bgpvpns.ListRouterAssociationsOpts{}
	}
	pages, err := bgpvpns.ListRouterAssociations(c, bgpvpnID, opts).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing router associations")
	}
	all, err := bgpvpns.ExtractRouterAssociations(pages)
	return all, wrap(err, "parsing the router association list")
}

// GetBGPVPNRouterAssociation reads one router association.
func GetBGPVPNRouterAssociation(ctx context.Context, c *gophercloud.ServiceClient, bgpvpnID, id string) (*bgpvpns.RouterAssociation, error) {
	r, err := bgpvpns.GetRouterAssociation(ctx, c, bgpvpnID, id).Extract()
	return r, wrap(err, "getting router association %s", id)
}

// CreateBGPVPNRouterAssociation associates a router with a BGP VPN.
// AdvertiseExtraRoutes needs bgpvpn-routes-control.
func CreateBGPVPNRouterAssociation(ctx context.Context, c *gophercloud.ServiceClient, bgpvpnID string,
	opts bgpvpns.CreateRouterAssociationOptsBuilder,
) (*bgpvpns.RouterAssociation, error) {
	r, err := bgpvpns.CreateRouterAssociation(ctx, c, bgpvpnID, opts).Extract()
	return r, wrap(err, "creating router association")
}

// UpdateBGPVPNRouterAssociation updates a router association's
// advertise_extra_routes (bgpvpn-routes-control).
func UpdateBGPVPNRouterAssociation(ctx context.Context, c *gophercloud.ServiceClient, bgpvpnID, id string,
	opts bgpvpns.UpdateRouterAssociationOptsBuilder,
) (*bgpvpns.RouterAssociation, error) {
	r, err := bgpvpns.UpdateRouterAssociation(ctx, c, bgpvpnID, id, opts).Extract()
	return r, wrap(err, "updating router association %s", id)
}

// DeleteBGPVPNRouterAssociation removes a router association.
func DeleteBGPVPNRouterAssociation(ctx context.Context, c *gophercloud.ServiceClient, bgpvpnID, id string) error {
	return wrap(bgpvpns.DeleteRouterAssociation(ctx, c, bgpvpnID, id).ExtractErr(), "deleting router association %s", id)
}

// ListBGPVPNPortAssociations lists a BGP VPN's port associations
// (bgpvpn-routes-control) matching opts; nil lists them all.
func ListBGPVPNPortAssociations(ctx context.Context, c *gophercloud.ServiceClient, bgpvpnID string,
	opts bgpvpns.ListPortAssociationsOptsBuilder,
) ([]bgpvpns.PortAssociation, error) {
	if opts == nil {
		opts = bgpvpns.ListPortAssociationsOpts{}
	}
	pages, err := bgpvpns.ListPortAssociations(c, bgpvpnID, opts).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing port associations")
	}
	all, err := bgpvpns.ExtractPortAssociations(pages)
	return all, wrap(err, "parsing the port association list")
}

// GetBGPVPNPortAssociation reads one port association.
func GetBGPVPNPortAssociation(ctx context.Context, c *gophercloud.ServiceClient, bgpvpnID, id string) (*bgpvpns.PortAssociation, error) {
	p, err := bgpvpns.GetPortAssociation(ctx, c, bgpvpnID, id).Extract()
	return p, wrap(err, "getting port association %s", id)
}

// CreateBGPVPNPortAssociation associates a port, and the routes it
// advertises, with a BGP VPN.
func CreateBGPVPNPortAssociation(ctx context.Context, c *gophercloud.ServiceClient, bgpvpnID string,
	opts bgpvpns.CreatePortAssociationOptsBuilder,
) (*bgpvpns.PortAssociation, error) {
	p, err := bgpvpns.CreatePortAssociation(ctx, c, bgpvpnID, opts).Extract()
	return p, wrap(err, "creating port association")
}

// UpdateBGPVPNPortAssociation updates a port association. Its routes replace
// the association's, so to add one, send the current routes with it.
func UpdateBGPVPNPortAssociation(ctx context.Context, c *gophercloud.ServiceClient, bgpvpnID, id string,
	opts bgpvpns.UpdatePortAssociationOptsBuilder,
) (*bgpvpns.PortAssociation, error) {
	p, err := bgpvpns.UpdatePortAssociation(ctx, c, bgpvpnID, id, opts).Extract()
	return p, wrap(err, "updating port association %s", id)
}

// DeleteBGPVPNPortAssociation removes a port association.
func DeleteBGPVPNPortAssociation(ctx context.Context, c *gophercloud.ServiceClient, bgpvpnID, id string) error {
	return wrap(bgpvpns.DeletePortAssociation(ctx, c, bgpvpnID, id).ExtractErr(), "deleting port association %s", id)
}

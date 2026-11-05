package network

import (
	"context"
	"net/url"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/layer3/floatingips"
)

// FloatingIP is gophercloud's floating IP plus the extension attributes it
// does not model.
type FloatingIP struct {
	floatingips.FloatingIP
	FloatingIPAttrs
}

// FloatingIPAttrs are a floating IP's extension attributes: qos_policy_id
// (qos-fip) and dns_name/dns_domain (dns-integration). The struct is an
// exported embed because gophercloud decodes each embedded struct on its own,
// next to FloatingIP's UnmarshalJSON.
type FloatingIPAttrs struct {
	QoSPolicyID string `json:"qos_policy_id"`
	DNSName     string `json:"dns_name"`
	DNSDomain   string `json:"dns_domain"`
}

// FloatingIPListOpts is floatingips.ListOpts plus the filters it cannot
// repeat. neutron ORs repeated values, which is how upstream sends a
// repeatable --network, --port or --router.
type FloatingIPListOpts struct {
	floatingips.ListOpts
	NetworkIDs []string
	PortIDs    []string
	RouterIDs  []string
}

// ToFloatingIPListQuery implements floatingips.ListOptsBuilder.
func (opts FloatingIPListOpts) ToFloatingIPListQuery() (string, error) {
	q, err := opts.ListOpts.ToFloatingIPListQuery()
	return withQueryValues(q, err, url.Values{
		"floating_network_id": opts.NetworkIDs,
		"port_id":             opts.PortIDs,
		"router_id":           opts.RouterIDs,
	})
}

// ListFloatingIPs lists every floating IP opts matches, all pages.
func ListFloatingIPs(ctx context.Context, c *gophercloud.ServiceClient, opts floatingips.ListOptsBuilder) ([]FloatingIP, error) {
	pages, err := floatingips.List(c, opts).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing floating IPs")
	}
	var all []FloatingIP
	if err := floatingips.ExtractFloatingIPsInto(pages, &all); err != nil {
		return nil, wrap(err, "parsing floating IP list")
	}
	return all, nil
}

// GetFloatingIP reads one floating IP.
func GetFloatingIP(ctx context.Context, c *gophercloud.ServiceClient, id string) (*FloatingIP, error) {
	var f FloatingIP
	if err := floatingips.Get(ctx, c, id).ExtractInto(&f); err != nil {
		return nil, wrap(err, "getting floating IP %s", id)
	}
	return &f, nil
}

// CreateFloatingIP allocates a floating IP. Wrap opts with
// WithFloatingIPCreateAttrs to send extension attributes.
func CreateFloatingIP(ctx context.Context, c *gophercloud.ServiceClient, opts floatingips.CreateOptsBuilder) (*FloatingIP, error) {
	var f FloatingIP
	if err := floatingips.Create(ctx, c, opts).ExtractInto(&f); err != nil {
		return nil, wrap(err, "creating floating IP")
	}
	return &f, nil
}

// UpdateFloatingIP updates a floating IP. Wrap opts with
// WithFloatingIPUpdateAttrs to send extension attributes.
func UpdateFloatingIP(ctx context.Context, c *gophercloud.ServiceClient, id string, opts floatingips.UpdateOptsBuilder) (*FloatingIP, error) {
	var f FloatingIP
	if err := floatingips.Update(ctx, c, id, opts).ExtractInto(&f); err != nil {
		return nil, wrap(err, "updating floating IP %s", id)
	}
	return &f, nil
}

// DeleteFloatingIP releases a floating IP.
func DeleteFloatingIP(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(floatingips.Delete(ctx, c, id).ExtractErr(), "deleting floating IP %s", id)
}

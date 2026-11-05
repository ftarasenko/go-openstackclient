package network

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/layer3/portforwarding"
)

// ListPortForwardings lists a floating IP's port forwardings matching opts.
func ListPortForwardings(ctx context.Context, c *gophercloud.ServiceClient, floatingIPID string, opts portforwarding.ListOptsBuilder) ([]portforwarding.PortForwarding, error) {
	pages, err := portforwarding.List(c, opts, floatingIPID).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing port forwardings of floating IP %s", floatingIPID)
	}
	all, err := portforwarding.ExtractPortForwardings(pages)
	return all, wrap(err, "parsing the port forwarding list")
}

// GetPortForwarding reads one of a floating IP's port forwardings.
func GetPortForwarding(ctx context.Context, c *gophercloud.ServiceClient, floatingIPID, id string) (*portforwarding.PortForwarding, error) {
	pf, err := portforwarding.Get(ctx, c, floatingIPID, id).Extract()
	return pf, wrap(err, "showing port forwarding %s", id)
}

// CreatePortForwarding adds a port forwarding to a floating IP. Wrap opts
// with WithPortForwardingCreateAttrs to send attributes gophercloud does
// not model.
func CreatePortForwarding(ctx context.Context, c *gophercloud.ServiceClient, floatingIPID string, opts portforwarding.CreateOptsBuilder) (*portforwarding.PortForwarding, error) {
	pf, err := portforwarding.Create(ctx, c, floatingIPID, opts).Extract()
	return pf, wrap(err, "creating a port forwarding on floating IP %s", floatingIPID)
}

// UpdatePortForwarding updates one of a floating IP's port forwardings.
func UpdatePortForwarding(ctx context.Context, c *gophercloud.ServiceClient, floatingIPID, id string, opts portforwarding.UpdateOptsBuilder) (*portforwarding.PortForwarding, error) {
	pf, err := portforwarding.Update(ctx, c, floatingIPID, id, opts).Extract()
	return pf, wrap(err, "updating port forwarding %s", id)
}

// DeletePortForwarding deletes one of a floating IP's port forwardings.
func DeletePortForwarding(ctx context.Context, c *gophercloud.ServiceClient, floatingIPID, id string) error {
	return wrap(portforwarding.Delete(ctx, c, floatingIPID, id).ExtractErr(), "deleting port forwarding %s", id)
}

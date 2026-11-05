package network

import (
	"context"
	"fmt"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/agents"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/external"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/provider"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/networks"
)

// Network is gophercloud's network with the external-net, provider and mtu
// extensions, plus the attributes gophercloud does not model (NetworkAttrs).
// Every part is an anonymous embed so gophercloud's extraction decodes each.
type Network struct {
	networks.Network
	external.NetworkExternalExt
	provider.NetworkProviderExt
	NetworkAttrs
}

// NetworkAttrs are the network extension attributes gophercloud's
// networks.Network does not model. Pointers are nil on a cloud without the
// extension.
type NetworkAttrs struct { //nolint:revive // one pattern with SubnetAttrs, PortAttrs and RouterAttrs
	MTU                 int    `json:"mtu"`
	QoSPolicyID         string `json:"qos_policy_id"`
	DNSDomain           string `json:"dns_domain"`
	IsDefault           *bool  `json:"is_default"`
	PortSecurityEnabled *bool  `json:"port_security_enabled"`
	VLANTransparent     *bool  `json:"vlan_transparent"`
	PVLAN               *bool  `json:"pvlan"`
	QinQ                *bool  `json:"qinq"`
	// VLANQinQ is the key openstacksdk reads is_vlan_qinq from. Neutron does
	// not define it, so it is absent from every response; neutron's own
	// attribute is QinQ.
	VLANQinQ *bool `json:"vlan_qinq"`
	// L2Adjacency is the l2_adjacency extension's read-only flag: false once
	// a network has segments that are not mutually reachable at layer 2.
	L2Adjacency       *bool    `json:"l2_adjacency"`
	AvailabilityZones []string `json:"availability_zones"`
	IPv4AddressScope  string   `json:"ipv4_address_scope"`
	IPv6AddressScope  string   `json:"ipv6_address_scope"`
}

// ListNetworks lists every network matching opts, following pagination.
func ListNetworks(ctx context.Context, c *gophercloud.ServiceClient, opts networks.ListOptsBuilder) ([]Network, error) {
	pages, err := networks.List(c, opts).AllPages(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing networks: %w", err)
	}
	var all []Network
	if err := networks.ExtractNetworksInto(pages, &all); err != nil {
		return nil, fmt.Errorf("parsing network list: %w", err)
	}
	return all, nil
}

// ListDHCPAgentNetworks lists the networks a DHCP agent hosts.
func ListDHCPAgentNetworks(ctx context.Context, c *gophercloud.ServiceClient, agentID string) ([]Network, error) {
	var all []Network
	if err := agents.ListDHCPNetworks(ctx, c, agentID).ExtractIntoSlicePtr(&all, "networks"); err != nil {
		return nil, fmt.Errorf("listing networks hosted by DHCP agent %s: %w", agentID, err)
	}
	return all, nil
}

// GetNetwork reads one network.
func GetNetwork(ctx context.Context, c *gophercloud.ServiceClient, id string) (*Network, error) {
	var n Network
	if err := networks.Get(ctx, c, id).ExtractInto(&n); err != nil {
		return nil, fmt.Errorf("getting network %s: %w", id, err)
	}
	return &n, nil
}

// CreateNetwork creates a network. Wrap opts with WithNetworkCreateAttrs to
// send attributes gophercloud does not model.
func CreateNetwork(ctx context.Context, c *gophercloud.ServiceClient, opts networks.CreateOptsBuilder) (*Network, error) {
	var n Network
	if err := networks.Create(ctx, c, opts).ExtractInto(&n); err != nil {
		return nil, fmt.Errorf("creating network: %w", err)
	}
	return &n, nil
}

// UpdateNetwork updates a network.
func UpdateNetwork(ctx context.Context, c *gophercloud.ServiceClient, id string, opts networks.UpdateOptsBuilder) (*Network, error) {
	var n Network
	if err := networks.Update(ctx, c, id, opts).ExtractInto(&n); err != nil {
		return nil, fmt.Errorf("updating network %s: %w", id, err)
	}
	return &n, nil
}

// DeleteNetwork deletes a network.
func DeleteNetwork(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	if err := networks.Delete(ctx, c, id).ExtractErr(); err != nil {
		return fmt.Errorf("deleting network %s: %w", id, err)
	}
	return nil
}

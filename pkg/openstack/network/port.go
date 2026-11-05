package network

import (
	"context"
	"fmt"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/ports"
)

// Port is gophercloud's port plus the extension attributes it does not
// model. Every part is an anonymous embed so gophercloud's extraction, which
// decodes each embedded struct on its own, fills them all.
type Port struct {
	ports.Port
	PortTrunkDetails
	PortSecurity
	PortAttrs
}

// PortTrunkDetails is the trunk-details extension's trunk_details: set on a
// trunk's parent port, zero on any other.
type PortTrunkDetails struct {
	TrunkDetails TrunkDetails `json:"trunk_details"`
}

// TrunkDetails is the trunk a parent port belongs to and its subports.
type TrunkDetails struct {
	TrunkID  string         `json:"trunk_id"`
	SubPorts []TrunkSubPort `json:"sub_ports"`
}

// TrunkSubPort is one subport in TrunkDetails.
type TrunkSubPort struct {
	PortID           string `json:"port_id"`
	SegmentationID   int    `json:"segmentation_id"`
	SegmentationType string `json:"segmentation_type"`
	MACAddress       string `json:"mac_address"`
}

// PortSecurity is the port-security extension's attribute, nil on a cloud
// without it.
type PortSecurity struct {
	PortSecurityEnabled *bool `json:"port_security_enabled"`
}

// PortAttrs are the portbindings, QoS, DNS-integration, extra-DHCP-opt,
// data-plane-status and later attributes gophercloud's ports.Port does not
// model. Each is zero on a cloud that does not run its extension.
type PortAttrs struct {
	BindingHostID      string           `json:"binding:host_id"`
	BindingProfile     map[string]any   `json:"binding:profile"`
	BindingVIFDetails  map[string]any   `json:"binding:vif_details"`
	BindingVIFType     string           `json:"binding:vif_type"`
	BindingVNICType    string           `json:"binding:vnic_type"`
	DataPlaneStatus    string           `json:"data_plane_status"`
	DNSAssignment      []map[string]any `json:"dns_assignment"`
	DNSDomain          string           `json:"dns_domain"`
	DNSName            string           `json:"dns_name"`
	ExtraDHCPOpts      []map[string]any `json:"extra_dhcp_opts"`
	QoSNetworkPolicyID string           `json:"qos_network_policy_id"`
	QoSPolicyID        string           `json:"qos_policy_id"`

	DeviceProfile       string         `json:"device_profile"`
	HardwareOffloadType string         `json:"hardware_offload_type"`
	Hints               map[string]any `json:"hints"`
	IPAllocation        string         `json:"ip_allocation"`
	NUMAAffinityPolicy  string         `json:"numa_affinity_policy"`
	PVLANType           *string        `json:"pvlan_type"`
	PVLANCommunity      *string        `json:"pvlan_community"`
	ResourceRequest     map[string]any `json:"resource_request"`
	Trusted             *bool          `json:"trusted"`
}

// ListPorts lists every port matching opts, following pagination.
func ListPorts(ctx context.Context, c *gophercloud.ServiceClient, opts ports.ListOptsBuilder) ([]Port, error) {
	pages, err := ports.List(c, opts).AllPages(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing ports: %w", err)
	}
	var all []Port
	if err := ports.ExtractPortsInto(pages, &all); err != nil {
		return nil, fmt.Errorf("parsing port list: %w", err)
	}
	return all, nil
}

// GetPort reads one port.
func GetPort(ctx context.Context, c *gophercloud.ServiceClient, id string) (*Port, error) {
	var p Port
	if err := ports.Get(ctx, c, id).ExtractInto(&p); err != nil {
		return nil, fmt.Errorf("getting port %s: %w", id, err)
	}
	return &p, nil
}

// CreatePort creates a port. Wrap opts with WithPortCreateAttrs to send
// attributes gophercloud does not model.
func CreatePort(ctx context.Context, c *gophercloud.ServiceClient, opts ports.CreateOptsBuilder) (*Port, error) {
	var p Port
	if err := ports.Create(ctx, c, opts).ExtractInto(&p); err != nil {
		return nil, fmt.Errorf("creating port: %w", err)
	}
	return &p, nil
}

// UpdatePort updates a port.
func UpdatePort(ctx context.Context, c *gophercloud.ServiceClient, id string, opts ports.UpdateOptsBuilder) (*Port, error) {
	var p Port
	if err := ports.Update(ctx, c, id, opts).ExtractInto(&p); err != nil {
		return nil, fmt.Errorf("updating port %s: %w", id, err)
	}
	return &p, nil
}

// DeletePort deletes a port.
func DeletePort(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	if err := ports.Delete(ctx, c, id).ExtractErr(); err != nil {
		return fmt.Errorf("deleting port %s: %w", id, err)
	}
	return nil
}

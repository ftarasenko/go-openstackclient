package network

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/trunks"
)

// FindTrunkID resolves a trunk name or ID to an ID.
func FindTrunkID(ctx context.Context, c *gophercloud.ServiceClient, ref string) (string, error) {
	return findID(ctx, "network trunk", ref, func(ctx context.Context) ([]trunks.Trunk, error) {
		pages, err := trunks.List(c, trunks.ListOpts{Name: ref}).AllPages(ctx)
		return extracted(pages, err, trunks.ExtractTrunks)
	}, func(t trunks.Trunk) string { return t.ID })
}

// TrunkSubportSpec is one subport in a request. gophercloud's
// trunks.Subport requires all three fields, while neutron requires only the
// port and leaves the segmentation to its own rules
// (segmentation_type=inherit takes no ID); zero values are omitted, as
// neutron has no segmentation ID 0.
type TrunkSubportSpec struct {
	PortID           string `json:"port_id"`
	SegmentationType string `json:"segmentation_type,omitempty"`
	SegmentationID   int    `json:"segmentation_id,omitempty"`
}

// AddTrunkSubportsOpts is a trunks.AddSubportsOptsBuilder over
// TrunkSubportSpec entries.
type AddTrunkSubportsOpts []TrunkSubportSpec

// ToTrunkAddSubportsMap implements trunks.AddSubportsOptsBuilder.
func (s AddTrunkSubportsOpts) ToTrunkAddSubportsMap() (map[string]any, error) {
	return map[string]any{"sub_ports": []TrunkSubportSpec(s)}, nil
}

// ListTrunks lists every trunk matching opts.
func ListTrunks(ctx context.Context, c *gophercloud.ServiceClient, opts trunks.ListOptsBuilder) ([]trunks.Trunk, error) {
	pages, err := trunks.List(c, opts).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing network trunks")
	}
	all, err := trunks.ExtractTrunks(pages)
	return all, wrap(err, "parsing network trunk list")
}

// GetTrunk reads one trunk.
func GetTrunk(ctx context.Context, c *gophercloud.ServiceClient, id string) (*trunks.Trunk, error) {
	t, err := trunks.Get(ctx, c, id).Extract()
	return t, wrap(err, "getting network trunk %s", id)
}

// CreateTrunk creates a trunk. To create it with subports gophercloud's
// trunks.Subport cannot express, wrap opts with WithTrunkCreateAttrs and a
// "sub_ports" list of TrunkSubportSpec.
func CreateTrunk(ctx context.Context, c *gophercloud.ServiceClient, opts trunks.CreateOptsBuilder) (*trunks.Trunk, error) {
	t, err := trunks.Create(ctx, c, opts).Extract()
	return t, wrap(err, "creating network trunk")
}

// UpdateTrunk updates a trunk's name, description or admin state.
func UpdateTrunk(ctx context.Context, c *gophercloud.ServiceClient, id string, opts trunks.UpdateOptsBuilder) (*trunks.Trunk, error) {
	t, err := trunks.Update(ctx, c, id, opts).Extract()
	return t, wrap(err, "updating network trunk %s", id)
}

// DeleteTrunk deletes a trunk.
func DeleteTrunk(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(trunks.Delete(ctx, c, id).ExtractErr(), "deleting network trunk %s", id)
}

// ListTrunkSubports lists a trunk's subports.
func ListTrunkSubports(ctx context.Context, c *gophercloud.ServiceClient, id string) ([]trunks.Subport, error) {
	s, err := trunks.GetSubports(ctx, c, id).Extract()
	return s, wrap(err, "listing sub-ports of trunk %s", id)
}

// AddTrunkSubports adds subports to a trunk. AddTrunkSubportsOpts leaves
// the segmentation to neutron where trunks.AddSubportsOpts cannot.
func AddTrunkSubports(ctx context.Context, c *gophercloud.ServiceClient, id string, opts trunks.AddSubportsOptsBuilder) (*trunks.Trunk, error) {
	t, err := trunks.AddSubports(ctx, c, id, opts).Extract()
	return t, wrap(err, "adding sub-ports to trunk %s", id)
}

// RemoveTrunkSubports removes subports from a trunk.
func RemoveTrunkSubports(ctx context.Context, c *gophercloud.ServiceClient, id string, opts trunks.RemoveSubportsOptsBuilder) (*trunks.Trunk, error) {
	t, err := trunks.RemoveSubports(ctx, c, id, opts).Extract()
	return t, wrap(err, "removing sub-ports from trunk %s", id)
}

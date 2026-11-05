package network

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/subnetpools"
)

// SubnetPool is gophercloud's subnet pool plus what it reads wrongly.
// subnetpools.SubnetPool has its own UnmarshalJSON, which a SubnetPool
// inherits, so the functions here decode the body twice, once into each
// part, as Router's do.
type SubnetPool struct {
	subnetpools.SubnetPool
	Attrs SubnetPoolAttrs `json:"-"`
}

// SubnetPoolAttrs carries default_quota as neutron sent it: null until an
// operator sets one, where SubnetPool.DefaultQuota reads 0, a quota of
// nothing.
type SubnetPoolAttrs struct {
	DefaultQuota *int `json:"default_quota"`
}

// FindSubnetPoolID resolves a subnet pool name or ID to an ID.
func FindSubnetPoolID(ctx context.Context, c *gophercloud.ServiceClient, ref string) (string, error) {
	return findID(ctx, "subnet pool", ref, func(ctx context.Context) ([]subnetpools.SubnetPool, error) {
		pages, err := subnetpools.List(c, subnetpools.ListOpts{Name: ref}).AllPages(ctx)
		return extracted(pages, err, subnetpools.ExtractSubnetPools)
	}, func(p subnetpools.SubnetPool) string { return p.ID })
}

func extractSubnetPool(r interface{ ExtractIntoStructPtr(any, string) error }) (*SubnetPool, error) {
	var p SubnetPool
	if err := r.ExtractIntoStructPtr(&p.SubnetPool, "subnetpool"); err != nil {
		return nil, err
	}
	if err := r.ExtractIntoStructPtr(&p.Attrs, "subnetpool"); err != nil {
		return nil, err
	}
	return &p, nil
}

// ListSubnetPools lists every subnet pool matching opts. A listing's
// DefaultQuota is gophercloud's int; GetSubnetPool tells null from 0.
func ListSubnetPools(ctx context.Context, c *gophercloud.ServiceClient, opts subnetpools.ListOptsBuilder) ([]subnetpools.SubnetPool, error) {
	pages, err := subnetpools.List(c, opts).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing subnet pools")
	}
	all, err := subnetpools.ExtractSubnetPools(pages)
	return all, wrap(err, "parsing subnet pool list")
}

// GetSubnetPool reads one subnet pool.
func GetSubnetPool(ctx context.Context, c *gophercloud.ServiceClient, id string) (*SubnetPool, error) {
	p, err := extractSubnetPool(subnetpools.Get(ctx, c, id))
	return p, wrap(err, "getting subnet pool %s", id)
}

// CreateSubnetPool creates a subnet pool. Wrap opts with
// WithSubnetPoolCreateAttrs to send attributes gophercloud does not model.
func CreateSubnetPool(ctx context.Context, c *gophercloud.ServiceClient, opts subnetpools.CreateOptsBuilder) (*SubnetPool, error) {
	p, err := extractSubnetPool(subnetpools.Create(ctx, c, opts))
	return p, wrap(err, "creating subnet pool")
}

// UpdateSubnetPool updates a subnet pool. Its Prefixes replace the pool's,
// so to add one, send the current prefixes with it.
func UpdateSubnetPool(ctx context.Context, c *gophercloud.ServiceClient, id string, opts subnetpools.UpdateOptsBuilder) (*SubnetPool, error) {
	p, err := extractSubnetPool(subnetpools.Update(ctx, c, id, opts))
	return p, wrap(err, "updating subnet pool %s", id)
}

// DeleteSubnetPool deletes a subnet pool.
func DeleteSubnetPool(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(subnetpools.Delete(ctx, c, id).ExtractErr(), "deleting subnet pool %s", id)
}

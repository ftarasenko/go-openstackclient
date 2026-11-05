package loadbalancer

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/loadbalancer/v2/pools"
)

// FindPoolID resolves a pool name or ID to an ID.
func FindPoolID(ctx context.Context, c *gophercloud.ServiceClient, ref string) (string, error) {
	return findID(ctx, "pool", ref, func(ctx context.Context) ([]pools.Pool, error) {
		pages, err := pools.List(c, pools.ListOpts{Name: ref}).AllPages(ctx)
		return extracted(pages, err, pools.ExtractPools)
	}, func(p pools.Pool) string { return p.ID })
}

// ListPools lists every pool matching opts.
func ListPools(ctx context.Context, c *gophercloud.ServiceClient, opts pools.ListOptsBuilder) ([]pools.Pool, error) {
	pages, err := pools.List(c, opts).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing pools")
	}
	all, err := pools.ExtractPools(pages)
	return all, wrap(err, "parsing pool list")
}

// GetPool reads one pool.
func GetPool(ctx context.Context, c *gophercloud.ServiceClient, id string) (*pools.Pool, error) {
	p, err := pools.Get(ctx, c, id).Extract()
	return p, wrap(err, "getting pool %s", id)
}

// CreatePool creates a pool on a load balancer or a listener.
func CreatePool(ctx context.Context, c *gophercloud.ServiceClient, opts pools.CreateOptsBuilder) (*pools.Pool, error) {
	p, err := pools.Create(ctx, c, opts).Extract()
	return p, wrap(err, "creating pool")
}

// UpdatePool updates a pool.
func UpdatePool(ctx context.Context, c *gophercloud.ServiceClient, id string, opts pools.UpdateOptsBuilder) (*pools.Pool, error) {
	p, err := pools.Update(ctx, c, id, opts).Extract()
	return p, wrap(err, "updating pool %s", id)
}

// DeletePool deletes a pool.
func DeletePool(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(pools.Delete(ctx, c, id).ExtractErr(), "deleting pool %s", id)
}

// FindMemberID resolves a member name or ID to an ID within one pool.
func FindMemberID(ctx context.Context, c *gophercloud.ServiceClient, poolID, ref string) (string, error) {
	return findID(ctx, "member", ref, func(ctx context.Context) ([]pools.Member, error) {
		pages, err := pools.ListMembers(c, poolID, pools.ListMembersOpts{Name: ref}).AllPages(ctx)
		return extracted(pages, err, pools.ExtractMembers)
	}, func(m pools.Member) string { return m.ID })
}

// ListMembers lists a pool's members matching opts.
func ListMembers(ctx context.Context, c *gophercloud.ServiceClient, poolID string, opts pools.ListMembersOptsBuilder) ([]pools.Member, error) {
	pages, err := pools.ListMembers(c, poolID, opts).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing members of pool %s", poolID)
	}
	all, err := pools.ExtractMembers(pages)
	return all, wrap(err, "parsing member list")
}

// GetMember reads one member of a pool.
func GetMember(ctx context.Context, c *gophercloud.ServiceClient, poolID, id string) (*pools.Member, error) {
	m, err := pools.GetMember(ctx, c, poolID, id).Extract()
	return m, wrap(err, "getting member %s of pool %s", id, poolID)
}

// CreateMember adds a member to a pool.
func CreateMember(ctx context.Context, c *gophercloud.ServiceClient, poolID string, opts pools.CreateMemberOptsBuilder) (*pools.Member, error) {
	m, err := pools.CreateMember(ctx, c, poolID, opts).Extract()
	return m, wrap(err, "adding member to pool %s", poolID)
}

// UpdateMember updates a member of a pool.
func UpdateMember(ctx context.Context, c *gophercloud.ServiceClient, poolID, id string, opts pools.UpdateMemberOptsBuilder) (*pools.Member, error) {
	m, err := pools.UpdateMember(ctx, c, poolID, id, opts).Extract()
	return m, wrap(err, "updating member %s of pool %s", id, poolID)
}

// DeleteMember removes a member from a pool.
func DeleteMember(ctx context.Context, c *gophercloud.ServiceClient, poolID, id string) error {
	return wrap(pools.DeleteMember(ctx, c, poolID, id).ExtractErr(), "removing member %s from pool %s", id, poolID)
}

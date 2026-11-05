package loadbalancer

import (
	"context"
	"fmt"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/loadbalancer/v2/loadbalancers"
	"github.com/gophercloud/gophercloud/v2/pagination"

	"github.com/ftarasenko/go-openstackclient/pkg/openstack/resolve"
)

// wrap prefixes err with a formatted message; a nil err stays nil.
func wrap(err error, format string, args ...any) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf(format+": %w", append(args, err)...)
}

// extracted runs extract over a listing that did not fail.
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
	switch len(all) {
	case 1:
		return idOf(all[0]), nil
	case 0:
		return "", &resolve.NotFoundError{Kind: kind, Ref: ref}
	default:
		return "", &resolve.AmbiguousError{Kind: kind, Ref: ref, Count: len(all)}
	}
}

// FindLoadBalancerID resolves a load balancer name or ID to an ID. Octavia's
// name filter is exact, and names are not unique.
func FindLoadBalancerID(ctx context.Context, c *gophercloud.ServiceClient, ref string) (string, error) {
	return findID(ctx, "load balancer", ref, func(ctx context.Context) ([]loadbalancers.LoadBalancer, error) {
		pages, err := loadbalancers.List(c, loadbalancers.ListOpts{Name: ref}).AllPages(ctx)
		return extracted(pages, err, loadbalancers.ExtractLoadBalancers)
	}, func(lb loadbalancers.LoadBalancer) string { return lb.ID })
}

// ListLoadBalancers lists every load balancer matching opts.
func ListLoadBalancers(ctx context.Context, c *gophercloud.ServiceClient, opts loadbalancers.ListOptsBuilder) ([]loadbalancers.LoadBalancer, error) {
	pages, err := loadbalancers.List(c, opts).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing load balancers")
	}
	all, err := loadbalancers.ExtractLoadBalancers(pages)
	return all, wrap(err, "parsing load balancer list")
}

// GetLoadBalancer reads one load balancer.
func GetLoadBalancer(ctx context.Context, c *gophercloud.ServiceClient, id string) (*loadbalancers.LoadBalancer, error) {
	lb, err := loadbalancers.Get(ctx, c, id).Extract()
	return lb, wrap(err, "getting load balancer %s", id)
}

// CreateLoadBalancer creates a load balancer. Octavia answers while it is
// still PENDING_CREATE; WaitForLoadBalancerActive waits for it.
func CreateLoadBalancer(ctx context.Context, c *gophercloud.ServiceClient, opts loadbalancers.CreateOptsBuilder) (*loadbalancers.LoadBalancer, error) {
	lb, err := loadbalancers.Create(ctx, c, opts).Extract()
	return lb, wrap(err, "creating load balancer")
}

// UpdateLoadBalancer updates a load balancer.
func UpdateLoadBalancer(ctx context.Context, c *gophercloud.ServiceClient, id string, opts loadbalancers.UpdateOptsBuilder) (*loadbalancers.LoadBalancer, error) {
	lb, err := loadbalancers.Update(ctx, c, id, opts).Extract()
	return lb, wrap(err, "updating load balancer %s", id)
}

// DeleteLoadBalancer requests a load balancer's deletion; opts may cascade it
// to everything under it. The record stays PENDING_DELETE until octavia is
// done; WaitForLoadBalancerDeleted waits for that.
func DeleteLoadBalancer(ctx context.Context, c *gophercloud.ServiceClient, id string, opts loadbalancers.DeleteOptsBuilder) error {
	return wrap(loadbalancers.Delete(ctx, c, id, opts).ExtractErr(), "deleting load balancer %s", id)
}

// FailoverLoadBalancer requests a failover of a load balancer's amphorae.
func FailoverLoadBalancer(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(loadbalancers.Failover(ctx, c, id).ExtractErr(), "failing over load balancer %s", id)
}

// GetLoadBalancerStats reads a load balancer's traffic counters.
func GetLoadBalancerStats(ctx context.Context, c *gophercloud.ServiceClient, id string) (*loadbalancers.Stats, error) {
	s, err := loadbalancers.GetStats(ctx, c, id).Extract()
	return s, wrap(err, "getting statistics for load balancer %s", id)
}

// GetLoadBalancerStatuses reads a load balancer's status tree: the load
// balancer, its listeners, their pools and the pools' members and monitor.
func GetLoadBalancerStatuses(ctx context.Context, c *gophercloud.ServiceClient, id string) (*loadbalancers.StatusTree, error) {
	t, err := loadbalancers.GetStatuses(ctx, c, id).Extract()
	return t, wrap(err, "getting status tree for load balancer %s", id)
}

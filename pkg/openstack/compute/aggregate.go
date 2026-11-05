package compute

import (
	"context"
	"fmt"
	"strconv"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/aggregates"

	"github.com/ftarasenko/go-openstackclient/pkg/openstack/resolve"
)

// Host aggregates are keyed by nova's integer ID.

// ListAggregates lists every host aggregate.
func ListAggregates(ctx context.Context, c *gophercloud.ServiceClient) ([]aggregates.Aggregate, error) {
	pages, err := aggregates.List(c).AllPages(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing aggregates: %w", err)
	}
	all, err := aggregates.ExtractAggregates(pages)
	if err != nil {
		return nil, fmt.Errorf("parsing aggregate list: %w", err)
	}
	return all, nil
}

// FindAggregateID returns the ID of the aggregate ref names. A ref that
// parses as an integer is taken as the ID without a request. Otherwise no
// match is a *resolve.NotFoundError and several are a *resolve.AmbiguousError.
func FindAggregateID(ctx context.Context, c *gophercloud.ServiceClient, ref string) (int, error) {
	if id, err := strconv.Atoi(ref); err == nil {
		return id, nil
	}
	all, err := ListAggregates(ctx, c)
	if err != nil {
		return 0, err
	}
	var matches []int
	for _, agg := range all {
		if agg.Name == ref {
			matches = append(matches, agg.ID)
		}
	}
	switch len(matches) {
	case 0:
		return 0, &resolve.NotFoundError{Kind: "aggregate", Ref: ref}
	case 1:
		return matches[0], nil
	default:
		return 0, &resolve.AmbiguousError{Kind: "aggregate", Ref: ref, Count: len(matches)}
	}
}

// HostAggregates maps each compute host to the names of the aggregates that
// hold it, in the order nova lists them.
func HostAggregates(ctx context.Context, c *gophercloud.ServiceClient) (map[string][]string, error) {
	all, err := ListAggregates(ctx, c)
	if err != nil {
		return nil, err
	}
	byHost := map[string][]string{}
	for _, agg := range all {
		for _, h := range agg.Hosts {
			byHost[h] = append(byHost[h], agg.Name)
		}
	}
	return byHost, nil
}

// GetAggregate reads one aggregate.
func GetAggregate(ctx context.Context, c *gophercloud.ServiceClient, id int) (*aggregates.Aggregate, error) {
	agg, err := aggregates.Get(ctx, c, id).Extract()
	if err != nil {
		return nil, fmt.Errorf("showing aggregate %d: %w", id, err)
	}
	return agg, nil
}

// CreateAggregate creates an aggregate. Its metadata is set by a separate
// call ([SetAggregateMetadata]).
func CreateAggregate(ctx context.Context, c *gophercloud.ServiceClient, opts aggregates.CreateOpts) (*aggregates.Aggregate, error) {
	agg, err := aggregates.Create(ctx, c, opts).Extract()
	if err != nil {
		return nil, fmt.Errorf("creating aggregate %q: %w", opts.Name, err)
	}
	return agg, nil
}

// UpdateAggregate renames an aggregate or moves it to another availability
// zone.
func UpdateAggregate(ctx context.Context, c *gophercloud.ServiceClient, id int, opts aggregates.UpdateOpts) (*aggregates.Aggregate, error) {
	agg, err := aggregates.Update(ctx, c, id, opts).Extract()
	if err != nil {
		return nil, fmt.Errorf("updating aggregate %d: %w", id, err)
	}
	return agg, nil
}

// SetAggregateMetadata merges metadata into an aggregate's; a key whose value
// is nil is removed.
func SetAggregateMetadata(ctx context.Context, c *gophercloud.ServiceClient, id int, metadata map[string]any) (*aggregates.Aggregate, error) {
	agg, err := aggregates.SetMetadata(ctx, c, id, aggregates.SetMetadataOpts{Metadata: metadata}).Extract()
	if err != nil {
		return nil, fmt.Errorf("setting properties on aggregate %d: %w", id, err)
	}
	return agg, nil
}

// DeleteAggregate deletes an aggregate; nova refuses one that still holds
// hosts.
func DeleteAggregate(ctx context.Context, c *gophercloud.ServiceClient, id int) error {
	if err := aggregates.Delete(ctx, c, id).ExtractErr(); err != nil {
		return fmt.Errorf("deleting aggregate %d: %w", id, err)
	}
	return nil
}

// AddAggregateHost adds a compute host to an aggregate.
func AddAggregateHost(ctx context.Context, c *gophercloud.ServiceClient, id int, host string) (*aggregates.Aggregate, error) {
	agg, err := aggregates.AddHost(ctx, c, id, aggregates.AddHostOpts{Host: host}).Extract()
	if err != nil {
		return nil, fmt.Errorf("adding host %q to aggregate %d: %w", host, id, err)
	}
	return agg, nil
}

// RemoveAggregateHost removes a compute host from an aggregate.
func RemoveAggregateHost(ctx context.Context, c *gophercloud.ServiceClient, id int, host string) (*aggregates.Aggregate, error) {
	agg, err := aggregates.RemoveHost(ctx, c, id, aggregates.RemoveHostOpts{Host: host}).Extract()
	if err != nil {
		return nil, fmt.Errorf("removing host %q from aggregate %d: %w", host, id, err)
	}
	return agg, nil
}

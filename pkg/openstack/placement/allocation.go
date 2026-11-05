package placement

import (
	"context"
	"slices"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/placement/v1/allocationcandidates"
	"github.com/gophercloud/gophercloud/v2/openstack/placement/v1/allocations"
)

// GetAllocations reads a consumer's allocations, keyed by resource provider.
func GetAllocations(ctx context.Context, c *gophercloud.ServiceClient, consumer string) (*allocations.Allocations, error) {
	a, err := allocations.Get(ctx, c, consumer).Extract()
	return a, wrap(err, "showing the allocations of consumer %s", consumer)
}

// UpdateAllocations replaces a consumer's allocations wholesale. From
// ConsumerTypeMicroversion on, opts must carry a ConsumerType.
func UpdateAllocations(ctx context.Context, c *gophercloud.ServiceClient, consumer string,
	opts allocations.UpdateOptsBuilder,
) error {
	err := allocations.Update(ctx, c, consumer, opts).ExtractErr()
	return wrap(err, "setting the allocations of consumer %s", consumer)
}

// DeleteAllocations removes every allocation of a consumer.
func DeleteAllocations(ctx context.Context, c *gophercloud.ServiceClient, consumer string) error {
	err := allocations.Delete(ctx, c, consumer).ExtractErr()
	return wrap(err, "deleting allocations for consumer %s", consumer)
}

// RemoveAllocations drops a consumer's allocations against providers and of
// classes, and reports whether any are left. Placement has no partial delete,
// so it reads the allocations, writes back the rest, and deletes them all
// when nothing is left (or when neither list names anything) — placement
// rejects an empty update.
func RemoveAllocations(ctx context.Context, c *gophercloud.ServiceClient, consumer string,
	providers, classes []string,
) (bool, error) {
	if len(providers) == 0 && len(classes) == 0 {
		return false, deleteAllAllocations(ctx, c, consumer)
	}
	current, err := allocations.Get(ctx, c, consumer).Extract()
	if err != nil {
		return false, wrap(err, "reading the allocations of consumer %s", consumer)
	}
	kept := keepAllocations(current.Allocations, providers, classes)
	if len(kept) == 0 {
		return false, deleteAllAllocations(ctx, c, consumer)
	}
	err = allocations.Update(ctx, c, consumer, allocations.UpdateOpts{Allocations: kept}).ExtractErr()
	return err == nil, wrap(err, "updating the allocations of consumer %s", consumer)
}

func deleteAllAllocations(ctx context.Context, c *gophercloud.ServiceClient, consumer string) error {
	err := allocations.Delete(ctx, c, consumer).ExtractErr()
	return wrap(err, "deleting the allocations of consumer %s", consumer)
}

// keepAllocations computes the allocations that survive a removal: every
// provider not named, minus every class named. A provider left with no class
// is dropped, since placement rejects an empty resources object.
func keepAllocations(current map[string]allocations.ProviderAllocations,
	providers, classes []string,
) map[string]allocations.ProviderAllocationsOpts {
	kept := map[string]allocations.ProviderAllocationsOpts{}
	for providerID, alloc := range current {
		if slices.Contains(providers, providerID) {
			continue
		}
		resources := map[string]int{}
		for class, used := range alloc.Resources {
			if slices.Contains(classes, class) {
				continue
			}
			resources[class] = used
		}
		if len(resources) > 0 {
			kept[providerID] = allocations.ProviderAllocationsOpts{Resources: resources}
		}
	}
	return kept
}

// ListAllocationCandidates lists the providers that can satisfy a resource
// request. opts.Resources is placement's form, CLASS:amount,…
func ListAllocationCandidates(ctx context.Context, c *gophercloud.ServiceClient,
	opts allocationcandidates.ListOptsBuilder,
) (*allocationcandidates.AllocationCandidates, error) {
	pages, err := allocationcandidates.List(c, opts).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing allocation candidates")
	}
	all, err := allocationcandidates.ExtractAllocationCandidates(pages)
	return all, wrap(err, "parsing the allocation candidate list")
}

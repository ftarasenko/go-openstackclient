package placement

import (
	"context"
	"fmt"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/placement/v1/resourceproviders"
)

// wrap prefixes err with a formatted message; a nil err stays nil.
func wrap(err error, format string, args ...any) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf(format+": %w", append(args, err)...)
}

// ListResourceProviders lists resource providers matching opts.
func ListResourceProviders(ctx context.Context, c *gophercloud.ServiceClient,
	opts resourceproviders.ListOptsBuilder,
) ([]resourceproviders.ResourceProvider, error) {
	pages, err := resourceproviders.List(c, opts).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing resource providers")
	}
	all, err := resourceproviders.ExtractResourceProviders(pages)
	return all, wrap(err, "parsing resource provider list")
}

// GetResourceProvider reads one resource provider.
func GetResourceProvider(ctx context.Context, c *gophercloud.ServiceClient, id string) (*resourceproviders.ResourceProvider, error) {
	rp, err := resourceproviders.Get(ctx, c, id).Extract()
	return rp, wrap(err, "getting resource provider %s", id)
}

// CreateResourceProvider creates a resource provider.
func CreateResourceProvider(ctx context.Context, c *gophercloud.ServiceClient,
	opts resourceproviders.CreateOpts,
) (*resourceproviders.ResourceProvider, error) {
	rp, err := resourceproviders.Create(ctx, c, opts).Extract()
	return rp, wrap(err, "creating resource provider %q", opts.Name)
}

// UpdateResourceProvider renames or re-parents a resource provider; an empty
// ParentProviderUUID makes it a root (placement 1.37 or later).
func UpdateResourceProvider(ctx context.Context, c *gophercloud.ServiceClient, id string,
	opts resourceproviders.UpdateOptsBuilder,
) (*resourceproviders.ResourceProvider, error) {
	rp, err := resourceproviders.Update(ctx, c, id, opts).Extract()
	return rp, wrap(err, "updating resource provider %s", id)
}

// DeleteResourceProvider deletes a resource provider.
func DeleteResourceProvider(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(resourceproviders.Delete(ctx, c, id).ExtractErr(), "deleting resource provider %s", id)
}

// GetProviderAllocations reads the allocations against a resource provider,
// keyed by consumer.
func GetProviderAllocations(ctx context.Context, c *gophercloud.ServiceClient, id string) (*resourceproviders.ResourceProviderAllocations, error) {
	a, err := resourceproviders.GetAllocations(ctx, c, id).Extract()
	return a, wrap(err, "getting allocations for resource provider %s", id)
}

// GetProviderUsages reads a resource provider's usage per resource class.
func GetProviderUsages(ctx context.Context, c *gophercloud.ServiceClient, id string) (*resourceproviders.ResourceProviderUsage, error) {
	u, err := resourceproviders.GetUsages(ctx, c, id).Extract()
	return u, wrap(err, "getting usages for resource provider %s", id)
}

// GetProviderTraits reads a resource provider's traits.
func GetProviderTraits(ctx context.Context, c *gophercloud.ServiceClient, id string) (*resourceproviders.ResourceProviderTraits, error) {
	t, err := resourceproviders.GetTraits(ctx, c, id).Extract()
	return t, wrap(err, "listing traits for resource provider %s", id)
}

// SetProviderTraits replaces a resource provider's traits (placement has no
// merge mode) at its current generation.
func SetProviderTraits(ctx context.Context, c *gophercloud.ServiceClient, id string,
	traits []string,
) (*resourceproviders.ResourceProviderTraits, error) {
	current, err := resourceproviders.GetTraits(ctx, c, id).Extract()
	if err != nil {
		return nil, wrap(err, "reading resource provider %s before writing its traits", id)
	}
	got, err := resourceproviders.UpdateTraits(ctx, c, id, resourceproviders.UpdateTraitsOpts{
		ResourceProviderGeneration: current.ResourceProviderGeneration,
		Traits:                     traits,
	}).Extract()
	return got, wrap(err, "setting the traits of resource provider %s", id)
}

// DeleteProviderTraits removes every trait from a resource provider.
func DeleteProviderTraits(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(resourceproviders.DeleteTraits(ctx, c, id).ExtractErr(), "deleting the traits of resource provider %s", id)
}

// GetProviderAggregates reads the aggregates a resource provider belongs to.
func GetProviderAggregates(ctx context.Context, c *gophercloud.ServiceClient, id string) (*resourceproviders.ResourceProviderAggregates, error) {
	a, err := resourceproviders.GetAggregates(ctx, c, id).Extract()
	return a, wrap(err, "listing aggregates for resource provider %s", id)
}

// SetProviderAggregates replaces the aggregates a resource provider belongs
// to at its current generation. A client at "latest" works: the body shape
// gophercloud picks needs a number, which is read from the version document.
func SetProviderAggregates(ctx context.Context, c *gophercloud.ServiceClient, id string,
	aggregates []string,
) (*resourceproviders.ResourceProviderAggregates, error) {
	current, err := resourceproviders.GetAggregates(ctx, c, id).Extract()
	if err != nil {
		return nil, wrap(err, "reading resource provider %s before writing its aggregates", id)
	}
	got, err := resourceproviders.UpdateAggregates(ctx, concreteClient(ctx, c), id, resourceproviders.UpdateAggregatesOpts{
		ResourceProviderGeneration: current.ResourceProviderGeneration,
		Aggregates:                 aggregates,
	}).Extract()
	return got, wrap(err, "setting the aggregates of resource provider %s", id)
}

package placement

import (
	"context"
	"maps"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/placement/v1/resourceproviders"
)

// InventoryFields are the inventory fields to send (total, reserved,
// min_unit, max_unit, step_size, allocation_ratio); placement applies its
// defaults to the rest. resourceproviders.Inventory cannot leave a field out,
// and its zero step_size, min_unit or max_unit fails placement's minimum of 1.
type InventoryFields map[string]any

// updateInventoryOpts is one class's body: the generation plus the fields.
type updateInventoryOpts struct {
	generation int
	fields     InventoryFields
}

func (o updateInventoryOpts) ToResourceProviderUpdateInventoryMap() (map[string]any, error) {
	b := map[string]any{"resource_provider_generation": o.generation}
	maps.Copy(b, o.fields)
	return b, nil
}

// updateInventoriesOpts is the whole-provider body.
type updateInventoriesOpts struct {
	generation  int
	inventories map[string]InventoryFields
}

func (o updateInventoriesOpts) ToResourceProviderUpdateInventoriesMap() (map[string]any, error) {
	inv := make(map[string]any, len(o.inventories))
	for class, fields := range o.inventories {
		inv[class] = map[string]any(fields)
	}
	return map[string]any{
		"resource_provider_generation": o.generation,
		"inventories":                  inv,
	}, nil
}

// GetInventories reads a resource provider's inventories, keyed by resource
// class.
func GetInventories(ctx context.Context, c *gophercloud.ServiceClient, id string) (*resourceproviders.ResourceProviderInventories, error) {
	inv, err := resourceproviders.GetInventories(ctx, c, id).Extract()
	return inv, wrap(err, "listing inventories for resource provider %s", id)
}

// GetInventory reads one resource class's inventory on a provider.
func GetInventory(ctx context.Context, c *gophercloud.ServiceClient, id, class string) (*resourceproviders.ResourceProviderInventory, error) {
	inv, err := resourceproviders.GetInventory(ctx, c, id, class).Extract()
	return inv, wrap(err, "getting %s inventory for resource provider %s", class, id)
}

// readGeneration is the provider generation an inventory write needs.
func readGeneration(ctx context.Context, c *gophercloud.ServiceClient, id string) (int, error) {
	current, err := resourceproviders.GetInventories(ctx, c, id).Extract()
	if err != nil {
		return 0, wrap(err, "reading resource provider %s before writing its inventory", id)
	}
	return current.ResourceProviderGeneration, nil
}

// SetInventory replaces one resource class's inventory on a provider at its
// current generation.
func SetInventory(ctx context.Context, c *gophercloud.ServiceClient, id, class string,
	fields InventoryFields,
) (*resourceproviders.ResourceProviderInventory, error) {
	generation, err := readGeneration(ctx, c, id)
	if err != nil {
		return nil, err
	}
	got, err := resourceproviders.UpdateInventory(ctx, c, id, class,
		updateInventoryOpts{generation: generation, fields: fields}).Extract()
	return got, wrap(err, "setting %s inventory on resource provider %s", class, id)
}

// SetInventories replaces a provider's whole inventory at its current
// generation: a class left out of inventories is removed.
func SetInventories(ctx context.Context, c *gophercloud.ServiceClient, id string,
	inventories map[string]InventoryFields,
) (*resourceproviders.ResourceProviderInventories, error) {
	generation, err := readGeneration(ctx, c, id)
	if err != nil {
		return nil, err
	}
	got, err := resourceproviders.UpdateInventories(ctx, c, id,
		updateInventoriesOpts{generation: generation, inventories: inventories}).Extract()
	return got, wrap(err, "setting the inventory of resource provider %s", id)
}

// DeleteInventory removes one resource class's inventory from a provider.
func DeleteInventory(ctx context.Context, c *gophercloud.ServiceClient, id, class string) error {
	err := resourceproviders.DeleteInventory(ctx, c, id, class).ExtractErr()
	return wrap(err, "deleting %s inventory from resource provider %s", class, id)
}

// DeleteInventories removes every inventory from a provider.
func DeleteInventories(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	err := resourceproviders.DeleteInventories(ctx, c, id).ExtractErr()
	return wrap(err, "deleting the inventory of resource provider %s", id)
}

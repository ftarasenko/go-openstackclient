// Package placement is the placement half of koc's API: resource providers
// and their inventories, traits, aggregates, usages and allocations; resource
// classes; traits; consumer allocations and allocation candidates; project
// usage.
//
// Functions take a *gophercloud.ServiceClient from openstack.Session.Placement
// and return gophercloud v2 types. Placement addresses everything by UUID or
// name, so there are no resolvers.
//
// Placement guards a provider's inventory, traits and aggregates with a
// generation: a write must carry the current one. SetInventory,
// SetInventories, SetProviderTraits and SetProviderAggregates read it first,
// so two writes in a row need no read in between; a concurrent change still
// fails with 409.
package placement

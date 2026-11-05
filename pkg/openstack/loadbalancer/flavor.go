package loadbalancer

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/loadbalancer/v2/flavorprofiles"
	"github.com/gophercloud/gophercloud/v2/openstack/loadbalancer/v2/flavors"
)

// FindFlavorID resolves a load balancer flavor name or ID to an ID.
func FindFlavorID(ctx context.Context, c *gophercloud.ServiceClient, ref string) (string, error) {
	return findID(ctx, "load balancer flavor", ref, func(ctx context.Context) ([]flavors.Flavor, error) {
		pages, err := flavors.List(c, flavors.ListOpts{Name: ref}).AllPages(ctx)
		return extracted(pages, err, flavors.ExtractFlavors)
	}, func(f flavors.Flavor) string { return f.ID })
}

// ListFlavors lists every load balancer flavor matching opts.
func ListFlavors(ctx context.Context, c *gophercloud.ServiceClient, opts flavors.ListOptsBuilder) ([]flavors.Flavor, error) {
	pages, err := flavors.List(c, opts).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing load balancer flavors")
	}
	all, err := flavors.ExtractFlavors(pages)
	return all, wrap(err, "parsing flavor list")
}

// GetFlavor reads one load balancer flavor.
func GetFlavor(ctx context.Context, c *gophercloud.ServiceClient, id string) (*flavors.Flavor, error) {
	f, err := flavors.Get(ctx, c, id).Extract()
	return f, wrap(err, "getting load balancer flavor %s", id)
}

// CreateFlavor creates a load balancer flavor.
func CreateFlavor(ctx context.Context, c *gophercloud.ServiceClient, opts flavors.CreateOptsBuilder) (*flavors.Flavor, error) {
	f, err := flavors.Create(ctx, c, opts).Extract()
	return f, wrap(err, "creating load balancer flavor")
}

// UpdateFlavor updates a load balancer flavor. flavors.UpdateOpts drops
// enabled=false; SetFlavorEnabled sends it.
func UpdateFlavor(ctx context.Context, c *gophercloud.ServiceClient, id string, opts flavors.UpdateOptsBuilder) (*flavors.Flavor, error) {
	f, err := flavors.Update(ctx, c, id, opts).Extract()
	return f, wrap(err, "updating load balancer flavor %s", id)
}

// SetFlavorEnabled enables or disables a load balancer flavor with a raw PUT
// that always sends enabled. The updated flavor is not decoded; GetFlavor
// reads it.
func SetFlavorEnabled(ctx context.Context, c *gophercloud.ServiceClient, id string, enabled bool) error {
	body := map[string]any{"flavor": map[string]any{"enabled": enabled}}
	resp, err := c.Put(ctx, c.ServiceURL("lbaas", "flavors", id), body, nil, &gophercloud.RequestOpts{
		OkCodes: []int{200},
	})
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	_, _, err = gophercloud.ParseResponse(resp, err)
	return wrap(err, "setting enabled=%t on load balancer flavor %s", enabled, id)
}

// DeleteFlavor deletes a load balancer flavor.
func DeleteFlavor(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(flavors.Delete(ctx, c, id).ExtractErr(), "deleting load balancer flavor %s", id)
}

// FindFlavorProfileID resolves a flavor profile name or ID to an ID. It
// lists every profile and matches the name client-side.
func FindFlavorProfileID(ctx context.Context, c *gophercloud.ServiceClient, ref string) (string, error) {
	return findID(ctx, "flavor profile", ref, func(ctx context.Context) ([]flavorprofiles.FlavorProfile, error) {
		pages, err := flavorprofiles.List(c, flavorprofiles.ListOpts{}).AllPages(ctx)
		all, err := extracted(pages, err, flavorprofiles.ExtractFlavorProfiles)
		if err != nil {
			return nil, err
		}
		named := make([]flavorprofiles.FlavorProfile, 0, 1)
		for _, p := range all {
			if p.Name == ref {
				named = append(named, p)
			}
		}
		return named, nil
	}, func(p flavorprofiles.FlavorProfile) string { return p.ID })
}

// ListFlavorProfiles lists every flavor profile matching opts.
func ListFlavorProfiles(ctx context.Context, c *gophercloud.ServiceClient, opts flavorprofiles.ListOptsBuilder) ([]flavorprofiles.FlavorProfile, error) {
	pages, err := flavorprofiles.List(c, opts).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing flavor profiles")
	}
	all, err := flavorprofiles.ExtractFlavorProfiles(pages)
	return all, wrap(err, "parsing flavor profile list")
}

// GetFlavorProfile reads one flavor profile.
func GetFlavorProfile(ctx context.Context, c *gophercloud.ServiceClient, id string) (*flavorprofiles.FlavorProfile, error) {
	p, err := flavorprofiles.Get(ctx, c, id).Extract()
	return p, wrap(err, "getting flavor profile %s", id)
}

// CreateFlavorProfile creates a flavor profile.
func CreateFlavorProfile(ctx context.Context, c *gophercloud.ServiceClient, opts flavorprofiles.CreateOptsBuilder) (*flavorprofiles.FlavorProfile, error) {
	p, err := flavorprofiles.Create(ctx, c, opts).Extract()
	return p, wrap(err, "creating flavor profile")
}

// UpdateFlavorProfile updates a flavor profile.
func UpdateFlavorProfile(ctx context.Context, c *gophercloud.ServiceClient, id string, opts flavorprofiles.UpdateOptsBuilder) (*flavorprofiles.FlavorProfile, error) {
	p, err := flavorprofiles.Update(ctx, c, id, opts).Extract()
	return p, wrap(err, "updating flavor profile %s", id)
}

// DeleteFlavorProfile deletes a flavor profile.
func DeleteFlavorProfile(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(flavorprofiles.Delete(ctx, c, id).ExtractErr(), "deleting flavor profile %s", id)
}

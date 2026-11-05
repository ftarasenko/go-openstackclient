package compute

import (
	"context"
	"fmt"
	"net/http"
	"sort"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/flavors"

	"github.com/ftarasenko/go-openstackclient/pkg/openstack/paging"
	"github.com/ftarasenko/go-openstackclient/pkg/openstack/resolve"
)

// FlavorDescriptionMicroversion is the nova microversion that added a
// flavor's description, and with it PUT /flavors/{id}, the only way to
// change one.
const FlavorDescriptionMicroversion = "2.55"

// ListFlavors lists the flavors opts selects, with their details. Nova treats
// opts.Limit only as a page size; here it is a hard cap on the result, and
// paging stops once it is met. Zero means no cap.
func ListFlavors(ctx context.Context, c *gophercloud.ServiceClient, opts flavors.ListOpts) ([]flavors.Flavor, error) {
	all, err := paging.Collect(ctx, flavors.ListDetail(c, opts), opts.Limit, flavors.ExtractFlavors)
	if err != nil {
		return nil, fmt.Errorf("listing flavors: %w", err)
	}
	return all, nil
}

// GetFlavor reads one flavor by ID.
func GetFlavor(ctx context.Context, c *gophercloud.ServiceClient, id string) (*flavors.Flavor, error) {
	fl, err := flavors.Get(ctx, c, id).Extract()
	if err != nil {
		return nil, fmt.Errorf("showing flavor %q: %w", id, err)
	}
	return fl, nil
}

// FindFlavor returns the flavor whose ID or name is ref. An ID match wins
// over a name match; nova has no name filter, so both are matched against
// the full listing.
//
// Nova's listing defaults to public flavors, even for an admin, so a miss
// there is retried across every access type. That is what lets an admin name
// a private flavor; for a non-admin token nova ignores the wider filter.
//
// No match is a *resolve.NotFoundError and several are a
// *resolve.AmbiguousError, as in package resolve.
func FindFlavor(ctx context.Context, c *gophercloud.ServiceClient, ref string) (*flavors.Flavor, error) {
	for _, access := range []flavors.AccessType{"", flavors.AllAccess} {
		fl, err := findFlavor(ctx, c, ref, access)
		if err != nil || fl != nil {
			return fl, err
		}
	}
	return nil, &resolve.NotFoundError{Kind: "flavor", Ref: ref}
}

// findFlavor returns the flavor matching ref within one access view, or nil
// when that view holds no match.
func findFlavor(ctx context.Context, c *gophercloud.ServiceClient, ref string, access flavors.AccessType) (*flavors.Flavor, error) {
	pages, err := flavors.ListDetail(c, flavors.ListOpts{AccessType: access}).AllPages(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolving flavor %q: %w", ref, err)
	}
	all, err := flavors.ExtractFlavors(pages)
	if err != nil {
		return nil, fmt.Errorf("resolving flavor %q: %w", ref, err)
	}
	var byName []int
	for i := range all {
		if all[i].ID == ref {
			return &all[i], nil
		}
		if all[i].Name == ref {
			byName = append(byName, i)
		}
	}
	switch len(byName) {
	case 0:
		return nil, nil
	case 1:
		return &all[byName[0]], nil
	default:
		return nil, &resolve.AmbiguousError{Kind: "flavor", Ref: ref, Count: len(byName)}
	}
}

// CreateFlavor creates a flavor. Nova takes its access list and extra specs
// in separate calls ([AddFlavorAccess], [SetFlavorExtraSpecs]).
func CreateFlavor(ctx context.Context, c *gophercloud.ServiceClient, opts flavors.CreateOpts) (*flavors.Flavor, error) {
	fl, err := flavors.Create(ctx, c, opts).Extract()
	if err != nil {
		return nil, fmt.Errorf("creating flavor %q: %w", opts.Name, err)
	}
	return fl, nil
}

// DeleteFlavor deletes a flavor by ID.
func DeleteFlavor(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	if err := flavors.Delete(ctx, c, id).ExtractErr(); err != nil {
		return fmt.Errorf("deleting flavor %q: %w", id, err)
	}
	return nil
}

// ListFlavorAccess lists the projects granted access to a private flavor. A
// public flavor has no access list; nova answers 404.
func ListFlavorAccess(ctx context.Context, c *gophercloud.ServiceClient, id string) ([]flavors.FlavorAccess, error) {
	pages, err := flavors.ListAccesses(c, id).AllPages(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing project access for flavor %q: %w", id, err)
	}
	access, err := flavors.ExtractAccesses(pages)
	if err != nil {
		return nil, fmt.Errorf("parsing project access for flavor %q: %w", id, err)
	}
	return access, nil
}

// AddFlavorAccess grants a project access to a private flavor and returns the
// flavor's access list after the grant.
func AddFlavorAccess(ctx context.Context, c *gophercloud.ServiceClient, id, projectID string) ([]flavors.FlavorAccess, error) {
	access, err := flavors.AddAccess(ctx, c, id, flavors.AddAccessOpts{Tenant: projectID}).Extract()
	if err != nil {
		return nil, fmt.Errorf("granting project %q access to flavor %q: %w", projectID, id, err)
	}
	return access, nil
}

// RemoveFlavorAccess revokes a project's access to a private flavor and
// returns the flavor's access list after the revocation.
func RemoveFlavorAccess(ctx context.Context, c *gophercloud.ServiceClient, id, projectID string) ([]flavors.FlavorAccess, error) {
	access, err := flavors.RemoveAccess(ctx, c, id, flavors.RemoveAccessOpts{Tenant: projectID}).Extract()
	if err != nil {
		return nil, fmt.Errorf("revoking project %q access to flavor %q: %w", projectID, id, err)
	}
	return access, nil
}

// SetFlavorExtraSpecs adds or changes a flavor's extra specs (the CLI's
// properties) and returns the specs nova stored.
func SetFlavorExtraSpecs(ctx context.Context, c *gophercloud.ServiceClient, id string, specs map[string]string) (map[string]string, error) {
	set, err := flavors.CreateExtraSpecs(ctx, c, id, flavors.ExtraSpecsOpts(specs)).Extract()
	if err != nil {
		return nil, fmt.Errorf("setting properties on flavor %q: %w", id, err)
	}
	return set, nil
}

// DeleteFlavorExtraSpec removes one extra spec from a flavor.
func DeleteFlavorExtraSpec(ctx context.Context, c *gophercloud.ServiceClient, id, key string) error {
	if err := flavors.DeleteExtraSpec(ctx, c, id, key).ExtractErr(); err != nil {
		return fmt.Errorf("unsetting property %q on flavor %q: %w", key, id, err)
	}
	return nil
}

// ClearFlavorExtraSpecs removes every extra spec from a flavor. Nova has no
// bulk delete, so the specs are listed and deleted one key at a time, in
// sorted order.
func ClearFlavorExtraSpecs(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	specs, err := flavors.ListExtraSpecs(ctx, c, id).Extract()
	if err != nil {
		return fmt.Errorf("listing properties of flavor %q: %w", id, err)
	}
	keys := make([]string, 0, len(specs))
	for k := range specs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if err := DeleteFlavorExtraSpec(ctx, c, id, k); err != nil {
			return err
		}
	}
	return nil
}

// UpdateFlavorDescription sets a flavor's description, or clears it when
// description is empty. It needs nova [FlavorDescriptionMicroversion] or
// later.
//
// The PUT is raw because gophercloud's flavors.UpdateOpts tags Description
// omitempty and cannot clear it: an empty string sends {"flavor": {}}, which
// nova rejects. Here a clear sends an explicit null.
func UpdateFlavorDescription(ctx context.Context, c *gophercloud.ServiceClient, id, description string) error {
	var value any
	if description != "" {
		value = description
	}
	body := map[string]any{"flavor": map[string]any{"description": value}}
	resp, err := c.Put(ctx, c.ServiceURL("flavors", id), body, nil, &gophercloud.RequestOpts{
		OkCodes: []int{http.StatusOK},
	})
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if err != nil {
		return fmt.Errorf("setting description on flavor %q: %w", id, err)
	}
	return nil
}

package placement

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/placement/v1/resourceclasses"
	"github.com/gophercloud/gophercloud/v2/openstack/placement/v1/traits"
	"github.com/gophercloud/gophercloud/v2/openstack/placement/v1/usages"
)

// ListResourceClasses lists every resource class, standard and custom.
func ListResourceClasses(ctx context.Context, c *gophercloud.ServiceClient) ([]resourceclasses.ResourceClass, error) {
	pages, err := resourceclasses.List(c).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing resource classes")
	}
	all, err := resourceclasses.ExtractResourceClasses(pages)
	return all, wrap(err, "parsing the resource class list")
}

// GetResourceClass reads one resource class.
func GetResourceClass(ctx context.Context, c *gophercloud.ServiceClient, name string) (*resourceclasses.ResourceClass, error) {
	rc, err := resourceclasses.Get(ctx, c, name).Extract()
	return rc, wrap(err, "showing resource class %s", name)
}

// CreateResourceClass creates a custom (CUSTOM_*) resource class.
func CreateResourceClass(ctx context.Context, c *gophercloud.ServiceClient, name string) error {
	err := resourceclasses.Create(ctx, c, resourceclasses.CreateOpts{Name: name}).ExtractErr()
	return wrap(err, "creating resource class %s", name)
}

// EnsureResourceClass creates a custom resource class unless it exists
// (placement's PUT: a no-op for an existing class, not a rename).
func EnsureResourceClass(ctx context.Context, c *gophercloud.ServiceClient, name string) error {
	return wrap(resourceclasses.Update(ctx, c, name).ExtractErr(), "setting resource class %s", name)
}

// DeleteResourceClass deletes a custom resource class.
func DeleteResourceClass(ctx context.Context, c *gophercloud.ServiceClient, name string) error {
	return wrap(resourceclasses.Delete(ctx, c, name).ExtractErr(), "deleting resource class %s", name)
}

// ListTraits lists trait names matching opts.
func ListTraits(ctx context.Context, c *gophercloud.ServiceClient, opts traits.ListOptsBuilder) ([]string, error) {
	pages, err := traits.List(c, opts).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing traits")
	}
	all, err := traits.ExtractTraits(pages)
	return all, wrap(err, "parsing trait list")
}

// GetTrait succeeds when the trait exists (placement answers 204, no body).
func GetTrait(ctx context.Context, c *gophercloud.ServiceClient, name string) error {
	return wrap(traits.Get(ctx, c, name).ExtractErr(), "showing trait %s", name)
}

// CreateTrait creates a custom (CUSTOM_*) trait.
func CreateTrait(ctx context.Context, c *gophercloud.ServiceClient, name string) error {
	return wrap(traits.Create(ctx, c, name).ExtractErr(), "creating trait %s", name)
}

// DeleteTrait deletes a custom trait.
func DeleteTrait(ctx context.Context, c *gophercloud.ServiceClient, name string) error {
	return wrap(traits.Delete(ctx, c, name).ExtractErr(), "deleting trait %s", name)
}

// GetProjectUsage reads a project's usage across every provider, optionally
// for one user, as one total per resource class. From placement 1.38 the
// usage is grouped by consumer type; the totals sum across the types.
func GetProjectUsage(ctx context.Context, c *gophercloud.ServiceClient, projectID, userID string) (map[string]int, error) {
	var raw struct {
		Usages map[string]json.RawMessage `json:"usages"`
	}
	err := usages.Get(ctx, c, usages.GetOpts{ProjectID: projectID, UserID: userID}).ExtractInto(&raw)
	if err != nil {
		return nil, wrap(err, "showing resource usage for project %s", projectID)
	}
	perClass, err := usageByClass(raw.Usages)
	return perClass, wrap(err, "showing resource usage for project %s", projectID)
}

// usageByClass folds /usages into one total per resource class. Before 1.38
// each value is a class's usage; from 1.38 the classes sit under each consumer
// type (INSTANCE, MIGRATION, unknown) beside a consumer_count.
func usageByClass(in map[string]json.RawMessage) (map[string]int, error) {
	out := map[string]int{}
	for key, v := range in {
		var n int
		if err := json.Unmarshal(v, &n); err == nil {
			out[key] += n
			continue
		}
		var byType map[string]int
		if err := json.Unmarshal(v, &byType); err != nil {
			return nil, fmt.Errorf("parsing usages for %q: %w", key, err)
		}
		for class, used := range byType {
			if class != "consumer_count" {
				out[class] += used
			}
		}
	}
	return out, nil
}

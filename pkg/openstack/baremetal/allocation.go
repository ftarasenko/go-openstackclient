package baremetal

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/baremetal/v1/allocations"
	"github.com/gophercloud/gophercloud/v2/pagination"

	"github.com/ftarasenko/koc/pkg/openstack/paging"
)

// Allocation is ironic's allocation plus owner (API 1.60), which gophercloud
// does not model.
type Allocation struct {
	allocations.Allocation
	Owner string `json:"owner"`
}

// AllocationListOpts adds the owner filter to gophercloud's ListOpts.
type AllocationListOpts struct {
	allocations.ListOpts
	Owner string
}

// ToAllocationListQuery implements allocations.ListOptsBuilder.
func (o AllocationListOpts) ToAllocationListQuery() (string, error) {
	q, err := o.ListOpts.ToAllocationListQuery()
	if err != nil || o.Owner == "" {
		return q, err
	}
	sep := "?"
	if strings.Contains(q, "?") {
		sep = "&"
	}
	return q + sep + "owner=" + url.QueryEscape(o.Owner), nil
}

// AllocationCreateOpts adds owner to gophercloud's CreateOpts.
type AllocationCreateOpts struct {
	allocations.CreateOpts
	Owner string `json:"owner,omitempty"`
}

// ToAllocationCreateMap implements allocations.CreateOptsBuilder.
func (o AllocationCreateOpts) ToAllocationCreateMap() (map[string]any, error) {
	b, err := o.CreateOpts.ToAllocationCreateMap()
	if err != nil || o.Owner == "" {
		return b, err
	}
	b["owner"] = o.Owner
	return b, nil
}

// extractAllocations decodes a page into Allocation so owner survives.
func extractAllocations(page pagination.Page) ([]Allocation, error) {
	var s struct {
		Allocations []Allocation `json:"allocations"`
	}
	ap, ok := page.(allocations.AllocationPage)
	if !ok {
		return nil, fmt.Errorf("extractAllocations: unexpected page type %T", page)
	}
	if err := ap.ExtractInto(&s); err != nil {
		return nil, err
	}
	return s.Allocations, nil
}

// ListAllocations lists allocations, at most limit of them (0: all).
func ListAllocations(ctx context.Context, c *gophercloud.ServiceClient, opts allocations.ListOptsBuilder,
	limit int,
) ([]Allocation, error) {
	all, err := paging.Collect(ctx, allocations.List(c, opts), limit, extractAllocations)
	return all, wrap(err, "listing baremetal allocations")
}

// GetAllocation reads one allocation by name or UUID.
func GetAllocation(ctx context.Context, c *gophercloud.ServiceClient, id string) (*Allocation, error) {
	var al Allocation
	if err := allocations.Get(ctx, c, id).ExtractInto(&al); err != nil {
		return nil, wrap(err, "showing baremetal allocation %s", id)
	}
	return &al, nil
}

// CreateAllocation asks ironic for a node. Creation is asynchronous: the
// allocation starts in "allocating"; WaitForAllocation waits it out.
func CreateAllocation(ctx context.Context, c *gophercloud.ServiceClient,
	opts allocations.CreateOptsBuilder,
) (*Allocation, error) {
	var al Allocation
	if err := allocations.Create(ctx, c, opts).ExtractInto(&al); err != nil {
		return nil, wrap(err, "creating baremetal allocation")
	}
	return &al, nil
}

// UpdateAllocation applies a JSON patch (API 1.57), which gophercloud has no
// call for.
func UpdateAllocation(ctx context.Context, c *gophercloud.ServiceClient, id string, ops Patch) (*Allocation, error) {
	var al Allocation
	if err := ironicPatch(ctx, c, joinPath("allocations", id), ops, &al); err != nil {
		return nil, wrap(err, "updating baremetal allocation %s", id)
	}
	return &al, nil
}

// DeleteAllocation deletes one allocation.
func DeleteAllocation(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(allocations.Delete(ctx, c, id).ExtractErr(), "deleting baremetal allocation %s", id)
}

// WaitForAllocation polls an allocation every interval until it is active,
// failing on "error" and when ctx is done (give it a deadline).
func WaitForAllocation(ctx context.Context, c *gophercloud.ServiceClient, id string,
	interval time.Duration,
) (*Allocation, error) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		var al Allocation
		if err := allocations.Get(ctx, c, id).ExtractInto(&al); err != nil {
			if ctx.Err() != nil {
				return nil, fmt.Errorf("waiting for allocation %s: %w", id, ctx.Err())
			}
			return nil, fmt.Errorf("polling allocation %s: %w", id, err)
		}
		switch al.State {
		case "active":
			return &al, nil
		case "error":
			return nil, fmt.Errorf("allocation %s failed: %s", id, al.LastError)
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("waiting for allocation %s (last state %q): %w", id, al.State, ctx.Err())
		case <-ticker.C:
		}
	}
}

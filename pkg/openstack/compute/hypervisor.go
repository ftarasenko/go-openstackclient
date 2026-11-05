package compute

import (
	"context"
	"fmt"
	"sync"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/hypervisors"
	"github.com/gophercloud/gophercloud/v2/openstack/placement/v1/resourceproviders"

	"github.com/ftarasenko/go-openstackclient/pkg/openstack/resolve"
)

// Nova 2.88 removed a hypervisor's usage fields (vcpus, memory_mb, local_gb,
// running_vms, cpu_info, host_ip and the rest); they live in placement now.
// At 2.1 nova still reports them, but names hypervisors and their services by
// integer IDs, where 2.53 and later use UUIDs. The functions below say which
// microversion they read at.

// ListHypervisors lists hypervisors at the client's microversion.
func ListHypervisors(ctx context.Context, c *gophercloud.ServiceClient, opts hypervisors.ListOptsBuilder) ([]hypervisors.Hypervisor, error) {
	pages, err := hypervisors.List(c, opts).AllPages(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing hypervisors: %w", err)
	}
	all, err := hypervisors.ExtractHypervisors(pages)
	if err != nil {
		return nil, fmt.Errorf("parsing hypervisor list: %w", err)
	}
	return all, nil
}

// ListHypervisorsWithUsage lists hypervisors at microversion 2.1, whatever
// the client's, so the usage fields are filled in. The IDs in the result are
// 2.1's integers.
func ListHypervisorsWithUsage(ctx context.Context, c *gophercloud.ServiceClient) ([]hypervisors.Hypervisor, error) {
	return ListHypervisors(ctx, at21(c), nil)
}

// at21 is a shallow copy of c without a microversion, which nova serves as
// 2.1.
func at21(c *gophercloud.ServiceClient) *gophercloud.ServiceClient {
	cp := *c
	cp.Microversion = ""
	return &cp
}

// FindHypervisor returns the hypervisor whose ID or hostname is ref, read at
// 2.1 so its usage fields are filled in (see [ListHypervisorsWithUsage]).
//
// When the client has a microversion and nothing at 2.1 matches, ref is also
// tried as an ID at the client's microversion (a UUID from 2.53), and that
// hypervisor's hostname and service host are matched in the 2.1 list.
//
// Hostnames are not unique across cells, so several matches are a
// *resolve.AmbiguousError rather than a pick; no match is a
// *resolve.NotFoundError.
func FindHypervisor(ctx context.Context, c *gophercloud.ServiceClient, ref string) (hypervisors.Hypervisor, error) {
	all, err := ListHypervisorsWithUsage(ctx, c)
	if err != nil {
		return hypervisors.Hypervisor{}, err
	}
	var matches []hypervisors.Hypervisor
	for _, h := range all {
		if h.ID == ref || h.HypervisorHostname == ref {
			matches = append(matches, h)
		}
	}
	if len(matches) == 0 && c.Microversion != "" {
		current, err := ListHypervisors(ctx, c, nil)
		if err != nil {
			return hypervisors.Hypervisor{}, err
		}
		for _, h := range current {
			if h.ID == ref {
				matches = sameHost(all, h)
				break
			}
		}
	}
	switch len(matches) {
	case 0:
		return hypervisors.Hypervisor{}, &resolve.NotFoundError{Kind: "hypervisor", Ref: ref}
	case 1:
		return matches[0], nil
	default:
		return hypervisors.Hypervisor{}, &resolve.AmbiguousError{Kind: "hypervisor", Ref: ref, Count: len(matches)}
	}
}

// sameHost returns the hypervisors in all with h's hostname and service host.
func sameHost(all []hypervisors.Hypervisor, h hypervisors.Hypervisor) []hypervisors.Hypervisor {
	var out []hypervisors.Hypervisor
	for _, cand := range all {
		if cand.HypervisorHostname == h.HypervisorHostname && cand.Service.Host == h.Service.Host {
			out = append(out, cand)
		}
	}
	return out
}

// HypervisorIDs returns the IDs of h, a hypervisor read at 2.1, and of its
// service as nova names them at the client's microversion (UUIDs from 2.53),
// matching on hostname and service host. When the client has no
// microversion, the listing fails or no single hypervisor matches, h's own
// IDs are returned with ok false.
func HypervisorIDs(ctx context.Context, c *gophercloud.ServiceClient, h hypervisors.Hypervisor) (id, serviceID string, ok bool) {
	if c.Microversion == "" {
		return h.ID, h.Service.ID, false
	}
	current, err := ListHypervisors(ctx, c, nil)
	if err != nil {
		return h.ID, h.Service.ID, false
	}
	match := sameHost(current, h)
	if len(match) != 1 {
		return h.ID, h.Service.ID, false
	}
	return match[0].ID, match[0].Service.ID, true
}

// ProviderUsage is a placement resource provider's inventories and usages:
// what nova's hypervisor usage fields became at 2.88.
type ProviderUsage struct {
	UUID        string
	Inventories map[string]resourceproviders.Inventory
	Usages      map[string]int
}

// PlacementUsage looks up, for each hostname, the placement resource provider
// of the same name and reads its inventories and usages, eight providers at a
// time. It takes the placement client (openstack.Session.Placement).
//
// Only the provider listing fails the call. A hostname with no provider, or
// whose inventory or usage read fails, is left out of the result, so a
// caller can fall back to what nova reported.
func PlacementUsage(ctx context.Context, placement *gophercloud.ServiceClient, hostnames []string) (map[string]ProviderUsage, error) {
	pages, err := resourceproviders.List(placement, resourceproviders.ListOpts{}).AllPages(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing resource providers: %w", err)
	}
	rps, err := resourceproviders.ExtractResourceProviders(pages)
	if err != nil {
		return nil, fmt.Errorf("parsing resource provider list: %w", err)
	}
	byName := make(map[string]string, len(rps))
	for _, rp := range rps {
		byName[rp.Name] = rp.UUID
	}

	out := make(map[string]ProviderUsage, len(hostnames))
	var mu sync.Mutex
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for _, name := range hostnames {
		uuid, ok := byName[name]
		if !ok {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			inv, err := resourceproviders.GetInventories(ctx, placement, uuid).Extract()
			if err != nil {
				return
			}
			use, err := resourceproviders.GetUsages(ctx, placement, uuid).Extract()
			if err != nil {
				return
			}
			mu.Lock()
			out[name] = ProviderUsage{UUID: uuid, Inventories: inv.Inventories, Usages: use.Usages}
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out, nil
}

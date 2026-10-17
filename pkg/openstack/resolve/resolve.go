// Package resolve turns OpenStack resource references — a name or an ID —
// into IDs, across services: a compute request that names an image, a
// network or a project resolves it through glance, neutron or keystone.
//
// Every resolver applies the same policy:
//
//   - an empty reference, or one that already looks like a UUID ([IsUUID]),
//     is returned as-is with no API call;
//   - otherwise the service is listed filtered by that name, and exactly one
//     match yields its ID;
//   - no match is a *[NotFoundError], which matches openstack.ErrNotFound
//     through errors.Is;
//   - several matches are an *[AmbiguousError]: the caller must use an ID.
//
// The package-level functions resolve without a cache. A [Resolver] carrying a
// [Cache] memoizes single matches, for callers that resolve the same names
// over and over.
package resolve

import (
	"context"
	"fmt"
	"regexp"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/volumes"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/domains"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/projects"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/users"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/images"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/networks"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/ports"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/subnets"

	"github.com/ftarasenko/go-openstackclient/pkg/openstack"
)

// uuidRe matches a UUID in either of the two forms OpenStack hands out
// (case-insensitive): the canonical dashed 8-4-4-4-12 form used by nova,
// neutron, glance and ironic, and the 32-character undashed hex form Keystone
// uses for project, user, domain and role IDs.
var uuidRe = regexp.MustCompile(`^(?:[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}|[0-9a-fA-F]{32})$`)

// IsUUID reports whether ref is a UUID in either form OpenStack uses: dashed
// (nova, neutron, glance, ironic) or 32 undashed hex digits (keystone).
func IsUUID(ref string) bool { return uuidRe.MatchString(ref) }

// NotFoundError is a name that matched nothing. It matches
// openstack.ErrNotFound through errors.Is.
type NotFoundError struct {
	Kind string // "image", "project", …
	Ref  string
}

func (e *NotFoundError) Error() string { return fmt.Sprintf("no %s found for %q", e.Kind, e.Ref) }

// Is makes errors.Is(err, openstack.ErrNotFound) true.
func (e *NotFoundError) Is(target error) bool { return target == openstack.ErrNotFound }

// AmbiguousError is a name that matched more than one resource.
type AmbiguousError struct {
	Kind  string
	Ref   string
	Count int
}

func (e *AmbiguousError) Error() string {
	return fmt.Sprintf("multiple %ss named %q; specify an ID instead", e.Kind, e.Ref)
}

// Resolver resolves names, memoizing single matches in Cache when it is set.
// The zero value resolves without a cache, exactly like the package-level
// functions.
type Resolver struct {
	Cache *Cache
}

// ImageID resolves a glance image name (or ID) to an image ID.
func (r Resolver) ImageID(ctx context.Context, imageClient *gophercloud.ServiceClient, ref string) (string, error) {
	return byName(ctx, r.Cache, imageClient, "image", "", ref, func(ctx context.Context) ([]images.Image, error) {
		pages, err := images.List(imageClient, images.ListOpts{Name: ref}).AllPages(ctx)
		if err != nil {
			return nil, err
		}
		return images.ExtractImages(pages)
	}, func(i images.Image) string { return i.ID })
}

// NetworkID resolves a neutron network name (or ID) to a network ID.
func (r Resolver) NetworkID(ctx context.Context, networkClient *gophercloud.ServiceClient, ref string) (string, error) {
	return byName(ctx, r.Cache, networkClient, "network", "", ref, func(ctx context.Context) ([]networks.Network, error) {
		pages, err := networks.List(networkClient, networks.ListOpts{Name: ref}).AllPages(ctx)
		if err != nil {
			return nil, err
		}
		return networks.ExtractNetworks(pages)
	}, func(n networks.Network) string { return n.ID })
}

// SubnetID resolves a neutron subnet name (or ID) to a subnet ID.
func (r Resolver) SubnetID(ctx context.Context, networkClient *gophercloud.ServiceClient, ref string) (string, error) {
	return byName(ctx, r.Cache, networkClient, "subnet", "", ref, func(ctx context.Context) ([]subnets.Subnet, error) {
		pages, err := subnets.List(networkClient, subnets.ListOpts{Name: ref}).AllPages(ctx)
		if err != nil {
			return nil, err
		}
		return subnets.ExtractSubnets(pages)
	}, func(s subnets.Subnet) string { return s.ID })
}

// PortID resolves a neutron port name (or ID) to a port ID.
func (r Resolver) PortID(ctx context.Context, networkClient *gophercloud.ServiceClient, ref string) (string, error) {
	return byName(ctx, r.Cache, networkClient, "port", "", ref, func(ctx context.Context) ([]ports.Port, error) {
		pages, err := ports.List(networkClient, ports.ListOpts{Name: ref}).AllPages(ctx)
		if err != nil {
			return nil, err
		}
		return ports.ExtractPorts(pages)
	}, func(p ports.Port) string { return p.ID })
}

// VolumeID resolves a cinder volume name (or ID) to a volume ID.
func (r Resolver) VolumeID(ctx context.Context, volumeClient *gophercloud.ServiceClient, ref string) (string, error) {
	return byName(ctx, r.Cache, volumeClient, "volume", "", ref, func(ctx context.Context) ([]volumes.Volume, error) {
		pages, err := volumes.List(volumeClient, volumes.ListOpts{Name: ref}).AllPages(ctx)
		if err != nil {
			return nil, err
		}
		return volumes.ExtractVolumes(pages)
	}, func(v volumes.Volume) string { return v.ID })
}

// ServerID resolves a nova server name (or ID) to a server ID.
//
// Nova's ?name= filter is a regular-expression substring match, so the
// listing is narrowed to exact name matches before the policy is applied.
// AllTenants lets an admin token resolve another project's server; nova
// ignores it for other tokens, so it widens no one's visibility.
func (r Resolver) ServerID(ctx context.Context, computeClient *gophercloud.ServiceClient, ref string) (string, error) {
	return byName(ctx, r.Cache, computeClient, "server", "", ref, func(ctx context.Context) ([]servers.Server, error) {
		// The non-detail listing carries the ID and name this needs; the detail
		// view would ship every attribute of every regex match.
		pages, err := servers.ListSimple(computeClient, servers.ListOpts{Name: ref, AllTenants: true}).AllPages(ctx)
		if err != nil {
			return nil, err
		}
		all, err := servers.ExtractServers(pages)
		if err != nil {
			return nil, err
		}
		exact := make([]servers.Server, 0, len(all))
		for _, s := range all {
			if s.Name == ref {
				exact = append(exact, s)
			}
		}
		return exact, nil
	}, func(s servers.Server) string { return s.ID })
}

// ProjectID resolves a keystone project name (or ID) to a project ID.
func (r Resolver) ProjectID(ctx context.Context, identityClient *gophercloud.ServiceClient, ref string) (string, error) {
	return r.ProjectIDInDomainID(ctx, identityClient, ref, "")
}

// ProjectIDInDomain is ProjectID narrowed to one domain, given by name or ID,
// for a project name that exists in several domains. An empty domainRef is
// ProjectID.
func (r Resolver) ProjectIDInDomain(ctx context.Context, identityClient *gophercloud.ServiceClient, ref, domainRef string) (string, error) {
	if domainRef == "" || ref == "" || IsUUID(ref) {
		return r.ProjectID(ctx, identityClient, ref)
	}
	domainID, err := r.DomainID(ctx, identityClient, domainRef)
	if err != nil {
		return "", err
	}
	return r.ProjectIDInDomainID(ctx, identityClient, ref, domainID)
}

// ProjectIDInDomainID is ProjectIDInDomain for a domain already known by ID,
// which then needs no lookup — and must not get a by-name one: the default
// domain's ID is "default", its name "Default". An empty domainID is
// ProjectID.
func (r Resolver) ProjectIDInDomainID(ctx context.Context, identityClient *gophercloud.ServiceClient, ref, domainID string) (string, error) {
	return byName(ctx, r.Cache, identityClient, "project", domainID, ref, func(ctx context.Context) ([]projects.Project, error) {
		pages, err := projects.List(identityClient, projects.ListOpts{Name: ref, DomainID: domainID}).AllPages(ctx)
		if err != nil {
			return nil, err
		}
		return projects.ExtractProjects(pages)
	}, func(p projects.Project) string { return p.ID })
}

// DomainID resolves a keystone domain name (or ID) to a domain ID.
func (r Resolver) DomainID(ctx context.Context, identityClient *gophercloud.ServiceClient, ref string) (string, error) {
	return byName(ctx, r.Cache, identityClient, "domain", "", ref, func(ctx context.Context) ([]domains.Domain, error) {
		pages, err := domains.List(identityClient, domains.ListOpts{Name: ref}).AllPages(ctx)
		if err != nil {
			return nil, err
		}
		return domains.ExtractDomains(pages)
	}, func(d domains.Domain) string { return d.ID })
}

// UserID resolves a keystone user name (or ID) to a user ID.
func (r Resolver) UserID(ctx context.Context, identityClient *gophercloud.ServiceClient, ref string) (string, error) {
	return r.UserIDInDomainID(ctx, identityClient, ref, "")
}

// UserIDInDomain is UserID narrowed to one domain, given by name or ID, for a
// user name that exists in several domains. An empty domainRef is UserID.
func (r Resolver) UserIDInDomain(ctx context.Context, identityClient *gophercloud.ServiceClient, ref, domainRef string) (string, error) {
	if domainRef == "" || ref == "" || IsUUID(ref) {
		return r.UserID(ctx, identityClient, ref)
	}
	domainID, err := r.DomainID(ctx, identityClient, domainRef)
	if err != nil {
		return "", err
	}
	return r.UserIDInDomainID(ctx, identityClient, ref, domainID)
}

// UserIDInDomainID is UserIDInDomain for a domain already known by ID. An
// empty domainID is UserID.
func (r Resolver) UserIDInDomainID(ctx context.Context, identityClient *gophercloud.ServiceClient, ref, domainID string) (string, error) {
	return byName(ctx, r.Cache, identityClient, "user", domainID, ref, func(ctx context.Context) ([]users.User, error) {
		pages, err := users.List(identityClient, users.ListOpts{Name: ref, DomainID: domainID}).AllPages(ctx)
		if err != nil {
			return nil, err
		}
		return users.ExtractUsers(pages)
	}, func(u users.User) string { return u.ID })
}

// byName is the shared engine: an empty ref or a UUID short-circuits without
// an API call; otherwise fetch runs a name-filtered list, and exactly one
// result is the answer (and is cached).
func byName[T any](ctx context.Context, cache *Cache, client *gophercloud.ServiceClient, kind, scope, ref string,
	fetch func(context.Context) ([]T, error), idOf func(T) string,
) (string, error) {
	if ref == "" || IsUUID(ref) {
		return ref, nil
	}
	var key cacheKey
	if cache != nil {
		key = newCacheKey(client, kind, scope, ref)
		if id, ok := cache.get(key); ok {
			return id, nil
		}
	}
	all, err := fetch(ctx)
	if err != nil {
		return "", fmt.Errorf("looking up %s %q: %w", kind, ref, err)
	}
	switch len(all) {
	case 0:
		return "", &NotFoundError{Kind: kind, Ref: ref}
	case 1:
		id := idOf(all[0])
		cache.put(key, id)
		return id, nil
	}
	return "", &AmbiguousError{Kind: kind, Ref: ref, Count: len(all)}
}

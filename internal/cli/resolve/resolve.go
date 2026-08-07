// Package resolve provides cross-service name→ID resolution helpers. A command
// authenticated against one service (e.g. compute) often accepts a reference
// that lives in another service (an image name, a network name, a project
// name); these helpers take the appropriate secondary service client and turn a
// name into an ID, passing UUIDs through untouched.
//
// Resolution policy (shared by all helpers): if the reference already looks
// like a UUID it is returned as-is without an API call; otherwise the service
// is listed filtered by name and exactly one match yields its ID, zero matches
// fall back to treating the reference as an opaque ID, and multiple matches are
// an error (the caller must disambiguate with an ID).
package resolve

import (
	"context"
	"fmt"
	"regexp"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/domains"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/projects"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/users"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/images"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/networks"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/ports"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/subnets"
)

// uuidRe matches a UUID in either of the two forms OpenStack hands out
// (case-insensitive): the canonical dashed 8-4-4-4-12 form used by nova,
// neutron, glance and ironic, and the 32-character UNDASHED hex form Keystone
// uses for project, user, domain and role IDs.
//
// Only the dashed form was accepted before, so "resolvers pass UUIDs through
// untouched" was violated for every Keystone reference: koc issued a doomed
// GET /v3/projects?name=<32-hex-id>, got {"projects": []}, and only then fell
// back to the literal ref. That cost a round trip on every project/user lookup
// and leaned on the zero-match fallback for correctness.
var uuidRe = regexp.MustCompile(`^(?:[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}|[0-9a-fA-F]{32})$`)

// IsUUID reports whether ref is a UUID in either accepted form.
func IsUUID(ref string) bool { return uuidRe.MatchString(ref) }

// ImageID resolves a glance image name (or ID) to an image ID using the given
// image service client.
func ImageID(ctx context.Context, imageClient *gophercloud.ServiceClient, ref string) (string, error) {
	return byName(ctx, "image", ref, func(ctx context.Context) ([]images.Image, error) {
		pages, err := images.List(imageClient, images.ListOpts{Name: ref}).AllPages(ctx)
		if err != nil {
			return nil, err
		}
		return images.ExtractImages(pages)
	}, func(i images.Image) string { return i.ID })
}

// NetworkID resolves a neutron network name (or ID) to a network ID using the
// given network service client.
func NetworkID(ctx context.Context, networkClient *gophercloud.ServiceClient, ref string) (string, error) {
	return byName(ctx, "network", ref, func(ctx context.Context) ([]networks.Network, error) {
		pages, err := networks.List(networkClient, networks.ListOpts{Name: ref}).AllPages(ctx)
		if err != nil {
			return nil, err
		}
		return networks.ExtractNetworks(pages)
	}, func(n networks.Network) string { return n.ID })
}

// ProjectID resolves a keystone project name (or ID) to a project ID using the
// given identity service client.
func ProjectID(ctx context.Context, identityClient *gophercloud.ServiceClient, ref string) (string, error) {
	return byName(ctx, "project", ref, func(ctx context.Context) ([]projects.Project, error) {
		pages, err := projects.List(identityClient, projects.ListOpts{Name: ref}).AllPages(ctx)
		if err != nil {
			return nil, err
		}
		return projects.ExtractProjects(pages)
	}, func(p projects.Project) string { return p.ID })
}

// ServerID resolves a nova server name (or ID) to a server ID using the given
// compute service client.
//
// Unlike glance/neutron/keystone, nova's ?name= filter is a regular-expression
// *substring* match, so the results are narrowed to an exact name match before
// the shared one-result policy is applied. AllTenants lets an admin token resolve
// a server owned by another project; nova ignores it for non-admin tokens, so it
// does not broaden a regular user's visibility.
func ServerID(ctx context.Context, computeClient *gophercloud.ServiceClient, ref string) (string, error) {
	if ref == "" || IsUUID(ref) {
		return ref, nil
	}
	pages, err := servers.List(computeClient, servers.ListOpts{Name: ref, AllTenants: true}).AllPages(ctx)
	if err != nil {
		return "", fmt.Errorf("looking up server %q: %w", ref, err)
	}
	all, err := servers.ExtractServers(pages)
	if err != nil {
		return "", fmt.Errorf("looking up server %q: %w", ref, err)
	}
	matches := make([]servers.Server, 0, len(all))
	for _, s := range all {
		if s.Name == ref {
			matches = append(matches, s)
		}
	}
	return pick("server", ref, len(matches), func(i int) string { return matches[i].ID })
}

// DomainID resolves a keystone domain name (or ID) to a domain ID using the
// given identity service client.
func DomainID(ctx context.Context, identityClient *gophercloud.ServiceClient, ref string) (string, error) {
	return byName(ctx, "domain", ref, func(ctx context.Context) ([]domains.Domain, error) {
		pages, err := domains.List(identityClient, domains.ListOpts{Name: ref}).AllPages(ctx)
		if err != nil {
			return nil, err
		}
		return domains.ExtractDomains(pages)
	}, func(d domains.Domain) string { return d.ID })
}

// ProjectIDInDomain is ProjectID narrowed to one domain (name or ID), backing
// the --project/--project-domain pair OSC uses when a project name exists in
// more than one domain. An empty domainRef behaves exactly like ProjectID.
func ProjectIDInDomain(ctx context.Context, identityClient *gophercloud.ServiceClient, ref, domainRef string) (string, error) {
	if domainRef == "" {
		return ProjectID(ctx, identityClient, ref)
	}
	if ref == "" || IsUUID(ref) {
		return ref, nil
	}
	domainID, err := DomainID(ctx, identityClient, domainRef)
	if err != nil {
		return "", err
	}
	return byName(ctx, "project", ref, func(ctx context.Context) ([]projects.Project, error) {
		pages, err := projects.List(identityClient, projects.ListOpts{Name: ref, DomainID: domainID}).AllPages(ctx)
		if err != nil {
			return nil, err
		}
		return projects.ExtractProjects(pages)
	}, func(p projects.Project) string { return p.ID })
}

// SubnetID resolves a neutron subnet name (or ID) to a subnet ID using the given
// network service client.
func SubnetID(ctx context.Context, networkClient *gophercloud.ServiceClient, ref string) (string, error) {
	return byName(ctx, "subnet", ref, func(ctx context.Context) ([]subnets.Subnet, error) {
		pages, err := subnets.List(networkClient, subnets.ListOpts{Name: ref}).AllPages(ctx)
		if err != nil {
			return nil, err
		}
		return subnets.ExtractSubnets(pages)
	}, func(s subnets.Subnet) string { return s.ID })
}

// PortID resolves a neutron port name (or ID) to a port ID using the given
// network service client.
func PortID(ctx context.Context, networkClient *gophercloud.ServiceClient, ref string) (string, error) {
	return byName(ctx, "port", ref, func(ctx context.Context) ([]ports.Port, error) {
		pages, err := ports.List(networkClient, ports.ListOpts{Name: ref}).AllPages(ctx)
		if err != nil {
			return nil, err
		}
		return ports.ExtractPorts(pages)
	}, func(p ports.Port) string { return p.ID })
}

// UserID resolves a keystone user name (or ID) to a user ID using the given
// identity service client.
func UserID(ctx context.Context, identityClient *gophercloud.ServiceClient, ref string) (string, error) {
	return byName(ctx, "user", ref, func(ctx context.Context) ([]users.User, error) {
		pages, err := users.List(identityClient, users.ListOpts{Name: ref}).AllPages(ctx)
		if err != nil {
			return nil, err
		}
		return users.ExtractUsers(pages)
	}, func(u users.User) string { return u.ID })
}

// UserIDInDomain is UserID narrowed to one domain (name or ID), backing the
// --user/--user-domain pair OSC uses when a user name exists in more than one
// domain. An empty domainRef behaves exactly like UserID.
func UserIDInDomain(ctx context.Context, identityClient *gophercloud.ServiceClient, ref, domainRef string) (string, error) {
	if domainRef == "" {
		return UserID(ctx, identityClient, ref)
	}
	if ref == "" || IsUUID(ref) {
		return ref, nil
	}
	domainID, err := DomainID(ctx, identityClient, domainRef)
	if err != nil {
		return "", err
	}
	return byName(ctx, "user", ref, func(ctx context.Context) ([]users.User, error) {
		pages, err := users.List(identityClient, users.ListOpts{Name: ref, DomainID: domainID}).AllPages(ctx)
		if err != nil {
			return nil, err
		}
		return users.ExtractUsers(pages)
	}, func(u users.User) string { return u.ID })
}

// byName is the shared engine for the cross-service resolvers: an empty ref or a
// UUID short-circuits without an API call; otherwise fetch runs a name-filtered
// list and pick applies the match policy.
func byName[T any](ctx context.Context, kind, ref string,
	fetch func(context.Context) ([]T, error), idOf func(T) string,
) (string, error) {
	if ref == "" || IsUUID(ref) {
		return ref, nil
	}
	all, err := fetch(ctx)
	if err != nil {
		return "", fmt.Errorf("looking up %s %q: %w", kind, ref, err)
	}
	return pick(kind, ref, len(all), func(i int) string { return idOf(all[i]) })
}

// pick applies the shared match policy: one → its ID, zero → ref passthrough,
// many → error.
func pick(kind, ref string, n int, idAt func(int) string) (string, error) {
	switch n {
	case 0:
		return ref, nil
	case 1:
		return idAt(0), nil
	default:
		return "", fmt.Errorf("multiple %ss named %q; specify an ID instead", kind, ref)
	}
}

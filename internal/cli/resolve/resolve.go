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
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/projects"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/images"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/networks"
)

// uuidRe matches a canonical 8-4-4-4-12 UUID (case-insensitive).
var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// IsUUID reports whether ref is a canonical UUID.
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

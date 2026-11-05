package identity

import (
	"context"
	"fmt"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/domains"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/groups"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/projects"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/roles"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/services"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/users"

	identityapi "github.com/ftarasenko/go-openstackclient/pkg/openstack/identity"
)

// The keystone API keys grants, assignments and endpoint creation on IDs, but
// operators supply names. Each resolver runs a name-filtered list first; if it
// finds exactly one match it returns that ID, otherwise it assumes the caller
// already passed an ID and returns the value unchanged. This lets every command
// accept either a name or an ID for domain/project/user/role/group/service.

func resolveDomainID(ctx context.Context, client *gophercloud.ServiceClient, nameOrID string) (string, error) {
	return resolveByName("domain", "", nameOrID, func() ([]domains.Domain, error) {
		return identityapi.ListDomains(ctx, client, domains.ListOpts{Name: nameOrID})
	}, func(d domains.Domain) string { return d.ID })
}

func resolveProjectID(ctx context.Context, client *gophercloud.ServiceClient, nameOrID, domainID string) (string, error) {
	return resolveByName("project", " (try --domain)", nameOrID, func() ([]projects.Project, error) {
		return identityapi.ListProjects(ctx, client, projects.ListOpts{Name: nameOrID, DomainID: domainID})
	}, func(p projects.Project) string { return p.ID })
}

func resolveUserID(ctx context.Context, client *gophercloud.ServiceClient, nameOrID, domainID string) (string, error) {
	return resolveByName("user", " (try --domain)", nameOrID, func() ([]users.User, error) {
		return identityapi.ListUsers(ctx, client, users.ListOpts{Name: nameOrID, DomainID: domainID})
	}, func(u users.User) string { return u.ID })
}

func resolveRoleID(ctx context.Context, client *gophercloud.ServiceClient, nameOrID, domainID string) (string, error) {
	return resolveByName("role", "", nameOrID, func() ([]roles.Role, error) {
		return identityapi.ListRoles(ctx, client, roles.ListOpts{Name: nameOrID, DomainID: domainID})
	}, func(r roles.Role) string { return r.ID })
}

func resolveGroupID(ctx context.Context, client *gophercloud.ServiceClient, nameOrID, domainID string) (string, error) {
	return resolveByName("group", "", nameOrID, func() ([]groups.Group, error) {
		return identityapi.ListGroups(ctx, client, groups.ListOpts{Name: nameOrID, DomainID: domainID})
	}, func(g groups.Group) string { return g.ID })
}

// resolveServiceID does not use the shared resolveByName generic: the keystone
// /v3/services endpoint filters only by `type`, not by name — passing ?name= is
// ignored and the server returns the full catalog, which would make every name
// look ambiguous. So it lists everything and matches the name client-side.
func resolveServiceID(ctx context.Context, client *gophercloud.ServiceClient, nameOrID string) (string, error) {
	if nameOrID == "" {
		return "", nil
	}
	all, err := identityapi.ListServices(ctx, client, services.ListOpts{})
	if err != nil {
		return "", relabel(err, "resolving service %q", nameOrID)
	}
	var matches []string
	for _, s := range all {
		if s.Name == nameOrID {
			matches = append(matches, s.ID)
		}
	}
	switch len(matches) {
	case 0:
		return nameOrID, nil
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("service name %q is ambiguous: %d matches", nameOrID, len(matches))
	}
}

// resolveByName backs every keystone name→ID resolver: an empty ref yields "",
// a name-filtered list of one match wins, zero matches falls back to treating
// the ref as an ID, and multiple matches are ambiguous. hint is appended to the
// ambiguity error (e.g. " (try --domain)" for domain-scoped nouns).
func resolveByName[T any](kind, hint, nameOrID string,
	list func() ([]T, error), idOf func(T) string,
) (string, error) {
	if nameOrID == "" {
		return "", nil
	}
	all, err := list()
	if err != nil {
		return "", relabel(err, "resolving %s %q", kind, nameOrID)
	}
	switch len(all) {
	case 0:
		return nameOrID, nil
	case 1:
		return idOf(all[0]), nil
	default:
		return "", fmt.Errorf("%s name %q is ambiguous: %d matches%s", kind, nameOrID, len(all), hint)
	}
}

// enabledFromFlags derives the tri-state Enabled pointer from the mutually
// exclusive --enable/--disable pair. It returns nil when neither was set so the
// server default (or an unchanged value on update) is preserved.
// checkEnableDisable rejects an invocation that set both --enable and --disable,
// which are mutually exclusive. Callers pass the tri-state "Changed" bits they
// already read for enabledFromFlags.
func checkEnableDisable(enableSet, disableSet bool) error {
	if enableSet && disableSet {
		return fmt.Errorf("--enable and --disable are mutually exclusive")
	}
	return nil
}

func enabledFromFlags(enableSet, disableSet, enable bool) *bool {
	if enableSet {
		v := enable
		return &v
	}
	if disableSet {
		v := false
		return &v
	}
	return nil
}

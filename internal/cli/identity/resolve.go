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
)

// The keystone API keys grants, assignments and endpoint creation on IDs, but
// operators supply names. Each resolver runs a name-filtered list first; if it
// finds exactly one match it returns that ID, otherwise it assumes the caller
// already passed an ID and returns the value unchanged. This lets every command
// accept either a name or an ID for domain/project/user/role/group/service.

func resolveDomainID(ctx context.Context, client *gophercloud.ServiceClient, nameOrID string) (string, error) {
	return resolveByName("domain", "", nameOrID, func() ([]domains.Domain, error) {
		pages, err := domains.List(client, domains.ListOpts{Name: nameOrID}).AllPages(ctx)
		if err != nil {
			return nil, err
		}
		return domains.ExtractDomains(pages)
	}, func(d domains.Domain) string { return d.ID })
}

func resolveProjectID(ctx context.Context, client *gophercloud.ServiceClient, nameOrID, domainID string) (string, error) {
	return resolveByName("project", " (try --domain)", nameOrID, func() ([]projects.Project, error) {
		pages, err := projects.List(client, projects.ListOpts{Name: nameOrID, DomainID: domainID}).AllPages(ctx)
		if err != nil {
			return nil, err
		}
		return projects.ExtractProjects(pages)
	}, func(p projects.Project) string { return p.ID })
}

func resolveUserID(ctx context.Context, client *gophercloud.ServiceClient, nameOrID, domainID string) (string, error) {
	return resolveByName("user", " (try --domain)", nameOrID, func() ([]users.User, error) {
		pages, err := users.List(client, users.ListOpts{Name: nameOrID, DomainID: domainID}).AllPages(ctx)
		if err != nil {
			return nil, err
		}
		return users.ExtractUsers(pages)
	}, func(u users.User) string { return u.ID })
}

func resolveRoleID(ctx context.Context, client *gophercloud.ServiceClient, nameOrID, domainID string) (string, error) {
	return resolveByName("role", "", nameOrID, func() ([]roles.Role, error) {
		pages, err := roles.List(client, roles.ListOpts{Name: nameOrID, DomainID: domainID}).AllPages(ctx)
		if err != nil {
			return nil, err
		}
		return roles.ExtractRoles(pages)
	}, func(r roles.Role) string { return r.ID })
}

func resolveGroupID(ctx context.Context, client *gophercloud.ServiceClient, nameOrID, domainID string) (string, error) {
	return resolveByName("group", "", nameOrID, func() ([]groups.Group, error) {
		pages, err := groups.List(client, groups.ListOpts{Name: nameOrID, DomainID: domainID}).AllPages(ctx)
		if err != nil {
			return nil, err
		}
		return groups.ExtractGroups(pages)
	}, func(g groups.Group) string { return g.ID })
}

func resolveServiceID(ctx context.Context, client *gophercloud.ServiceClient, nameOrID string) (string, error) {
	return resolveByName("service", "", nameOrID, func() ([]services.Service, error) {
		pages, err := services.List(client, services.ListOpts{Name: nameOrID}).AllPages(ctx)
		if err != nil {
			return nil, err
		}
		return services.ExtractServices(pages)
	}, func(s services.Service) string { return s.ID })
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
		return "", fmt.Errorf("resolving %s %q: %w", kind, nameOrID, err)
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

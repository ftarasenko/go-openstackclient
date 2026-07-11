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
	if nameOrID == "" {
		return "", nil
	}
	pages, err := domains.List(client, domains.ListOpts{Name: nameOrID}).AllPages(ctx)
	if err != nil {
		return "", fmt.Errorf("resolving domain %q: %w", nameOrID, err)
	}
	all, err := domains.ExtractDomains(pages)
	if err != nil {
		return "", fmt.Errorf("parsing domain list: %w", err)
	}
	switch len(all) {
	case 0:
		return nameOrID, nil
	case 1:
		return all[0].ID, nil
	default:
		return "", fmt.Errorf("domain name %q is ambiguous: %d matches", nameOrID, len(all))
	}
}

func resolveProjectID(ctx context.Context, client *gophercloud.ServiceClient, nameOrID, domainID string) (string, error) {
	if nameOrID == "" {
		return "", nil
	}
	pages, err := projects.List(client, projects.ListOpts{Name: nameOrID, DomainID: domainID}).AllPages(ctx)
	if err != nil {
		return "", fmt.Errorf("resolving project %q: %w", nameOrID, err)
	}
	all, err := projects.ExtractProjects(pages)
	if err != nil {
		return "", fmt.Errorf("parsing project list: %w", err)
	}
	switch len(all) {
	case 0:
		return nameOrID, nil
	case 1:
		return all[0].ID, nil
	default:
		return "", fmt.Errorf("project name %q is ambiguous: %d matches (try --domain)", nameOrID, len(all))
	}
}

func resolveUserID(ctx context.Context, client *gophercloud.ServiceClient, nameOrID, domainID string) (string, error) {
	if nameOrID == "" {
		return "", nil
	}
	pages, err := users.List(client, users.ListOpts{Name: nameOrID, DomainID: domainID}).AllPages(ctx)
	if err != nil {
		return "", fmt.Errorf("resolving user %q: %w", nameOrID, err)
	}
	all, err := users.ExtractUsers(pages)
	if err != nil {
		return "", fmt.Errorf("parsing user list: %w", err)
	}
	switch len(all) {
	case 0:
		return nameOrID, nil
	case 1:
		return all[0].ID, nil
	default:
		return "", fmt.Errorf("user name %q is ambiguous: %d matches (try --domain)", nameOrID, len(all))
	}
}

func resolveRoleID(ctx context.Context, client *gophercloud.ServiceClient, nameOrID, domainID string) (string, error) {
	if nameOrID == "" {
		return "", nil
	}
	pages, err := roles.List(client, roles.ListOpts{Name: nameOrID, DomainID: domainID}).AllPages(ctx)
	if err != nil {
		return "", fmt.Errorf("resolving role %q: %w", nameOrID, err)
	}
	all, err := roles.ExtractRoles(pages)
	if err != nil {
		return "", fmt.Errorf("parsing role list: %w", err)
	}
	switch len(all) {
	case 0:
		return nameOrID, nil
	case 1:
		return all[0].ID, nil
	default:
		return "", fmt.Errorf("role name %q is ambiguous: %d matches", nameOrID, len(all))
	}
}

func resolveGroupID(ctx context.Context, client *gophercloud.ServiceClient, nameOrID, domainID string) (string, error) {
	if nameOrID == "" {
		return "", nil
	}
	pages, err := groups.List(client, groups.ListOpts{Name: nameOrID, DomainID: domainID}).AllPages(ctx)
	if err != nil {
		return "", fmt.Errorf("resolving group %q: %w", nameOrID, err)
	}
	all, err := groups.ExtractGroups(pages)
	if err != nil {
		return "", fmt.Errorf("parsing group list: %w", err)
	}
	switch len(all) {
	case 0:
		return nameOrID, nil
	case 1:
		return all[0].ID, nil
	default:
		return "", fmt.Errorf("group name %q is ambiguous: %d matches", nameOrID, len(all))
	}
}

func resolveServiceID(ctx context.Context, client *gophercloud.ServiceClient, nameOrID string) (string, error) {
	if nameOrID == "" {
		return "", nil
	}
	pages, err := services.List(client, services.ListOpts{Name: nameOrID}).AllPages(ctx)
	if err != nil {
		return "", fmt.Errorf("resolving service %q: %w", nameOrID, err)
	}
	all, err := services.ExtractServices(pages)
	if err != nil {
		return "", fmt.Errorf("parsing service list: %w", err)
	}
	switch len(all) {
	case 0:
		return nameOrID, nil
	case 1:
		return all[0].ID, nil
	default:
		return "", fmt.Errorf("service name %q is ambiguous: %d matches", nameOrID, len(all))
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

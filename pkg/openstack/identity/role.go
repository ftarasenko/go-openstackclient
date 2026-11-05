package identity

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/roles"
)

// ListRoles lists the roles opts selects; opts.DomainID lists a domain's own
// roles instead of the global ones.
func ListRoles(ctx context.Context, c *gophercloud.ServiceClient, opts roles.ListOpts) ([]roles.Role, error) {
	return listAll(ctx, roles.List(c, opts), roles.ExtractRoles, "listing roles")
}

// GetRole returns the role with the given ID.
func GetRole(ctx context.Context, c *gophercloud.ServiceClient, id string) (*roles.Role, error) {
	r, err := roles.Get(ctx, c, id).Extract()
	return r, wrap(err, "showing role %s", id)
}

// CreateRole creates a role. Keystone answers a duplicate name with 409, which
// gophercloud.ResponseCodeIs detects through the wrapped error.
func CreateRole(ctx context.Context, c *gophercloud.ServiceClient, opts roles.CreateOpts) (*roles.Role, error) {
	r, err := roles.Create(ctx, c, opts).Extract()
	return r, wrap(err, "creating role %q", opts.Name)
}

// UpdateRole applies opts to the role with the given ID.
func UpdateRole(ctx context.Context, c *gophercloud.ServiceClient, id string, opts roles.UpdateOpts) (*roles.Role, error) {
	r, err := roles.Update(ctx, c, id, opts).Extract()
	return r, wrap(err, "updating role %s", id)
}

// DeleteRole deletes the role with the given ID.
func DeleteRole(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(roles.Delete(ctx, c, id).ExtractErr(), "deleting role %s", id)
}

// AssignRole grants a role to the user or group in opts on the project, domain
// or system it names.
func AssignRole(ctx context.Context, c *gophercloud.ServiceClient, roleID string, opts roles.AssignOpts) error {
	return wrap(roles.Assign(ctx, c, roleID, opts).ExtractErr(), "granting role %s", roleID)
}

// UnassignRole revokes a grant AssignRole made.
func UnassignRole(ctx context.Context, c *gophercloud.ServiceClient, roleID string, opts roles.UnassignOpts) error {
	return wrap(roles.Unassign(ctx, c, roleID, opts).ExtractErr(), "revoking role %s", roleID)
}

// CreateRoleInferenceRule makes the prior role imply the implied one.
func CreateRoleInferenceRule(ctx context.Context, c *gophercloud.ServiceClient, priorID, impliedID string) (*roles.RoleInferenceRule, error) {
	r, err := roles.CreateRoleInferenceRule(ctx, c, priorID, impliedID).Extract()
	return r, wrap(err, "creating role-inference rule %s implies %s", priorID, impliedID)
}

// DeleteRoleInferenceRule removes the rule that the prior role implies the
// implied one.
func DeleteRoleInferenceRule(ctx context.Context, c *gophercloud.ServiceClient, priorID, impliedID string) error {
	return wrap(roles.DeleteRoleInferenceRule(ctx, c, priorID, impliedID).ExtractErr(),
		"deleting role-inference rule %s implies %s", priorID, impliedID)
}

// ListRoleInferenceRules lists every role-inference rule, one entry per prior
// role with every role it implies.
func ListRoleInferenceRules(ctx context.Context, c *gophercloud.ServiceClient) ([]roles.RoleInferenceRules, error) {
	l, err := roles.ListRoleInferenceRules(ctx, c).Extract()
	if err != nil {
		return nil, wrap(err, "listing role-inference rules")
	}
	return l.RoleInferenceRuleList, nil
}

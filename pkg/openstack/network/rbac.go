package network

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/rbacpolicies"
)

// RBACPolicyEmptyUpdate is an RBAC policy update with no typed field, for
// WithRBACPolicyUpdateAttrs to fill: rbacpolicies.UpdateOpts always sends
// target_tenant.
type RBACPolicyEmptyUpdate struct{}

// ToRBACPolicyUpdateMap implements rbacpolicies.UpdateOptsBuilder.
func (RBACPolicyEmptyUpdate) ToRBACPolicyUpdateMap() (map[string]any, error) {
	return map[string]any{"rbac_policy": map[string]any{}}, nil
}

// ListRBACPolicies lists every RBAC policy matching opts.
func ListRBACPolicies(ctx context.Context, c *gophercloud.ServiceClient, opts rbacpolicies.ListOptsBuilder) ([]rbacpolicies.RBACPolicy, error) {
	pages, err := rbacpolicies.List(c, opts).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing network RBAC policies")
	}
	all, err := rbacpolicies.ExtractRBACPolicies(pages)
	return all, wrap(err, "parsing the network RBAC policy list")
}

// GetRBACPolicy reads one RBAC policy.
func GetRBACPolicy(ctx context.Context, c *gophercloud.ServiceClient, id string) (*rbacpolicies.RBACPolicy, error) {
	p, err := rbacpolicies.Get(ctx, c, id).Extract()
	return p, wrap(err, "showing network RBAC policy %s", id)
}

// CreateRBACPolicy creates an RBAC policy. Wrap opts with
// WithRBACPolicyCreateAttrs to set its owner (project_id).
func CreateRBACPolicy(ctx context.Context, c *gophercloud.ServiceClient, opts rbacpolicies.CreateOptsBuilder) (*rbacpolicies.RBACPolicy, error) {
	p, err := rbacpolicies.Create(ctx, c, opts).Extract()
	return p, wrap(err, "creating network RBAC policy")
}

// UpdateRBACPolicy updates an RBAC policy.
func UpdateRBACPolicy(ctx context.Context, c *gophercloud.ServiceClient, id string, opts rbacpolicies.UpdateOptsBuilder) (*rbacpolicies.RBACPolicy, error) {
	p, err := rbacpolicies.Update(ctx, c, id, opts).Extract()
	return p, wrap(err, "updating network RBAC policy %s", id)
}

// DeleteRBACPolicy deletes an RBAC policy.
func DeleteRBACPolicy(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(rbacpolicies.Delete(ctx, c, id).ExtractErr(), "deleting network RBAC policy %s", id)
}

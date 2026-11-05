package loadbalancer

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/loadbalancer/v2/l7policies"
)

// FindL7PolicyID resolves an L7 policy name or ID to an ID. Rules have no
// name; they are addressed by ID within their policy.
func FindL7PolicyID(ctx context.Context, c *gophercloud.ServiceClient, ref string) (string, error) {
	return findID(ctx, "L7 policy", ref, func(ctx context.Context) ([]l7policies.L7Policy, error) {
		pages, err := l7policies.List(c, l7policies.ListOpts{Name: ref}).AllPages(ctx)
		return extracted(pages, err, l7policies.ExtractL7Policies)
	}, func(p l7policies.L7Policy) string { return p.ID })
}

// ListL7Policies lists every L7 policy matching opts.
func ListL7Policies(ctx context.Context, c *gophercloud.ServiceClient, opts l7policies.ListOptsBuilder) ([]l7policies.L7Policy, error) {
	pages, err := l7policies.List(c, opts).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing l7 policies")
	}
	all, err := l7policies.ExtractL7Policies(pages)
	return all, wrap(err, "parsing l7 policy list")
}

// GetL7Policy reads one L7 policy.
func GetL7Policy(ctx context.Context, c *gophercloud.ServiceClient, id string) (*l7policies.L7Policy, error) {
	p, err := l7policies.Get(ctx, c, id).Extract()
	return p, wrap(err, "getting l7 policy %s", id)
}

// CreateL7Policy creates an L7 policy on a listener.
func CreateL7Policy(ctx context.Context, c *gophercloud.ServiceClient, opts l7policies.CreateOptsBuilder) (*l7policies.L7Policy, error) {
	p, err := l7policies.Create(ctx, c, opts).Extract()
	return p, wrap(err, "creating l7 policy")
}

// UpdateL7Policy updates an L7 policy.
func UpdateL7Policy(ctx context.Context, c *gophercloud.ServiceClient, id string, opts l7policies.UpdateOptsBuilder) (*l7policies.L7Policy, error) {
	p, err := l7policies.Update(ctx, c, id, opts).Extract()
	return p, wrap(err, "updating l7 policy %s", id)
}

// DeleteL7Policy deletes an L7 policy.
func DeleteL7Policy(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(l7policies.Delete(ctx, c, id).ExtractErr(), "deleting l7 policy %s", id)
}

// ListL7Rules lists an L7 policy's rules matching opts.
func ListL7Rules(ctx context.Context, c *gophercloud.ServiceClient, policyID string, opts l7policies.ListRulesOptsBuilder) ([]l7policies.Rule, error) {
	pages, err := l7policies.ListRules(c, policyID, opts).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing rules of l7 policy %s", policyID)
	}
	all, err := l7policies.ExtractRules(pages)
	return all, wrap(err, "parsing l7 rule list")
}

// GetL7Rule reads one rule of an L7 policy.
func GetL7Rule(ctx context.Context, c *gophercloud.ServiceClient, policyID, id string) (*l7policies.Rule, error) {
	r, err := l7policies.GetRule(ctx, c, policyID, id).Extract()
	return r, wrap(err, "getting rule %s of l7 policy %s", id, policyID)
}

// CreateL7Rule adds a rule to an L7 policy.
func CreateL7Rule(ctx context.Context, c *gophercloud.ServiceClient, policyID string, opts l7policies.CreateRuleOptsBuilder) (*l7policies.Rule, error) {
	r, err := l7policies.CreateRule(ctx, c, policyID, opts).Extract()
	return r, wrap(err, "adding rule to l7 policy %s", policyID)
}

// UpdateL7Rule updates a rule of an L7 policy.
func UpdateL7Rule(ctx context.Context, c *gophercloud.ServiceClient, policyID, id string, opts l7policies.UpdateRuleOptsBuilder) (*l7policies.Rule, error) {
	r, err := l7policies.UpdateRule(ctx, c, policyID, id, opts).Extract()
	return r, wrap(err, "updating rule %s of l7 policy %s", id, policyID)
}

// DeleteL7Rule deletes a rule of an L7 policy.
func DeleteL7Rule(ctx context.Context, c *gophercloud.ServiceClient, policyID, id string) error {
	return wrap(l7policies.DeleteRule(ctx, c, policyID, id).ExtractErr(), "deleting rule %s of l7 policy %s", id, policyID)
}

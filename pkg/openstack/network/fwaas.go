package network

import (
	"context"
	"errors"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/fwaas_v2/groups"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/fwaas_v2/policies"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/fwaas_v2/rules"
)

// Firewall groups, policies and rules: the fwaas_v2 extension. Its typed
// update opts omit empty values, so clearing an attribute (a null policy, an
// empty port list) takes a builder of the caller's own.

// FindFirewallGroupID resolves a firewall group name or ID to an ID.
func FindFirewallGroupID(ctx context.Context, c *gophercloud.ServiceClient, ref string) (string, error) {
	return findID(ctx, "firewall group", ref, func(ctx context.Context) ([]groups.Group, error) {
		pages, err := groups.List(c, groups.ListOpts{Name: ref}).AllPages(ctx)
		return extracted(pages, err, groups.ExtractGroups)
	}, func(g groups.Group) string { return g.ID })
}

// FindFirewallPolicyID resolves a firewall policy name or ID to an ID.
func FindFirewallPolicyID(ctx context.Context, c *gophercloud.ServiceClient, ref string) (string, error) {
	return findID(ctx, "firewall policy", ref, func(ctx context.Context) ([]policies.Policy, error) {
		pages, err := policies.List(c, policies.ListOpts{Name: ref}).AllPages(ctx)
		return extracted(pages, err, policies.ExtractPolicies)
	}, func(p policies.Policy) string { return p.ID })
}

// FindFirewallRuleID resolves a firewall rule name or ID to an ID.
func FindFirewallRuleID(ctx context.Context, c *gophercloud.ServiceClient, ref string) (string, error) {
	return findID(ctx, "firewall rule", ref, func(ctx context.Context) ([]rules.Rule, error) {
		pages, err := rules.List(c, rules.ListOpts{Name: ref}).AllPages(ctx)
		return extracted(pages, err, rules.ExtractRules)
	}, func(r rules.Rule) string { return r.ID })
}

// ListFirewallGroups lists every firewall group matching opts.
func ListFirewallGroups(ctx context.Context, c *gophercloud.ServiceClient, opts groups.ListOptsBuilder) ([]groups.Group, error) {
	pages, err := groups.List(c, opts).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing firewall groups")
	}
	all, err := groups.ExtractGroups(pages)
	return all, wrap(err, "parsing firewall group list")
}

// GetFirewallGroup reads one firewall group.
func GetFirewallGroup(ctx context.Context, c *gophercloud.ServiceClient, id string) (*groups.Group, error) {
	g, err := groups.Get(ctx, c, id).Extract()
	return g, wrap(err, "getting firewall group %s", id)
}

// CreateFirewallGroup creates a firewall group.
func CreateFirewallGroup(ctx context.Context, c *gophercloud.ServiceClient, opts groups.CreateOptsBuilder) (*groups.Group, error) {
	g, err := groups.Create(ctx, c, opts).Extract()
	return g, wrap(err, "creating firewall group")
}

// UpdateFirewallGroup updates a firewall group. Its ports replace the
// group's, so to add one, send the current ports with it.
func UpdateFirewallGroup(ctx context.Context, c *gophercloud.ServiceClient, id string, opts groups.UpdateOptsBuilder) (*groups.Group, error) {
	g, err := groups.Update(ctx, c, id, opts).Extract()
	return g, wrap(err, "updating firewall group %s", id)
}

// DeleteFirewallGroup deletes a firewall group.
func DeleteFirewallGroup(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(groups.Delete(ctx, c, id).ExtractErr(), "deleting firewall group %s", id)
}

// ListFirewallPolicies lists every firewall policy matching opts.
func ListFirewallPolicies(ctx context.Context, c *gophercloud.ServiceClient, opts policies.ListOptsBuilder) ([]policies.Policy, error) {
	pages, err := policies.List(c, opts).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing firewall policies")
	}
	all, err := policies.ExtractPolicies(pages)
	return all, wrap(err, "parsing firewall policy list")
}

// GetFirewallPolicy reads one firewall policy.
func GetFirewallPolicy(ctx context.Context, c *gophercloud.ServiceClient, id string) (*policies.Policy, error) {
	p, err := policies.Get(ctx, c, id).Extract()
	return p, wrap(err, "getting firewall policy %s", id)
}

// CreateFirewallPolicy creates a firewall policy.
func CreateFirewallPolicy(ctx context.Context, c *gophercloud.ServiceClient, opts policies.CreateOptsBuilder) (*policies.Policy, error) {
	p, err := policies.Create(ctx, c, opts).Extract()
	return p, wrap(err, "creating firewall policy")
}

// UpdateFirewallPolicy updates a firewall policy. Its rules replace the
// policy's; InsertFirewallPolicyRule and RemoveFirewallPolicyRule edit one.
func UpdateFirewallPolicy(ctx context.Context, c *gophercloud.ServiceClient, id string, opts policies.UpdateOptsBuilder) (*policies.Policy, error) {
	p, err := policies.Update(ctx, c, id, opts).Extract()
	return p, wrap(err, "updating firewall policy %s", id)
}

// DeleteFirewallPolicy deletes a firewall policy.
func DeleteFirewallPolicy(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(policies.Delete(ctx, c, id).ExtractErr(), "deleting firewall policy %s", id)
}

// InsertFirewallPolicyRule inserts a rule into a policy, before or after
// another when opts says so. The updated policy is not decoded;
// GetFirewallPolicy reads it.
func InsertFirewallPolicyRule(ctx context.Context, c *gophercloud.ServiceClient, policyID string, opts policies.InsertRuleOptsBuilder) error {
	return wrap(policies.InsertRule(ctx, c, policyID, opts).Err, "inserting firewall rule into firewall policy %s", policyID)
}

// RemoveFirewallPolicyRule removes a rule from a policy. The updated policy
// is not decoded; GetFirewallPolicy reads it.
func RemoveFirewallPolicyRule(ctx context.Context, c *gophercloud.ServiceClient, policyID, ruleID string) error {
	return wrap(policies.RemoveRule(ctx, c, policyID, ruleID).Err, "removing firewall rule %s from firewall policy %s", ruleID, policyID)
}

// FirewallRule is a firewall rule plus the remote-group attributes
// gophercloud's rules.Rule does not model. They arrive through an exported
// embed because gophercloud's slice extraction decodes each embedded struct
// separately and drops plain fields.
type FirewallRule struct {
	rules.Rule
	FirewallRuleGroupsExt
}

// FirewallRuleGroupsExt carries fwaas_v2's source and destination firewall
// group IDs.
type FirewallRuleGroupsExt struct {
	SourceFirewallGroupID      string `json:"source_firewall_group_id"`
	DestinationFirewallGroupID string `json:"destination_firewall_group_id"`
}

// firewallRule decodes a rule response, wrapping a request error with what
// and a body that does not decode with "parsing firewall rule".
func firewallRule(r gophercloud.Result, what string, args ...any) (*FirewallRule, error) {
	if r.Err != nil {
		return nil, wrap(r.Err, what, args...)
	}
	var rule FirewallRule
	if err := r.ExtractIntoStructPtr(&rule, "firewall_rule"); err != nil {
		return nil, wrap(err, "parsing firewall rule")
	}
	return &rule, nil
}

// ListFirewallRules lists every firewall rule matching opts.
func ListFirewallRules(ctx context.Context, c *gophercloud.ServiceClient, opts rules.ListOptsBuilder) ([]FirewallRule, error) {
	pages, err := rules.List(c, opts).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing firewall rules")
	}
	page, ok := pages.(rules.RulePage)
	if !ok {
		return nil, errors.New("parsing firewall rule list: unexpected page type")
	}
	var all []FirewallRule
	if err := page.ExtractIntoSlicePtr(&all, "firewall_rules"); err != nil {
		return nil, wrap(err, "parsing firewall rule list")
	}
	return all, nil
}

// GetFirewallRule reads one firewall rule.
func GetFirewallRule(ctx context.Context, c *gophercloud.ServiceClient, id string) (*FirewallRule, error) {
	return firewallRule(rules.Get(ctx, c, id).Result, "getting firewall rule %s", id)
}

// CreateFirewallRule creates a firewall rule. rules.CreateOpts requires a
// protocol and an action, which neutron does not; pass a builder of its own
// to leave them to neutron.
func CreateFirewallRule(ctx context.Context, c *gophercloud.ServiceClient, opts rules.CreateOptsBuilder) (*FirewallRule, error) {
	return firewallRule(rules.Create(ctx, c, opts).Result, "creating firewall rule")
}

// UpdateFirewallRule updates a firewall rule.
func UpdateFirewallRule(ctx context.Context, c *gophercloud.ServiceClient, id string, opts rules.UpdateOptsBuilder) (*FirewallRule, error) {
	return firewallRule(rules.Update(ctx, c, id, opts).Result, "updating firewall rule %s", id)
}

// DeleteFirewallRule deletes a firewall rule.
func DeleteFirewallRule(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(rules.Delete(ctx, c, id).ExtractErr(), "deleting firewall rule %s", id)
}

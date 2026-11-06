package network

import (
	"context"
	"fmt"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/qos/policies"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/qos/ruletypes"
	"github.com/gophercloud/gophercloud/v2/pagination"

	"github.com/ftarasenko/koc/pkg/openstack"
)

// FindQoSPolicyID resolves a QoS policy name or ID to an ID.
func FindQoSPolicyID(ctx context.Context, c *gophercloud.ServiceClient, ref string) (string, error) {
	return findID(ctx, "QoS policy", ref, func(ctx context.Context) ([]policies.Policy, error) {
		pages, err := policies.List(c, policies.ListOpts{Name: ref}).AllPages(ctx)
		return extracted(pages, err, policies.ExtractPolicies)
	}, func(p policies.Policy) string { return p.ID })
}

// ListQoSPolicies lists every QoS policy matching opts.
func ListQoSPolicies(ctx context.Context, c *gophercloud.ServiceClient, opts policies.PolicyListOptsBuilder) ([]policies.Policy, error) {
	pages, err := policies.List(c, opts).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing network QoS policies")
	}
	all, err := policies.ExtractPolicies(pages)
	return all, wrap(err, "parsing the network QoS policy list")
}

// GetQoSPolicy reads one QoS policy. Its Rules carry every rule inline, of
// every type, as neutron sent them.
func GetQoSPolicy(ctx context.Context, c *gophercloud.ServiceClient, id string) (*policies.Policy, error) {
	p, err := policies.Get(ctx, c, id).Extract()
	return p, wrap(err, "getting network QoS policy %s", id)
}

// CreateQoSPolicy creates a QoS policy. Wrap opts with
// WithQoSPolicyCreateAttrs to send an explicit false shared or is_default,
// which policies.CreateOpts drops.
func CreateQoSPolicy(ctx context.Context, c *gophercloud.ServiceClient, opts policies.CreateOptsBuilder) (*policies.Policy, error) {
	p, err := policies.Create(ctx, c, opts).Extract()
	return p, wrap(err, "creating network QoS policy")
}

// UpdateQoSPolicy updates a QoS policy.
func UpdateQoSPolicy(ctx context.Context, c *gophercloud.ServiceClient, id string, opts policies.UpdateOptsBuilder) (*policies.Policy, error) {
	p, err := policies.Update(ctx, c, id, opts).Extract()
	return p, wrap(err, "updating network QoS policy %s", id)
}

// DeleteQoSPolicy deletes a QoS policy.
func DeleteQoSPolicy(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(policies.Delete(ctx, c, id).ExtractErr(), "deleting network QoS policy %s", id)
}

// QoS rules. gophercloud's qos/rules package models three of neutron's four
// rule types (it has no minimum_packet_rate), so these speak the envelope
// every type shares: a rule of type t ("bandwidth_limit", "dscp_marking",
// "minimum_bandwidth", "minimum_packet_rate") lives under
// /qos/policies/{policy}/{t}_rules and travels as {"{t}_rule": {…}}. A rule
// is the map neutron sent, so fields a newer neutron adds survive.

// QoSRuleNotFoundError is FindQoSRuleType's answer for a rule its policy
// does not carry. It matches openstack.ErrNotFound.
type QoSRuleNotFoundError struct {
	PolicyID, RuleID string
}

func (e *QoSRuleNotFoundError) Error() string {
	return fmt.Sprintf("QoS policy %s has no rule %s", e.PolicyID, e.RuleID)
}

// Is reports whether target is openstack.ErrNotFound.
func (e *QoSRuleNotFoundError) Is(target error) bool { return target == openstack.ErrNotFound }

// FindQoSRuleType returns the type of one of a policy's rules, read off the
// policy, which the per-rule calls need to address it.
func FindQoSRuleType(ctx context.Context, c *gophercloud.ServiceClient, policyID, ruleID string) (string, error) {
	p, err := policies.Get(ctx, c, policyID).Extract()
	if err != nil {
		return "", fmt.Errorf("looking up QoS rule %s: %w", ruleID, err)
	}
	for _, rule := range p.Rules {
		if fmt.Sprint(rule["id"]) == ruleID {
			return fmt.Sprint(rule["type"]), nil
		}
	}
	return "", &QoSRuleNotFoundError{PolicyID: policyID, RuleID: ruleID}
}

func qosRuleURL(c *gophercloud.ServiceClient, policyID, ruleType string, ruleID ...string) string {
	return c.ServiceURL(append([]string{"qos", "policies", policyID, ruleType + "_rules"}, ruleID...)...)
}

// GetQoSRule reads one rule of the given type.
func GetQoSRule(ctx context.Context, c *gophercloud.ServiceClient, policyID, ruleType, ruleID string) (map[string]any, error) {
	var doc map[string]map[string]any
	if err := getJSON(ctx, c, qosRuleURL(c, policyID, ruleType, ruleID), &doc); err != nil {
		return nil, fmt.Errorf("getting QoS rule %s: %w", ruleID, err)
	}
	return doc[ruleType+"_rule"], nil
}

// CreateQoSRule adds a rule of the given type to a policy.
func CreateQoSRule(ctx context.Context, c *gophercloud.ServiceClient, policyID, ruleType string, attrs map[string]any) (map[string]any, error) {
	var doc map[string]map[string]any
	resp, err := c.Post(ctx, qosRuleURL(c, policyID, ruleType), map[string]any{ruleType + "_rule": attrs}, &doc,
		&gophercloud.RequestOpts{OkCodes: []int{201}})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		return nil, fmt.Errorf("creating a %s rule on QoS policy %s: %w", ruleType, policyID, err)
	}
	return doc[ruleType+"_rule"], nil
}

// UpdateQoSRule updates one rule of the given type.
func UpdateQoSRule(ctx context.Context, c *gophercloud.ServiceClient, policyID, ruleType, ruleID string, attrs map[string]any) (map[string]any, error) {
	var doc map[string]map[string]any
	resp, err := c.Put(ctx, qosRuleURL(c, policyID, ruleType, ruleID), map[string]any{ruleType + "_rule": attrs}, &doc,
		&gophercloud.RequestOpts{OkCodes: []int{200}})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		return nil, fmt.Errorf("updating QoS rule %s: %w", ruleID, err)
	}
	return doc[ruleType+"_rule"], nil
}

// DeleteQoSRule deletes one rule of the given type.
func DeleteQoSRule(ctx context.Context, c *gophercloud.ServiceClient, policyID, ruleType, ruleID string) error {
	resp, err := c.Delete(ctx, qosRuleURL(c, policyID, ruleType, ruleID), &gophercloud.RequestOpts{OkCodes: []int{204}})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	return wrap(err, "deleting QoS rule %s", ruleID)
}

// QoSRuleTypeListOpts are the rule-type filters gophercloud's ListRuleTypes
// does not take. AllSupported lists the union of what every loaded
// mechanism driver supports, AllRules everything neutron's QoS code knows;
// neutron accepts one at a time, and AllSupported wins.
type QoSRuleTypeListOpts struct {
	AllSupported, AllRules bool
}

// ListQoSRuleTypes lists the QoS rule types the cloud supports.
func ListQoSRuleTypes(ctx context.Context, c *gophercloud.ServiceClient, opts QoSRuleTypeListOpts) ([]ruletypes.RuleType, error) {
	url := c.ServiceURL("qos", "rule-types")
	switch {
	case opts.AllSupported:
		url += "?all_supported=true"
	case opts.AllRules:
		url += "?all_rules=true"
	}
	pages, err := pagination.NewPager(c, url, func(r pagination.PageResult) pagination.Page {
		return ruletypes.ListRuleTypesPage{SinglePageBase: pagination.SinglePageBase(r)}
	}).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing QoS rule types")
	}
	all, err := ruletypes.ExtractRuleTypes(pages)
	return all, wrap(err, "parsing the QoS rule type list")
}

// GetQoSRuleType reads one rule type and the parameters each driver
// supports for it.
func GetQoSRuleType(ctx context.Context, c *gophercloud.ServiceClient, name string) (*ruletypes.RuleType, error) {
	rt, err := ruletypes.GetRuleType(ctx, c, name).Extract()
	return rt, wrap(err, "showing QoS rule type %s", name)
}

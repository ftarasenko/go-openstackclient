package network

import (
	"context"
	"fmt"
	"strconv"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/security/groups"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/security/rules"
	"github.com/gophercloud/gophercloud/v2/pagination"
)

// SecurityGroup is gophercloud's security group plus what it drops: the
// shared attribute and, on a single read, the rules exactly as neutron sent
// them.
type SecurityGroup struct {
	groups.SecGroup
	SecurityGroupShared

	// RawRules are the group's rules with every key neutron sent, including
	// ones rules.SecGroupRule lacks. Set by Get, Create and Update; empty for
	// a group read from a listing.
	RawRules []map[string]any `json:"-"`
}

// SecurityGroupShared is neutron's shared attribute of a security group (the
// security-groups-shared-filtering extension). A pointer: nil on a cloud
// without the extension.
type SecurityGroupShared struct {
	Shared *bool `json:"shared"`
}

// SecurityGroupListOpts is groups.ListOpts plus the shared filter, which
// gophercloud does not model.
type SecurityGroupListOpts struct {
	groups.ListOpts
	Shared *bool
}

// secGroupKey is the envelope of a single security group.
const secGroupKey = "security_group"

func extractSecurityGroup(r interface{ ExtractIntoStructPtr(any, string) error }) (*SecurityGroup, error) {
	var g SecurityGroup
	if err := r.ExtractIntoStructPtr(&g, secGroupKey); err != nil {
		return nil, err
	}
	var raw struct {
		Rules []map[string]any `json:"security_group_rules"`
	}
	if err := r.ExtractIntoStructPtr(&raw, secGroupKey); err != nil {
		return nil, err
	}
	g.RawRules = raw.Rules
	return &g, nil
}

// ListSecurityGroups lists every security group opts matches, all pages.
// groups.List takes the concrete ListOpts and has no ExtractGroupsInto, so
// the pager is built here over the same URL and page type.
func ListSecurityGroups(ctx context.Context, c *gophercloud.ServiceClient, opts SecurityGroupListOpts) ([]SecurityGroup, error) {
	q, err := gophercloud.BuildQueryString(&opts.ListOpts)
	if err != nil {
		return nil, fmt.Errorf("building security group query: %w", err)
	}
	params := q.Query()
	if opts.Shared != nil {
		params.Set("shared", strconv.FormatBool(*opts.Shared))
	}
	u := c.ServiceURL("security-groups")
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	pages, err := pagination.NewPager(c, u, func(r pagination.PageResult) pagination.Page {
		return groups.SecGroupPage{LinkedPageBase: pagination.LinkedPageBase{PageResult: r}}
	}).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing security groups")
	}
	page, ok := pages.(groups.SecGroupPage)
	if !ok {
		return nil, fmt.Errorf("parsing security group list: unexpected page type %T", pages)
	}
	var all []SecurityGroup
	if err := page.ExtractIntoSlicePtr(&all, "security_groups"); err != nil {
		return nil, wrap(err, "parsing security group list")
	}
	return all, nil
}

// GetSecurityGroup reads one security group, rules included.
func GetSecurityGroup(ctx context.Context, c *gophercloud.ServiceClient, id string) (*SecurityGroup, error) {
	g, err := extractSecurityGroup(groups.Get(ctx, c, id))
	return g, wrap(err, "getting security group %s", id)
}

// CreateSecurityGroup creates a security group. Wrap opts with
// WithSecurityGroupCreateAttrs to send extension attributes.
func CreateSecurityGroup(ctx context.Context, c *gophercloud.ServiceClient, opts groups.CreateOptsBuilder) (*SecurityGroup, error) {
	g, err := extractSecurityGroup(groups.Create(ctx, c, opts))
	return g, wrap(err, "creating security group")
}

// UpdateSecurityGroup updates a security group. Wrap opts with
// WithSecurityGroupUpdateAttrs to send extension attributes.
func UpdateSecurityGroup(ctx context.Context, c *gophercloud.ServiceClient, id string, opts groups.UpdateOptsBuilder) (*SecurityGroup, error) {
	g, err := extractSecurityGroup(groups.Update(ctx, c, id, opts))
	return g, wrap(err, "updating security group %s", id)
}

// DeleteSecurityGroup deletes a security group.
func DeleteSecurityGroup(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(groups.Delete(ctx, c, id).ExtractErr(), "deleting security group %s", id)
}

// ListSecurityGroupRules lists every security group rule opts matches, all
// pages.
func ListSecurityGroupRules(ctx context.Context, c *gophercloud.ServiceClient, opts rules.ListOptsBuilder) ([]rules.SecGroupRule, error) {
	pages, err := rules.List(c, opts).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing security group rules")
	}
	all, err := rules.ExtractRules(pages)
	return all, wrap(err, "parsing security group rule list")
}

// GetSecurityGroupRule reads one security group rule.
func GetSecurityGroupRule(ctx context.Context, c *gophercloud.ServiceClient, id string) (*rules.SecGroupRule, error) {
	r, err := rules.Get(ctx, c, id).Extract()
	return r, wrap(err, "getting security group rule %s", id)
}

// CreateSecurityGroupRule creates a security group rule. Wrap opts with
// WithSecurityGroupRuleCreateAttrs to send what rules.CreateOpts drops, such
// as a zero port_range_min (ICMP type 0).
func CreateSecurityGroupRule(ctx context.Context, c *gophercloud.ServiceClient, opts rules.CreateOptsBuilder) (*rules.SecGroupRule, error) {
	r, err := rules.Create(ctx, c, opts).Extract()
	return r, wrap(err, "creating security group rule")
}

// DeleteSecurityGroupRule deletes a security group rule.
func DeleteSecurityGroupRule(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(rules.Delete(ctx, c, id).ExtractErr(), "deleting security group rule %s", id)
}

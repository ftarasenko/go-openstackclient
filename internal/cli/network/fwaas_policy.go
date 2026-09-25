package network

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/fwaas_v2/policies"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

const (
	flagFWFirewallRule   = "firewall-rule"
	flagFWNoFirewallRule = "no-firewall-rule"
	flagFWAudited        = "audited"
	flagFWNoAudited      = "no-audited"
)

// newFirewallPolicyCommand builds "firewall group policy" (upstream
// network/v2/fwaas/policy.py).
func newFirewallPolicyCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "policy",
		Short: "Manage firewall policies (requires the fwaas_v2 extension)",
	}
	add := &cobra.Command{Use: "add", Short: "Insert a rule into a firewall policy"}
	add.AddCommand(newFirewallPolicyAddRuleCommand(a, o))
	remove := &cobra.Command{Use: "remove", Short: "Remove a rule from a firewall policy"}
	remove.AddCommand(newFirewallPolicyRemoveRuleCommand(a, o))
	cmd.AddCommand(
		add,
		remove,
		newFirewallPolicyCreateCommand(a, o),
		newFirewallPolicyDeleteCommand(a, o),
		newFirewallPolicyListCommand(a, o),
		newFirewallPolicySetCommand(a, o),
		newFirewallPolicyShowCommand(a, o),
		newFirewallPolicyUnsetCommand(a, o),
	)
	return cmd
}

// fwPolicyFlags carries the create/set options (upstream policy
// _get_common_parser plus --name/--firewall-rule/--no-firewall-rule/--project).
type fwPolicyFlags struct {
	name           string
	description    string
	audited        bool
	noAudited      bool
	share          bool
	noShare        bool
	firewallRules  []string
	noFirewallRule bool
	project        string
	projectDomain  string
	// projectID is --project resolved by RunE.
	projectID string
}

func bindFWPolicyCommonFlags(cmd *cobra.Command, f *fwPolicyFlags) {
	fl := cmd.Flags()
	fl.StringVar(&f.description, flagDescription, "", "description of the firewall policy")
	fl.BoolVar(&f.audited, flagFWAudited, false, "enable auditing for the policy")
	fl.BoolVar(&f.noAudited, flagFWNoAudited, false, "disable auditing for the policy")
	fl.BoolVar(&f.share, flagFWShare, false, "share the firewall policy with all projects")
	fl.BoolVar(&f.noShare, flagFWNoShare, false, "restrict the firewall policy to its project")
	fl.StringArrayVar(&f.firewallRules, flagFWFirewallRule, nil, "firewall rule to apply (name or ID, repeatable)")
	cmd.MarkFlagsMutuallyExclusive(flagFWAudited, flagFWNoAudited)
	cmd.MarkFlagsMutuallyExclusive(flagFWShare, flagFWNoShare)
}

// buildFWPolicyAttrs is upstream policy._get_common_attrs. policyID is "" on
// create; on set, --firewall-rule without --no-firewall-rule appends to the
// policy's current rules, and with it replaces them.
func buildFWPolicyAttrs(ctx context.Context, client *gophercloud.ServiceClient, f *fwPolicyFlags, policyID string) (map[string]any, error) {
	attrs := map[string]any{}
	switch {
	case len(f.firewallRules) > 0:
		var current []string
		if !f.noFirewallRule && policyID != "" {
			p, err := policies.Get(ctx, client, policyID).Extract()
			if err != nil {
				return nil, fmt.Errorf("getting firewall policy %s: %w", policyID, err)
			}
			current = p.Rules
		}
		ids, err := resolveEach(ctx, client, f.firewallRules, resolveFirewallRuleID)
		if err != nil {
			return nil, err
		}
		attrs["firewall_rules"] = append(append([]string{}, current...), ids...)
	case f.noFirewallRule:
		attrs["firewall_rules"] = []string{}
	}
	setBoolPair(attrs, "audited", f.audited, f.noAudited)
	if f.name != "" {
		attrs["name"] = f.name
	}
	if f.description != "" {
		attrs["description"] = f.description
	}
	setBoolPair(attrs, "shared", f.share, f.noShare)
	return attrs, nil
}

// fwPolicyShowFields renders upstream's show columns (display names, sorted).
func fwPolicyShowFields(p *policies.Policy) ([]string, []any) {
	return []string{"Audited", "Description", "Firewall Rules", "ID", "Name", "Project", "Shared"},
		[]any{p.Audited, p.Description, p.Rules, p.ID, p.Name, p.ProjectID, p.Shared}
}

func writeFWPolicy(o *output.Options, w io.Writer, p *policies.Policy) error {
	fields, values := fwPolicyShowFields(p)
	return o.WriteSingle(w, fields, values)
}

func newFirewallPolicyCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &fwPolicyFlags{}
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new firewall policy",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			f.name = args[0]
			ctx := cmd.Context()
			client, session, err := newNetworkSession(ctx, a)
			if err != nil {
				return err
			}
			if f.projectID, err = resolveProjectRef(ctx, session, f.project, f.projectDomain); err != nil {
				return err
			}
			return runFirewallPolicyCreate(ctx, client, o, f, cmd.OutOrStdout())
		},
	}
	bindFWPolicyCommonFlags(cmd, f)
	fl := cmd.Flags()
	fl.BoolVar(&f.noFirewallRule, flagFWNoFirewallRule, false, "create the policy with no firewall rules")
	fl.StringVar(&f.project, flagProject, "", "owner's project (name or ID)")
	fl.StringVar(&f.projectDomain, flagProjectDomain, "", projectDomainHelp)
	cmd.MarkFlagsMutuallyExclusive(flagFWFirewallRule, flagFWNoFirewallRule)
	return cmd
}

func runFirewallPolicyCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *fwPolicyFlags, w io.Writer) (err error) {
	defer func() { err = fwaasErr(ctx, client, err) }()
	attrs, err := buildFWPolicyAttrs(ctx, client, f, "")
	if err != nil {
		return err
	}
	if f.projectID != "" {
		attrs["project_id"] = f.projectID
	}
	p, err := policies.Create(ctx, client, fwPolicyBody(attrs)).Extract()
	if err != nil {
		return fmt.Errorf("creating firewall policy: %w", err)
	}
	return writeFWPolicy(o, w, p)
}

func newFirewallPolicyDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <firewall-policy> [<firewall-policy> ...]",
		Short: "Delete firewall policy(s)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			return runFirewallPolicyDelete(ctx, client, args)
		},
	}
}

func runFirewallPolicyDelete(ctx context.Context, client *gophercloud.ServiceClient, refs []string) error {
	return batchdelete.Each(refs, func(ref string) error {
		id, err := resolveFirewallPolicyID(ctx, client, ref)
		if err == nil {
			err = policies.Delete(ctx, client, id).ExtractErr()
		}
		if err != nil {
			return fwaasErr(ctx, client, fmt.Errorf("deleting firewall policy %s: %w", ref, err))
		}
		return nil
	})
}

func newFirewallPolicyListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var long bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List firewall policies",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			return runFirewallPolicyList(ctx, client, o, long, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&long, "long", false, "list additional fields in output")
	return cmd
}

func runFirewallPolicyList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, long bool, w io.Writer) (err error) {
	defer func() { err = fwaasErr(ctx, client, err) }()
	pages, err := policies.List(client, nil).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing firewall policies: %w", err)
	}
	all, err := policies.ExtractPolicies(pages)
	if err != nil {
		return fmt.Errorf("parsing firewall policy list: %w", err)
	}
	cols := []string{"ID", "Name", "Firewall Rules"}
	if long {
		cols = append(cols, "Description", "Audited", "Shared", "Project")
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(all))}
	for _, p := range all {
		row := []any{p.ID, p.Name, p.Rules}
		if long {
			row = append(row, p.Description, p.Audited, p.Shared, p.ProjectID)
		}
		t.Rows = append(t.Rows, row)
	}
	return o.WriteList(w, t)
}

func newFirewallPolicySetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &fwPolicyFlags{}
	cmd := &cobra.Command{
		Use:   "set <firewall-policy>",
		Short: "Set firewall policy properties",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			return runFirewallPolicySet(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	bindFWPolicyCommonFlags(cmd, f)
	fl := cmd.Flags()
	fl.StringVar(&f.name, flagFWName, "", "new name of the firewall policy")
	fl.BoolVar(&f.noFirewallRule, flagFWNoFirewallRule, false,
		"remove every firewall rule (with --firewall-rule: replace the rules instead of appending)")
	return cmd
}

func runFirewallPolicySet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, f *fwPolicyFlags, w io.Writer) (err error) {
	defer func() { err = fwaasErr(ctx, client, err) }()
	id, err := resolveFirewallPolicyID(ctx, client, ref)
	if err != nil {
		return err
	}
	attrs, err := buildFWPolicyAttrs(ctx, client, f, id)
	if err != nil {
		return err
	}
	if err := fwRequireChange(attrs, "firewall group policy set"); err != nil {
		return err
	}
	return updateFWPolicy(ctx, client, o, ref, id, attrs, w)
}

func updateFWPolicy(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref, id string, attrs map[string]any, w io.Writer) error {
	p, err := policies.Update(ctx, client, id, fwPolicyBody(attrs)).Extract()
	if err != nil {
		return fmt.Errorf("updating firewall policy %s: %w", ref, err)
	}
	return writeFWPolicy(o, w, p)
}

func newFirewallPolicyShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <firewall-policy>",
		Short: "Display firewall policy details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			return runFirewallPolicyShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runFirewallPolicyShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, w io.Writer) (err error) {
	defer func() { err = fwaasErr(ctx, client, err) }()
	id, err := resolveFirewallPolicyID(ctx, client, ref)
	if err != nil {
		return err
	}
	p, err := policies.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("getting firewall policy %s: %w", ref, err)
	}
	return writeFWPolicy(o, w, p)
}

// fwPolicyUnsetFlags mirrors upstream UnsetFirewallPolicy; --share is its
// deprecated spelling of set --no-share.
type fwPolicyUnsetFlags struct {
	firewallRules   []string
	allFirewallRule bool
	audited         bool
	share           bool
}

func newFirewallPolicyUnsetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &fwPolicyUnsetFlags{}
	cmd := &cobra.Command{
		Use:   "unset <firewall-policy>",
		Short: "Unset firewall policy properties",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			if f.share {
				fwDeprecated(cmd, `the --share option is deprecated; use "firewall group policy set --no-share" instead`)
			}
			ctx := cmd.Context()
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			return runFirewallPolicyUnset(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringArrayVar(&f.firewallRules, flagFWFirewallRule, nil, "firewall rule to remove from the policy (name or ID, repeatable)")
	fl.BoolVar(&f.allFirewallRule, "all-firewall-rule", false, "remove every firewall rule from the policy")
	fl.BoolVar(&f.audited, flagFWAudited, false, "disable auditing for the policy")
	fl.BoolVar(&f.share, flagFWShare, false, `(deprecated, use "firewall group policy set --no-share") restrict the policy to its project`)
	cmd.MarkFlagsMutuallyExclusive(flagFWFirewallRule, "all-firewall-rule")
	return cmd
}

func runFirewallPolicyUnset(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, f *fwPolicyUnsetFlags, w io.Writer) (err error) {
	defer func() { err = fwaasErr(ctx, client, err) }()
	id, err := resolveFirewallPolicyID(ctx, client, ref)
	if err != nil {
		return err
	}
	attrs := map[string]any{}
	if len(f.firewallRules) > 0 {
		p, err := policies.Get(ctx, client, id).Extract()
		if err != nil {
			return fmt.Errorf("getting firewall policy %s: %w", ref, err)
		}
		removed, err := resolveEach(ctx, client, f.firewallRules, resolveFirewallRuleID)
		if err != nil {
			return err
		}
		attrs["firewall_rules"] = keepUnmatched(p.Rules, func(r string) bool { return slices.Contains(removed, r) })
	}
	if f.allFirewallRule {
		attrs["firewall_rules"] = []string{}
	}
	if f.audited {
		attrs["audited"] = false
	}
	if f.share {
		attrs["shared"] = false
	}
	if err := fwRequireChange(attrs, "firewall group policy unset"); err != nil {
		return err
	}
	return updateFWPolicy(ctx, client, o, ref, id, attrs, w)
}

// errFWRuleRequired is upstream's _get_required_firewall_rule check.
var errFWRuleRequired = errors.New("firewall rule (name or ID) is required")

// fwInsertRuleBody is upstream's args2body for insert_rule: all three keys are
// always sent, the positions as "" when not given. gophercloud's InsertRuleOpts
// omits empty positions and rejects a call with neither (its xor tag), which
// upstream allows — neutron then inserts the rule at the top of the policy.
type fwInsertRuleBody struct {
	ruleID, before, after string
}

func (b fwInsertRuleBody) ToFirewallPolicyInsertRuleMap() (map[string]any, error) {
	return map[string]any{"firewall_rule_id": b.ruleID, "insert_before": b.before, "insert_after": b.after}, nil
}

func newFirewallPolicyAddRuleCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var before, after string
	cmd := &cobra.Command{
		Use:   "rule <firewall-policy> <firewall-rule>",
		Short: "Insert a rule into a given firewall policy",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			return runFirewallPolicyAddRule(ctx, client, args[0], args[1], before, after, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&before, "insert-before", "", "insert the new rule before this existing rule (name or ID)")
	fl.StringVar(&after, "insert-after", "", "insert the new rule after this existing rule (name or ID)")
	return cmd
}

func runFirewallPolicyAddRule(ctx context.Context, client *gophercloud.ServiceClient, policyRef, ruleRef, before, after string, w io.Writer) (err error) {
	defer func() { err = fwaasErr(ctx, client, err) }()
	if ruleRef == "" {
		return errFWRuleRequired
	}
	policyID, err := resolveFirewallPolicyID(ctx, client, policyRef)
	if err != nil {
		return err
	}
	body := fwInsertRuleBody{}
	for _, r := range []struct {
		ref string
		dst *string
	}{{ruleRef, &body.ruleID}, {before, &body.before}, {after, &body.after}} {
		if r.ref == "" {
			continue
		}
		if *r.dst, err = resolveFirewallRuleID(ctx, client, r.ref); err != nil {
			return err
		}
	}
	if err := policies.InsertRule(ctx, client, policyID, body).Err; err != nil {
		return fmt.Errorf("inserting firewall rule %s into firewall policy %s: %w", ruleRef, policyRef, err)
	}
	_, err = fmt.Fprintf(w, "Inserted firewall rule %s in firewall policy %s\n", body.ruleID, policyRef)
	return err
}

func newFirewallPolicyRemoveRuleCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "rule <firewall-policy> <firewall-rule>",
		Short: "Remove a rule from a given firewall policy",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			return runFirewallPolicyRemoveRule(ctx, client, args[0], args[1], cmd.OutOrStdout())
		},
	}
}

func runFirewallPolicyRemoveRule(ctx context.Context, client *gophercloud.ServiceClient, policyRef, ruleRef string, w io.Writer) (err error) {
	defer func() { err = fwaasErr(ctx, client, err) }()
	if ruleRef == "" {
		return errFWRuleRequired
	}
	policyID, err := resolveFirewallPolicyID(ctx, client, policyRef)
	if err != nil {
		return err
	}
	ruleID, err := resolveFirewallRuleID(ctx, client, ruleRef)
	if err != nil {
		return err
	}
	if err := policies.RemoveRule(ctx, client, policyID, ruleID).Err; err != nil {
		return fmt.Errorf("removing firewall rule %s from firewall policy %s: %w", ruleRef, policyRef, err)
	}
	_, err = fmt.Fprintf(w, "Removed firewall rule %s from firewall policy %s\n", ruleID, policyRef)
	return err
}

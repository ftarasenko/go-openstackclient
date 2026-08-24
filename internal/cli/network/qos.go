package network

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/qos/policies"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/qos/ruletypes"
	"github.com/gophercloud/gophercloud/v2/pagination"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// "koc network qos policy|rule|rule type …", mirroring the upstream OSC
// network_qos_* commands.
//
// Flag names follow upstream OSC. UNVERIFIED against KeyStack docs
// (https://docs.keystack.ru/ returned HTTP 403 at implementation time); falls
// back to upstream OSC semantics.

func newQoSCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "qos", Short: "Manage network QoS policies and rules"}
	cmd.AddCommand(newQoSPolicyCommand(a, o), newQoSRuleCommand(a, o))
	return cmd
}

// --- network qos policy ------------------------------------------------------

func newQoSPolicyCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "policy", Short: "Manage network QoS policies"}
	cmd.AddCommand(
		newQoSPolicyListCommand(a, o),
		newQoSPolicyShowCommand(a, o),
		newQoSPolicyCreateCommand(a, o),
		newQoSPolicySetCommand(a, o),
		newQoSPolicyDeleteCommand(a, o),
	)
	return cmd
}

func qosPolicyShowFields(p *policies.Policy) ([]string, []any) {
	return []string{
			"id", "name", "description", "shared", "is_default", "project_id",
			"revision_number", "tags", "created_at", "updated_at",
		}, []any{
			p.ID, p.Name, p.Description, p.Shared, p.IsDefault, p.ProjectID,
			p.RevisionNumber, strings.Join(p.Tags, ", "), p.CreatedAt, p.UpdatedAt,
		}
}

func newQoSPolicyListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var project string
	var share, noShare bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List network QoS policies",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			if err := mutuallyExclusive(cmd.Flags(), flagShare, flagNoShare); err != nil {
				return err
			}
			c, err := newNetworkClient(cmd.Context(), a)
			if err != nil {
				return err
			}
			return runQoSPolicyList(cmd.Context(), c, o, project, share, noShare, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&project, "project", "", "filter by project ID")
	fl.BoolVar(&share, flagShare, false, "list only shared policies")
	fl.BoolVar(&noShare, flagNoShare, false, "list only unshared policies")
	return cmd
}

func runQoSPolicyList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	project string, share, noShare bool, w io.Writer,
) error {
	opts := policies.ListOpts{ProjectID: project}
	// Shared is a *bool, so --no-share reaches neutron as shared=false rather
	// than being dropped as a zero value.
	switch {
	case share:
		t := true
		opts.Shared = &t
	case noShare:
		f := false
		opts.Shared = &f
	}
	pages, err := policies.List(client, opts).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing network QoS policies: %w", err)
	}
	all, err := policies.ExtractPolicies(pages)
	if err != nil {
		return fmt.Errorf("parsing the network QoS policy list: %w", err)
	}
	t := output.Table{
		Columns: []string{"ID", "Name", "Shared", "Default", "Project"},
		Rows:    make([][]any, 0, len(all)),
	}
	for _, p := range all {
		t.Rows = append(t.Rows, []any{p.ID, p.Name, p.Shared, p.IsDefault, p.ProjectID})
	}
	return o.WriteList(w, t)
}

func newQoSPolicyShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <qos-policy>",
		Short: "Show a network QoS policy",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			c, err := newNetworkClient(cmd.Context(), a)
			if err != nil {
				return err
			}
			return runQoSPolicyShow(cmd.Context(), c, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runQoSPolicyShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	ref string, w io.Writer,
) error {
	id, err := resolveQoSPolicyID(ctx, client, ref)
	if err != nil {
		return err
	}
	p, err := policies.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("showing network QoS policy %s: %w", ref, err)
	}
	fields, values := qosPolicyShowFields(p)
	return o.WriteSingle(w, fields, values)
}

func newQoSPolicyCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var description, project string
	var share, noShare, isDefault, noDefault bool
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a network QoS policy",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			if err := mutuallyExclusive(cmd.Flags(), flagShare, flagNoShare); err != nil {
				return err
			}
			if err := mutuallyExclusive(cmd.Flags(), flagDefault, flagNoDefault); err != nil {
				return err
			}
			c, err := newNetworkClient(cmd.Context(), a)
			if err != nil {
				return err
			}
			// --no-share / --no-default exist for OSC parity; they select the
			// neutron defaults, so there is nothing extra to send.
			return runQoSPolicyCreate(cmd.Context(), c, o, args[0], description, project,
				share, isDefault, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&description, "description", "", "description of the policy")
	fl.StringVar(&project, "project", "", "owning project ID")
	fl.BoolVar(&share, flagShare, false, "make the policy usable by every project")
	fl.BoolVar(&noShare, flagNoShare, false, "keep the policy private to its project (default)")
	fl.BoolVar(&isDefault, flagDefault, false, "make this the project's default policy")
	fl.BoolVar(&noDefault, flagNoDefault, false, "do not make this the default policy (default)")
	return cmd
}

func runQoSPolicyCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	name, description, project string, share, isDefault bool, w io.Writer,
) error {
	p, err := policies.Create(ctx, client, policies.CreateOpts{
		Name:        name,
		Description: description,
		ProjectID:   project,
		Shared:      share,
		IsDefault:   isDefault,
	}).Extract()
	if err != nil {
		return fmt.Errorf("creating network QoS policy %q: %w", name, err)
	}
	fields, values := qosPolicyShowFields(p)
	return o.WriteSingle(w, fields, values)
}

func newQoSPolicySetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var name, description string
	var share, noShare, isDefault, noDefault bool
	cmd := &cobra.Command{
		Use:   "set <qos-policy>",
		Short: "Set network QoS policy properties",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			fl := cmd.Flags()
			if err := mutuallyExclusive(fl, flagShare, flagNoShare); err != nil {
				return err
			}
			if err := mutuallyExclusive(fl, flagDefault, flagNoDefault); err != nil {
				return err
			}
			c, err := newNetworkClient(cmd.Context(), a)
			if err != nil {
				return err
			}
			return runQoSPolicySet(cmd.Context(), c, o, args[0], name, description,
				share, noShare, isDefault, noDefault, fl.Changed("description"), cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&name, "name", "", "new name")
	fl.StringVar(&description, "description", "", "new description")
	fl.BoolVar(&share, flagShare, false, "make the policy usable by every project")
	fl.BoolVar(&noShare, flagNoShare, false, "make the policy private to its project")
	fl.BoolVar(&isDefault, flagDefault, false, "make this the project's default policy")
	fl.BoolVar(&noDefault, flagNoDefault, false, "stop this being the default policy")
	return cmd
}

func runQoSPolicySet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	ref, name, description string, share, noShare, isDefault, noDefault, descSet bool, w io.Writer,
) error {
	id, err := resolveQoSPolicyID(ctx, client, ref)
	if err != nil {
		return err
	}
	opts := policies.UpdateOpts{Name: name}
	if descSet {
		opts.Description = &description
	}
	if share || noShare {
		opts.Shared = &share
	}
	if isDefault || noDefault {
		opts.IsDefault = &isDefault
	}
	p, err := policies.Update(ctx, client, id, opts).Extract()
	if err != nil {
		return fmt.Errorf("updating network QoS policy %s: %w", ref, err)
	}
	fields, values := qosPolicyShowFields(p)
	return o.WriteSingle(w, fields, values)
}

func newQoSPolicyDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <qos-policy> [<qos-policy> ...]",
		Short: "Delete network QoS policies",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			c, err := newNetworkClient(cmd.Context(), a)
			if err != nil {
				return err
			}
			return runQoSPolicyDelete(cmd.Context(), c, args)
		},
	}
}

func runQoSPolicyDelete(ctx context.Context, client *gophercloud.ServiceClient, refs []string) error {
	return batchdelete.Each(refs, func(ref string) error {
		id, err := resolveQoSPolicyID(ctx, client, ref)
		if err != nil {
			return err
		}
		if err := policies.Delete(ctx, client, id).ExtractErr(); err != nil {
			return fmt.Errorf("deleting network QoS policy %s: %w", ref, err)
		}
		return nil
	})
}

func resolveQoSPolicyID(ctx context.Context, client *gophercloud.ServiceClient, nameOrID string) (string, error) {
	return resolveByName(client, "QoS policy", nameOrID, func(c *gophercloud.ServiceClient) ([]policies.Policy, error) {
		pages, err := policies.List(c, policies.ListOpts{Name: nameOrID}).AllPages(ctx)
		if err != nil {
			return nil, err
		}
		return policies.ExtractPolicies(pages)
	}, func(p policies.Policy) string { return p.ID })
}

// --- network qos rule --------------------------------------------------------

// qosRuleKind maps the OSC --type spelling onto neutron's three URL/JSON names
// for a QoS rule.
//
// The rule verbs use raw ServiceClient calls rather than
// networking/v2/extensions/qos/rules: every rule type has the same
// {"<body>": {…}} envelope over /qos/policies/{id}/<collection>, and the typed
// package models only three of the four types (it has no minimum_packet_rate,
// added in neutron 2023.1), so one uniform path covers strictly more clouds
// than a four-way switch would. Replace with the typed package if it ever
// grows the missing type.
type qosRuleKind struct {
	cliType    string // --type value, e.g. "bandwidth-limit"
	apiType    string // the "type" field neutron reports, e.g. "bandwidth_limit"
	collection string // URL segment, e.g. "bandwidth_limit_rules"
	body       string // JSON envelope key, e.g. "bandwidth_limit_rule"
}

var qosRuleKinds = []qosRuleKind{
	{"bandwidth-limit", "bandwidth_limit", "bandwidth_limit_rules", "bandwidth_limit_rule"},
	{"dscp-marking", "dscp_marking", "dscp_marking_rules", "dscp_marking_rule"},
	{"minimum-bandwidth", "minimum_bandwidth", "minimum_bandwidth_rules", "minimum_bandwidth_rule"},
	{"minimum-packet-rate", "minimum_packet_rate", "minimum_packet_rate_rules", "minimum_packet_rate_rule"},
}

func qosRuleKindByCLIType(t string) (qosRuleKind, error) {
	for _, k := range qosRuleKinds {
		if k.cliType == t {
			return k, nil
		}
	}
	names := make([]string, 0, len(qosRuleKinds))
	for _, k := range qosRuleKinds {
		names = append(names, k.cliType)
	}
	return qosRuleKind{}, fmt.Errorf("unknown QoS rule type %q: expected one of %s", t, strings.Join(names, ", "))
}

func qosRuleKindByAPIType(t string) (qosRuleKind, error) {
	for _, k := range qosRuleKinds {
		if k.apiType == t {
			return k, nil
		}
	}
	return qosRuleKind{}, fmt.Errorf("the cloud reported QoS rule type %q, which koc does not know", t)
}

func newQoSRuleCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "rule", Short: "Manage network QoS policy rules"}
	cmd.AddCommand(
		newQoSRuleListCommand(a, o),
		newQoSRuleShowCommand(a, o),
		newQoSRuleCreateCommand(a, o),
		newQoSRuleSetCommand(a, o),
		newQoSRuleDeleteCommand(a, o),
		newQoSRuleTypeCommand(a, o),
	)
	return cmd
}

func newQoSRuleListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "list <qos-policy>",
		Short: "List the rules of a network QoS policy",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			c, err := newNetworkClient(cmd.Context(), a)
			if err != nil {
				return err
			}
			return runQoSRuleList(cmd.Context(), c, o, args[0], cmd.OutOrStdout())
		},
	}
}

// runQoSRuleList reads the rules straight off the policy. Neutron has no
// combined rule collection — one endpoint per rule type — but the policy
// carries every rule inline, so a single GET lists them all, including types
// koc has no typed knowledge of.
func runQoSRuleList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	ref string, w io.Writer,
) error {
	id, err := resolveQoSPolicyID(ctx, client, ref)
	if err != nil {
		return err
	}
	p, err := policies.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("listing the rules of network QoS policy %s: %w", ref, err)
	}
	t := output.Table{
		Columns: []string{"ID", "Type", "Properties"},
		Rows:    make([][]any, 0, len(p.Rules)),
	}
	for _, rule := range p.Rules {
		t.Rows = append(t.Rows, []any{
			fmt.Sprint(rule["id"]),
			fmt.Sprint(rule["type"]),
			summariseQoSRule(rule),
		})
	}
	return o.WriteList(w, t)
}

// summariseQoSRule renders every field but id/type as a stable key=value list,
// so a rule type koc does not model still shows its settings.
func summariseQoSRule(rule map[string]any) string {
	keys := make([]string, 0, len(rule))
	for k := range rule {
		if k == "id" || k == "type" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, rule[k]))
	}
	return strings.Join(parts, ", ")
}

// qosRuleTypeOf finds an existing rule's type by looking it up in its policy,
// which is what show/set/delete need to pick the right endpoint.
func qosRuleTypeOf(ctx context.Context, client *gophercloud.ServiceClient, policyID, ruleID string) (qosRuleKind, error) {
	p, err := policies.Get(ctx, client, policyID).Extract()
	if err != nil {
		return qosRuleKind{}, fmt.Errorf("looking up QoS rule %s: %w", ruleID, err)
	}
	for _, rule := range p.Rules {
		if fmt.Sprint(rule["id"]) == ruleID {
			return qosRuleKindByAPIType(fmt.Sprint(rule["type"]))
		}
	}
	return qosRuleKind{}, fmt.Errorf("QoS policy %s has no rule %s", policyID, ruleID)
}

func qosRuleURL(client *gophercloud.ServiceClient, k qosRuleKind, policyID string, ruleID ...string) string {
	parts := append([]string{"qos", "policies", policyID, k.collection}, ruleID...)
	return client.ServiceURL(parts...)
}

// writeQoSRule renders whatever neutron returned, so new fields in newer
// releases show up without a code change.
func writeQoSRule(o *output.Options, w io.Writer, rule map[string]any) error {
	keys := make([]string, 0, len(rule))
	for k := range rule {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	values := make([]any, 0, len(keys))
	for _, k := range keys {
		values = append(values, rule[k])
	}
	return o.WriteSingle(w, keys, values)
}

// qosRuleCall issues one raw QoS-rule request and unwraps the
// {"<rule>": {…}} envelope every rule endpoint uses. It is the single place
// the response body is closed, keeping the four verbs free of the boilerplate.
func qosRuleCall(ctx context.Context, client *gophercloud.ServiceClient, method, url string,
	k qosRuleKind, attrs map[string]any,
) (map[string]any, error) {
	var doc map[string]map[string]any
	var resp *http.Response
	var err error
	switch method {
	case http.MethodGet:
		resp, err = client.Get(ctx, url, &doc, &gophercloud.RequestOpts{OkCodes: []int{200}})
	case http.MethodPost:
		resp, err = client.Post(ctx, url, map[string]any{k.body: attrs}, &doc,
			&gophercloud.RequestOpts{OkCodes: []int{201}})
	case http.MethodPut:
		resp, err = client.Put(ctx, url, map[string]any{k.body: attrs}, &doc,
			&gophercloud.RequestOpts{OkCodes: []int{200}})
	case http.MethodDelete:
		resp, err = client.Delete(ctx, url, &gophercloud.RequestOpts{OkCodes: []int{204}})
	default:
		return nil, fmt.Errorf("unsupported QoS rule request method %q", method)
	}
	if resp != nil && resp.Body != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if err != nil {
		return nil, err
	}
	return doc[k.body], nil
}

func newQoSRuleShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <qos-policy> <rule-id>",
		Short: "Show a network QoS policy rule",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			c, err := newNetworkClient(cmd.Context(), a)
			if err != nil {
				return err
			}
			return runQoSRuleShow(cmd.Context(), c, o, args[0], args[1], cmd.OutOrStdout())
		},
	}
}

func runQoSRuleShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	ref, ruleID string, w io.Writer,
) error {
	policyID, k, err := resolveQoSRule(ctx, client, ref, ruleID)
	if err != nil {
		return err
	}
	rule, err := qosRuleCall(ctx, client, http.MethodGet, qosRuleURL(client, k, policyID, ruleID), k, nil)
	if err != nil {
		return fmt.Errorf("showing QoS rule %s: %w", ruleID, err)
	}
	return writeQoSRule(o, w, rule)
}

// resolveQoSRule turns the (policy ref, rule id) pair into the policy ID plus
// the rule's kind, which every per-rule verb needs before it can build a URL.
func resolveQoSRule(ctx context.Context, client *gophercloud.ServiceClient, ref, ruleID string) (string, qosRuleKind, error) {
	policyID, err := resolveQoSPolicyID(ctx, client, ref)
	if err != nil {
		return "", qosRuleKind{}, err
	}
	k, err := qosRuleTypeOf(ctx, client, policyID, ruleID)
	if err != nil {
		return "", qosRuleKind{}, err
	}
	return policyID, k, nil
}

type qosRuleFlags struct {
	maxKBps       int
	maxBurstKbits int
	minKBps       int
	minKpps       int
	dscpMark      int
	direction     string
}

func (f *qosRuleFlags) register(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.IntVar(&f.maxKBps, "max-kbps", 0, "maximum bandwidth in kbps (bandwidth-limit)")
	fl.IntVar(&f.maxBurstKbits, "max-burst-kbits", 0, "maximum burst size in kilobits (bandwidth-limit)")
	fl.IntVar(&f.minKBps, "min-kbps", 0, "guaranteed bandwidth in kbps (minimum-bandwidth)")
	fl.IntVar(&f.minKpps, "min-kpps", 0, "guaranteed packet rate in kpps (minimum-packet-rate)")
	fl.IntVar(&f.dscpMark, "dscp-mark", 0, "DSCP mark value (dscp-marking)")
	fl.StringVar(&f.direction, "direction", "", "traffic direction: egress, ingress or any")
}

// body builds the rule attributes for kind k. Only the fields the operator
// actually set are included, so an update patches nothing it was not asked to.
func (f *qosRuleFlags) body(k qosRuleKind, fl interface{ Changed(string) bool }) map[string]any {
	attrs := map[string]any{}
	set := func(flag, key string, v any) {
		if fl.Changed(flag) {
			attrs[key] = v
		}
	}
	switch k.apiType {
	case "bandwidth_limit":
		set("max-kbps", "max_kbps", f.maxKBps)
		set("max-burst-kbits", "max_burst_kbps", f.maxBurstKbits)
		set("direction", "direction", f.direction)
	case "dscp_marking":
		set("dscp-mark", "dscp_mark", f.dscpMark)
	case "minimum_bandwidth":
		set("min-kbps", "min_kbps", f.minKBps)
		set("direction", "direction", f.direction)
	case "minimum_packet_rate":
		set("min-kpps", "min_kpps", f.minKpps)
		set("direction", "direction", f.direction)
	}
	return attrs
}

func newQoSRuleCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &qosRuleFlags{}
	var ruleType string
	cmd := &cobra.Command{
		Use:   "create <qos-policy>",
		Short: "Create a network QoS policy rule",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			k, err := qosRuleKindByCLIType(ruleType)
			if err != nil {
				return err
			}
			c, err := newNetworkClient(cmd.Context(), a)
			if err != nil {
				return err
			}
			return runQoSRuleCreate(cmd.Context(), c, o, args[0], k, f.body(k, cmd.Flags()), cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&ruleType, "type", "",
		"rule type: bandwidth-limit, dscp-marking, minimum-bandwidth or minimum-packet-rate")
	f.register(cmd)
	_ = cmd.MarkFlagRequired("type")
	return cmd
}

func runQoSRuleCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	ref string, k qosRuleKind, attrs map[string]any, w io.Writer,
) error {
	policyID, err := resolveQoSPolicyID(ctx, client, ref)
	if err != nil {
		return err
	}
	rule, err := qosRuleCall(ctx, client, http.MethodPost, qosRuleURL(client, k, policyID), k, attrs)
	if err != nil {
		return fmt.Errorf("creating a %s rule on QoS policy %s: %w", k.cliType, ref, err)
	}
	return writeQoSRule(o, w, rule)
}

func newQoSRuleSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &qosRuleFlags{}
	cmd := &cobra.Command{
		Use:   "set <qos-policy> <rule-id>",
		Short: "Set network QoS policy rule properties",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			c, err := newNetworkClient(cmd.Context(), a)
			if err != nil {
				return err
			}
			return runQoSRuleSet(cmd.Context(), c, o, args[0], args[1], f, cmd.Flags(), cmd.OutOrStdout())
		},
	}
	f.register(cmd)
	return cmd
}

func runQoSRuleSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	ref, ruleID string, f *qosRuleFlags, fl interface{ Changed(string) bool }, w io.Writer,
) error {
	policyID, k, err := resolveQoSRule(ctx, client, ref, ruleID)
	if err != nil {
		return err
	}
	attrs := f.body(k, fl)
	if len(attrs) == 0 {
		return fmt.Errorf("nothing to set on QoS rule %s: give at least one property flag", ruleID)
	}
	rule, err := qosRuleCall(ctx, client, http.MethodPut, qosRuleURL(client, k, policyID, ruleID), k, attrs)
	if err != nil {
		return fmt.Errorf("updating QoS rule %s: %w", ruleID, err)
	}
	return writeQoSRule(o, w, rule)
}

func newQoSRuleDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <qos-policy> <rule-id> [<rule-id> ...]",
		Short: "Delete network QoS policy rules",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			c, err := newNetworkClient(cmd.Context(), a)
			if err != nil {
				return err
			}
			return runQoSRuleDelete(cmd.Context(), c, args[0], args[1:])
		},
	}
}

func runQoSRuleDelete(ctx context.Context, client *gophercloud.ServiceClient, ref string, ruleIDs []string) error {
	return batchdelete.Each(ruleIDs, func(ruleID string) error {
		policyID, k, err := resolveQoSRule(ctx, client, ref, ruleID)
		if err != nil {
			return err
		}
		if _, err := qosRuleCall(ctx, client, http.MethodDelete,
			qosRuleURL(client, k, policyID, ruleID), k, nil); err != nil {
			return fmt.Errorf("deleting QoS rule %s: %w", ruleID, err)
		}
		return nil
	})
}

// --- network qos rule type ---------------------------------------------------

func newQoSRuleTypeCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "type", Short: "Show the QoS rule types the cloud supports"}
	cmd.AddCommand(newQoSRuleTypeListCommand(a, o), newQoSRuleTypeShowCommand(a, o))
	return cmd
}

func newQoSRuleTypeListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var allSupported, allRules bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the supported QoS rule types",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			c, err := newNetworkClient(cmd.Context(), a)
			if err != nil {
				return err
			}
			return runQoSRuleTypeList(cmd.Context(), c, o, allSupported, allRules, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&allSupported, "all-supported", false,
		"list the union of the rule types every loaded mechanism driver supports")
	fl.BoolVar(&allRules, "all-rules", false, "list every rule type implemented in neutron's QoS driver")
	cmd.MarkFlagsMutuallyExclusive("all-supported", "all-rules")
	return cmd
}

// ruleTypesQuery is the rule-type listing gophercloud does not model: its
// ListRuleTypes takes no options at all, while neutron accepts all_supported and
// all_rules — the difference between "what this cloud can enforce" and "what the
// code knows about". The pager is rebuilt around the same page type rather than
// dropping to a raw Get, so ExtractRuleTypes keeps decoding the body.
func ruleTypesQuery(client *gophercloud.ServiceClient, allSupported, allRules bool) pagination.Pager {
	url := client.ServiceURL("qos", "rule-types")
	switch {
	case allSupported:
		url += "?all_supported=true"
	case allRules:
		url += "?all_rules=true"
	}
	return pagination.NewPager(client, url, func(r pagination.PageResult) pagination.Page {
		return ruletypes.ListRuleTypesPage{SinglePageBase: pagination.SinglePageBase(r)}
	})
}

func runQoSRuleTypeList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	allSupported, allRules bool, w io.Writer,
) error {
	pages, err := ruleTypesQuery(client, allSupported, allRules).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing QoS rule types: %w", err)
	}
	all, err := ruletypes.ExtractRuleTypes(pages)
	if err != nil {
		return fmt.Errorf("parsing the QoS rule type list: %w", err)
	}
	t := output.Table{Columns: []string{"Type"}, Rows: make([][]any, 0, len(all))}
	for _, rt := range all {
		t.Rows = append(t.Rows, []any{rt.Type})
	}
	return o.WriteList(w, t)
}

func newQoSRuleTypeShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <rule-type>",
		Short: "Show a QoS rule type and its supported parameters",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			c, err := newNetworkClient(cmd.Context(), a)
			if err != nil {
				return err
			}
			return runQoSRuleTypeShow(cmd.Context(), c, o, args[0], cmd.OutOrStdout())
		},
	}
}

// runQoSRuleTypeShow accepts both the OSC hyphenated spelling and neutron's own
// underscored one, since "rule type list" prints the latter.
func runQoSRuleTypeShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	name string, w io.Writer,
) error {
	if k, err := qosRuleKindByCLIType(name); err == nil {
		name = k.apiType
	}
	rt, err := ruletypes.GetRuleType(ctx, client, name).Extract()
	if err != nil {
		return fmt.Errorf("showing QoS rule type %s: %w", name, err)
	}
	drivers := make([]string, 0, len(rt.Drivers))
	for _, d := range rt.Drivers {
		params := make([]string, 0, len(d.SupportedParameters))
		for _, p := range d.SupportedParameters {
			params = append(params, fmt.Sprintf("%s (%s: %v)", p.ParameterName, p.ParameterType, p.ParameterValues))
		}
		drivers = append(drivers, fmt.Sprintf("%s: %s", d.Name, strings.Join(params, "; ")))
	}
	return o.WriteSingle(w, []string{"type", "drivers"}, []any{rt.Type, strings.Join(drivers, "\n")})
}

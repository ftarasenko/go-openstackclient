package network

import (
	"context"
	"fmt"
	"io"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/qos/policies"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
	"github.com/ftarasenko/go-openstackclient/internal/output"
	networkapi "github.com/ftarasenko/go-openstackclient/pkg/openstack/network"
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

var qosPolicyColumns = fieldNames(qosPolicyShowFields(&policies.Policy{}))

func newQoSPolicyListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var project, projectDomain string
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
			ctx := cmd.Context()
			c, session, err := newNetworkSession(ctx, a)
			if err != nil {
				return err
			}
			projectID, err := resolveProjectRef(ctx, session, project, projectDomain)
			if err != nil {
				return err
			}
			return runQoSPolicyList(ctx, c, o, projectID, share, noShare, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&project, flagProject, "", "list only policies owned by this project (name or ID)")
	fl.StringVar(&projectDomain, flagProjectDomain, "", projectDomainHelp)
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
	all, err := networkapi.ListQoSPolicies(ctx, client, opts)
	if err != nil {
		return err
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
	p, err := networkapi.GetQoSPolicy(ctx, client, id)
	if err != nil {
		return relabel(err, "showing network QoS policy %s", ref)
	}
	fields, values := qosPolicyShowFields(p)
	return o.WriteSingle(w, fields, values)
}

type qosPolicyCreateFlags struct {
	description   string
	project       string
	projectDomain string
	// projectID is --project resolved by RunE, so the seam needs no identity client.
	projectID     string
	share         bool
	noShare       bool
	isDefault     bool
	noDefault     bool
	extraProperty []string
}

func newQoSPolicyCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &qosPolicyCreateFlags{}
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
			ctx := cmd.Context()
			c, session, err := newNetworkSession(ctx, a)
			if err != nil {
				return err
			}
			if f.projectID, err = resolveProjectRef(ctx, session, f.project, f.projectDomain); err != nil {
				return err
			}
			return runQoSPolicyCreate(ctx, c, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.description, flagDescription, "", "description of the policy")
	fl.StringVar(&f.project, flagProject, "", "owner's project (name or ID)")
	fl.StringVar(&f.projectDomain, flagProjectDomain, "", projectDomainHelp)
	fl.BoolVar(&f.share, flagShare, false, "make the policy usable by every project")
	fl.BoolVar(&f.noShare, flagNoShare, false, "keep the policy private to its project (default)")
	fl.BoolVar(&f.isDefault, flagDefault, false, "make this the project's default policy")
	fl.BoolVar(&f.noDefault, flagNoDefault, false, "do not make this the default policy (default)")
	bindExtraPropertyFlag(fl, &f.extraProperty)
	return cmd
}

func runQoSPolicyCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	name string, f *qosPolicyCreateFlags, w io.Writer,
) error {
	if err := o.CheckColumns(qosPolicyColumns...); err != nil {
		return err
	}
	opts := policies.CreateOpts{
		Name:        name,
		Description: f.description,
		ProjectID:   f.projectID,
		Shared:      f.share,
		IsDefault:   f.isDefault,
	}
	// CreateOpts drops a false Shared/IsDefault as a zero value, while upstream
	// sends --no-share / --no-default as an explicit false.
	var attrs map[string]any
	if f.noShare {
		attrs = mergeAttrs(attrs, map[string]any{"shared": false})
	}
	if f.noDefault {
		attrs = mergeAttrs(attrs, map[string]any{"is_default": false})
	}
	extra, err := parseExtraProperties(f.extraProperty, false)
	if err != nil {
		return err
	}
	attrs = mergeAttrs(attrs, extra)
	p, err := networkapi.CreateQoSPolicy(ctx, client, networkapi.WithQoSPolicyCreateAttrs(opts, attrs))
	if err != nil {
		return relabel(err, "creating network QoS policy %q", name)
	}
	fields, values := qosPolicyShowFields(p)
	return o.WriteSingle(w, fields, values)
}

type qosPolicySetFlags struct {
	name        string
	description string
	share       bool
	noShare     bool
	isDefault   bool
	noDefault   bool

	extraProperty []string

	// descSet records whether --description was given: an empty value is a
	// meaningful update, so it cannot be inferred from description alone.
	descSet bool
}

func newQoSPolicySetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &qosPolicySetFlags{}
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
			f.descSet = fl.Changed("description")
			return runQoSPolicySet(cmd.Context(), c, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "new name")
	fl.StringVar(&f.description, "description", "", "new description")
	fl.BoolVar(&f.share, flagShare, false, "make the policy usable by every project")
	fl.BoolVar(&f.noShare, flagNoShare, false, "make the policy private to its project")
	fl.BoolVar(&f.isDefault, flagDefault, false, "make this the project's default policy")
	fl.BoolVar(&f.noDefault, flagNoDefault, false, "stop this being the default policy")
	bindExtraPropertyFlag(fl, &f.extraProperty)
	return cmd
}

func runQoSPolicySet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	ref string, f *qosPolicySetFlags, w io.Writer,
) error {
	if err := o.CheckColumns(qosPolicyColumns...); err != nil {
		return err
	}
	id, err := resolveQoSPolicyID(ctx, client, ref)
	if err != nil {
		return err
	}
	opts := policies.UpdateOpts{Name: f.name}
	if f.descSet {
		opts.Description = &f.description
	}
	if f.share || f.noShare {
		opts.Shared = &f.share
	}
	if f.isDefault || f.noDefault {
		opts.IsDefault = &f.isDefault
	}
	extra, err := parseExtraProperties(f.extraProperty, false)
	if err != nil {
		return err
	}
	p, err := networkapi.UpdateQoSPolicy(ctx, client, id, networkapi.WithQoSPolicyUpdateAttrs(opts, extra))
	if err != nil {
		return relabel(err, "updating network QoS policy %s", ref)
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
		if err := networkapi.DeleteQoSPolicy(ctx, client, id); err != nil {
			return relabel(err, "deleting network QoS policy %s", ref)
		}
		return nil
	})
}

func resolveQoSPolicyID(ctx context.Context, client *gophercloud.ServiceClient, nameOrID string) (string, error) {
	return cliLookup(networkapi.FindQoSPolicyID(ctx, client, nameOrID))
}

// --- network qos rule --------------------------------------------------------

// qosRuleKind maps the OSC --type spelling onto the rule type neutron
// reports, which names the rule's endpoint (pkg/openstack/network's
// …QoSRule calls).
type qosRuleKind struct {
	cliType string // --type value, e.g. "bandwidth-limit"
	apiType string // the "type" field neutron reports, e.g. "bandwidth_limit"
}

var qosRuleKinds = []qosRuleKind{
	{"bandwidth-limit", "bandwidth_limit"},
	{"dscp-marking", "dscp_marking"},
	{"minimum-bandwidth", "minimum_bandwidth"},
	{"minimum-packet-rate", "minimum_packet_rate"},
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
	p, err := networkapi.GetQoSPolicy(ctx, client, id)
	if err != nil {
		return relabel(err, "listing the rules of network QoS policy %s", ref)
	}
	t := output.Table{Columns: qosRuleListColumns, Rows: make([][]any, 0, len(p.Rules))}
	for _, rule := range p.Rules {
		row := make([]any, 0, len(qosRuleListFields))
		for _, field := range qosRuleListFields {
			row = append(row, qosRuleCell(rule[field]))
		}
		t.Rows = append(t.Rows, row)
	}
	return o.WriteList(w, t)
}

// qosRuleListColumns/qosRuleListFields are upstream's ListNetworkQosRule
// columns: one per rule attribute, empty where a rule type has no such field.
// "Max Burst Kbits" reads max_burst_kbps — neutron's key, upstream's header.
var (
	qosRuleListColumns = []string{
		"ID", "QoS Policy ID", "Type", "Max Kbps", "Max Burst Kbits",
		"Min Kbps", "Min Kpps", "DSCP mark", "Direction",
	}
	qosRuleListFields = []string{
		"id", "qos_policy_id", "type", "max_kbps", "max_burst_kbps",
		"min_kbps", "min_kpps", "dscp_mark", "direction",
	}
)

// qosRuleCell renders one rule attribute: absent is empty, and JSON numbers
// (float64 after decoding) print as integers, which every QoS rule value is.
func qosRuleCell(v any) any {
	switch n := v.(type) {
	case nil:
		return ""
	case float64:
		return strconv.FormatFloat(n, 'f', -1, 64)
	default:
		return v
	}
}

// qosRuleTypeOf finds an existing rule's type by looking it up in its policy,
// which is what show/set/delete need to pick the right endpoint.
func qosRuleTypeOf(ctx context.Context, client *gophercloud.ServiceClient, policyID, ruleID string) (qosRuleKind, error) {
	t, err := networkapi.FindQoSRuleType(ctx, client, policyID, ruleID)
	if err != nil {
		return qosRuleKind{}, err
	}
	return qosRuleKindByAPIType(t)
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
	rule, err := networkapi.GetQoSRule(ctx, client, policyID, k.apiType, ruleID)
	if err != nil {
		return relabel(err, "showing QoS rule %s", ruleID)
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

// Upstream's direction spellings for a QoS rule (network_qos_rule.py). koc's
// own --direction <d> predates them and stays as a pass-through extra; all four
// are mutually exclusive.
const (
	flagQoSDirection = "direction"
	flagQoSIngress   = "ingress"
	flagQoSEgress    = "egress"
	flagQoSAny       = "any"

	flagQoSMaxKBps       = "max-kbps"
	flagQoSMaxBurstKbits = "max-burst-kbits"
	flagQoSMinKBps       = "min-kbps"
	flagQoSMinKpps       = "min-kpps"
	flagQoSDSCPMark      = "dscp-mark"
)

type qosRuleFlags struct {
	maxKBps       int
	maxBurstKbits int
	minKBps       int
	minKpps       int
	dscpMark      int
	direction     string
	ingress       bool
	egress        bool
	anyDirection  bool
	extraProperty []string

	// changed is the command's flag set, captured at construction so the run
	// seams take resolved flags instead of a second parameter.
	changed interface{ Changed(string) bool }
}

func (f *qosRuleFlags) register(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.IntVar(&f.maxKBps, flagQoSMaxKBps, 0, "maximum bandwidth in kbps (bandwidth-limit)")
	fl.IntVar(&f.maxBurstKbits, flagQoSMaxBurstKbits, 0, "maximum burst size in kilobits (bandwidth-limit)")
	fl.IntVar(&f.minKBps, flagQoSMinKBps, 0, "guaranteed bandwidth in kbps (minimum-bandwidth)")
	fl.IntVar(&f.minKpps, flagQoSMinKpps, 0, "guaranteed packet rate in kpps (minimum-packet-rate)")
	fl.IntVar(&f.dscpMark, flagQoSDSCPMark, 0, "DSCP mark value (dscp-marking)")
	fl.StringVar(&f.direction, flagQoSDirection, "", "traffic direction: egress, ingress or any")
	fl.BoolVar(&f.ingress, flagQoSIngress, false, "ingress traffic, from the project's point of view")
	fl.BoolVar(&f.egress, flagQoSEgress, false, "egress traffic, from the project's point of view")
	fl.BoolVar(&f.anyDirection, flagQoSAny, false, "traffic in either direction (minimum-packet-rate only)")
	bindExtraPropertyFlag(fl, &f.extraProperty)
	cmd.MarkFlagsMutuallyExclusive(flagQoSDirection, flagQoSIngress, flagQoSEgress, flagQoSAny)
}

// directionFor resolves the direction flags for kind k: ok is false when none
// was given. Upstream rejects --any on every type but minimum-packet-rate; the
// koc --direction spelling is passed through for neutron to validate.
func (f *qosRuleFlags) directionFor(k qosRuleKind) (dir string, ok bool, err error) {
	switch {
	case f.ingress:
		return "ingress", true, nil
	case f.egress:
		return "egress", true, nil
	case f.anyDirection:
		if k.apiType != "minimum_packet_rate" {
			return "", false, fmt.Errorf("--any can only be used with a minimum-packet-rate rule, not %s", k.cliType)
		}
		return "any", true, nil
	case f.changed.Changed(flagQoSDirection):
		if f.direction == "any" && k.apiType != "minimum_packet_rate" {
			return "", false, fmt.Errorf("--direction any can only be used with a minimum-packet-rate rule, not %s", k.cliType)
		}
		return f.direction, true, nil
	default:
		return "", false, nil
	}
}

// qosRuleParamFlags maps each rule parameter upstream validates to the koc
// flags that set it, in upstream's sorted order (its errors sort the names).
var qosRuleParamFlags = []struct {
	param string
	flags []string
}{
	{"direction", []string{flagQoSDirection, flagQoSIngress, flagQoSEgress, flagQoSAny}},
	{"dscp_mark", []string{flagQoSDSCPMark}},
	{"max_burst_kbps", []string{flagQoSMaxBurstKbits}},
	{"max_kbps", []string{flagQoSMaxKBps}},
	{"min_kbps", []string{flagQoSMinKBps}},
	{"min_kpps", []string{flagQoSMinKpps}},
}

// qosRuleParamSet is one rule type's row of upstream's MANDATORY_PARAMETERS /
// OPTIONAL_PARAMETERS table (network_qos_rule.py).
type qosRuleParamSet struct{ required, optional []string }

var qosRuleParams = map[string]qosRuleParamSet{
	"bandwidth_limit":     {required: []string{"max_kbps"}, optional: []string{"direction", "max_burst_kbps"}},
	"dscp_marking":        {required: []string{"dscp_mark"}},
	"minimum_bandwidth":   {required: []string{"direction", "min_kbps"}},
	"minimum_packet_rate": {required: []string{"direction", "min_kpps"}},
}

// checkType is upstream's _check_type_parameters, run before any request that
// would carry the rule: on create every required parameter of the type must be
// given, and on create and set a flag belonging to no parameter of the type is
// refused. Upstream only refuses a flag that is *required* by another type, so
// it lets --max-burst-kbits through on every type for neutron to reject; koc
// refuses it too. --extra-property is not checked, as upstream.
func (f *qosRuleFlags) checkType(k qosRuleKind, create bool) error {
	if _, _, err := f.directionFor(k); err != nil {
		return err
	}
	params := qosRuleParams[k.apiType]
	allowed := slices.Concat(params.required, params.optional)
	var missing, stray, accepted []string
	for _, pf := range qosRuleParamFlags {
		if slices.Contains(allowed, pf.param) {
			accepted = append(accepted, qosRuleParamSpelling(pf.param, pf.flags, k))
		}
		given := ""
		for _, name := range pf.flags {
			if f.changed.Changed(name) {
				given = "--" + name
				break
			}
		}
		switch {
		case given != "" && !slices.Contains(allowed, pf.param):
			stray = append(stray, given)
		case given == "" && create && slices.Contains(params.required, pf.param):
			missing = append(missing, qosRuleParamSpelling(pf.param, pf.flags, k))
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("\"create\" rule command for type %q requires arguments: %s",
			k.cliType, strings.Join(missing, ", "))
	}
	if len(stray) > 0 {
		return fmt.Errorf("rule type %q only accepts arguments: %s (got %s)",
			k.cliType, strings.Join(accepted, ", "), strings.Join(stray, ", "))
	}
	return nil
}

// qosRuleParamSpelling names a parameter by the flags that set it; direction
// is spelled with upstream's --ingress/--egress (and --any where it applies).
func qosRuleParamSpelling(param string, flags []string, k qosRuleKind) string {
	if param != "direction" {
		return "--" + flags[0]
	}
	if k.apiType == "minimum_packet_rate" {
		return "--ingress/--egress/--any"
	}
	return "--ingress/--egress"
}

// body builds the rule attributes for kind k. Only the fields the operator
// actually set are included, so an update patches nothing it was not asked to;
// checkType has already refused a flag belonging to another rule type.
// --extra-property is merged last, so it wins.
func (f *qosRuleFlags) body(k qosRuleKind) (map[string]any, error) {
	attrs := map[string]any{}
	set := func(flag, key string, v any) {
		if f.changed.Changed(flag) {
			attrs[key] = v
		}
	}
	dir, dirGiven, err := f.directionFor(k)
	if err != nil {
		return nil, err
	}
	setDirection := func() {
		if dirGiven {
			attrs["direction"] = dir
		}
	}
	switch k.apiType {
	case "bandwidth_limit":
		set(flagQoSMaxKBps, "max_kbps", f.maxKBps)
		set(flagQoSMaxBurstKbits, "max_burst_kbps", f.maxBurstKbits)
		setDirection()
	case "dscp_marking":
		set(flagQoSDSCPMark, "dscp_mark", f.dscpMark)
	case "minimum_bandwidth":
		set(flagQoSMinKBps, "min_kbps", f.minKBps)
		setDirection()
	case "minimum_packet_rate":
		set(flagQoSMinKpps, "min_kpps", f.minKpps)
		setDirection()
	}
	extra, err := parseExtraProperties(f.extraProperty, false)
	if err != nil {
		return nil, err
	}
	return mergeAttrs(attrs, extra), nil
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
			if err := f.checkType(k, true); err != nil {
				return err
			}
			attrs, err := f.body(k)
			if err != nil {
				return err
			}
			c, err := newNetworkClient(cmd.Context(), a)
			if err != nil {
				return err
			}
			return runQoSRuleCreate(cmd.Context(), c, o, args[0], k, attrs, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&ruleType, "type", "",
		"rule type: bandwidth-limit, dscp-marking, minimum-bandwidth or minimum-packet-rate")
	f.register(cmd)
	f.changed = cmd.Flags()
	_ = cmd.MarkFlagRequired("type")
	return cmd
}

func runQoSRuleCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	ref string, k qosRuleKind, attrs map[string]any, w io.Writer,
) error {
	if err := o.CheckColumns(qosRuleColumns...); err != nil {
		return err
	}
	policyID, err := resolveQoSPolicyID(ctx, client, ref)
	if err != nil {
		return err
	}
	rule, err := networkapi.CreateQoSRule(ctx, client, policyID, k.apiType, attrs)
	if err != nil {
		return relabel(err, "creating a %s rule on QoS policy %s", k.cliType, ref)
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
			return runQoSRuleSet(cmd.Context(), c, o, args[0], args[1], f, cmd.OutOrStdout())
		},
	}
	f.register(cmd)
	f.changed = cmd.Flags()
	return cmd
}

func runQoSRuleSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	ref, ruleID string, f *qosRuleFlags, w io.Writer,
) error {
	if err := o.CheckColumns(qosRuleColumns...); err != nil {
		return err
	}
	policyID, k, err := resolveQoSRule(ctx, client, ref, ruleID)
	if err != nil {
		return err
	}
	if err := f.checkType(k, false); err != nil {
		return err
	}
	attrs, err := f.body(k)
	if err != nil {
		return err
	}
	if len(attrs) == 0 {
		return fmt.Errorf("nothing to set on QoS rule %s: give at least one property flag", ruleID)
	}
	rule, err := networkapi.UpdateQoSRule(ctx, client, policyID, k.apiType, ruleID, attrs)
	if err != nil {
		return err
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
		return networkapi.DeleteQoSRule(ctx, client, policyID, k.apiType, ruleID)
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

func runQoSRuleTypeList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	allSupported, allRules bool, w io.Writer,
) error {
	all, err := networkapi.ListQoSRuleTypes(ctx, client,
		networkapi.QoSRuleTypeListOpts{AllSupported: allSupported, AllRules: allRules})
	if err != nil {
		return err
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
	rt, err := networkapi.GetQoSRuleType(ctx, client, name)
	if err != nil {
		return err
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

// qosRuleColumns is every attribute a QoS rule of any type can carry; rules
// render as neutron returns them, so the set is not known before the write.
var qosRuleColumns = []string{
	"direction", "dscp_mark", "id", "max_burst_kbps", "max_burst_kpps", "max_kbps",
	"max_kpps", "min_kbps", "min_kpps", "project_id", "qos_policy_id", "tenant_id", "type",
}

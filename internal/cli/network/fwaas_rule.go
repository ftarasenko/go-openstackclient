package network

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/fwaas_v2/rules"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Literals repeated within this file.
const (
	colFirewallPolicy = "Firewall Policy"
)

const (
	flagFWSourceIP    = "source-ip-address"
	flagFWDestIP      = "destination-ip-address"
	flagFWSourcePort  = "source-port"
	flagFWDestPort    = "destination-port"
	flagFWSourceGroup = "source-firewall-group"
	flagFWDestGroup   = "destination-firewall-group"
	flagFWEnableRule  = "enable-rule"
	flagFWDisableRule = "disable-rule"
)

// newFirewallRuleCommand builds "firewall group rule" (upstream
// network/v2/fwaas/rule.py).
func newFirewallRuleCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rule",
		Short: "Manage firewall rules (requires the fwaas_v2 extension)",
	}
	cmd.AddCommand(
		newFirewallRuleCreateCommand(a, o),
		newFirewallRuleDeleteCommand(a, o),
		newFirewallRuleListCommand(a, o),
		newFirewallRuleSetCommand(a, o),
		newFirewallRuleShowCommand(a, o),
		newFirewallRuleUnsetCommand(a, o),
	)
	return cmd
}

// fwRule is a firewall rule plus the remote-group attributes gophercloud's
// struct does not model. They arrive through an exported flat embed because
// gophercloud's slice extraction decodes each embedded struct separately and
// drops plain fields (see floatingIPExt).
type fwRule struct {
	rules.Rule
	FirewallRuleGroupsExt
}

// FirewallRuleGroupsExt carries fwaas_v2's source/destination_firewall_group_id.
type FirewallRuleGroupsExt struct {
	SourceFirewallGroupID      string `json:"source_firewall_group_id"`
	DestinationFirewallGroupID string `json:"destination_firewall_group_id"`
}

// fwRuleFlags carries the create/set options (upstream rule _get_common_parser
// plus --name/--project).
type fwRuleFlags struct {
	name          string
	description   string
	protocol      string
	action        string
	ipVersion     string
	sourceIP      string
	noSourceIP    bool
	destIP        string
	noDestIP      bool
	sourcePort    string
	noSourcePort  bool
	destPort      string
	noDestPort    bool
	share         bool
	noShare       bool
	enableRule    bool
	disableRule   bool
	sourceGroup   string
	noSourceGroup bool
	destGroup     string
	noDestGroup   bool
	project       string
	projectDomain string
	// projectID is --project resolved by RunE.
	projectID string
}

func bindFWRuleCommonFlags(cmd *cobra.Command, f *fwRuleFlags) {
	fl := cmd.Flags()
	fl.StringVar(&f.description, flagDescription, "", "description of the firewall rule")
	fl.StringVar(&f.protocol, "protocol", "", "IP protocol (tcp, udp, icmp, … an integer 0-255, or any)")
	fl.StringVar(&f.action, "action", "", "action for the firewall rule (allow, deny, reject)")
	fl.StringVar(&f.ipVersion, "ip-version", "", "IP version (4 or 6)")
	fl.StringVar(&f.sourceIP, flagFWSourceIP, "", "source IP address or subnet")
	fl.BoolVar(&f.noSourceIP, "no-"+flagFWSourceIP, false, "clear the source IP address")
	fl.StringVar(&f.destIP, flagFWDestIP, "", "destination IP address or subnet")
	fl.BoolVar(&f.noDestIP, "no-"+flagFWDestIP, false, "clear the destination IP address")
	fl.StringVar(&f.sourcePort, flagFWSourcePort, "", "source port number or range (1-65535, or a range like 123:456)")
	fl.BoolVar(&f.noSourcePort, "no-"+flagFWSourcePort, false, "clear the source port")
	fl.StringVar(&f.destPort, flagFWDestPort, "", "destination port number or range (1-65535, or a range like 123:456)")
	fl.BoolVar(&f.noDestPort, "no-"+flagFWDestPort, false, "clear the destination port")
	fl.BoolVar(&f.share, flagFWShare, false, "share the firewall rule with all projects")
	fl.BoolVar(&f.noShare, flagFWNoShare, false, "restrict the firewall rule to its project")
	fl.BoolVar(&f.enableRule, flagFWEnableRule, false, "enable the rule (the default)")
	fl.BoolVar(&f.disableRule, flagFWDisableRule, false, "disable the rule")
	fl.StringVar(&f.sourceGroup, flagFWSourceGroup, "", "source firewall group (name or ID)")
	fl.BoolVar(&f.noSourceGroup, "no-"+flagFWSourceGroup, false, "clear the source firewall group")
	fl.StringVar(&f.destGroup, flagFWDestGroup, "", "destination firewall group (name or ID)")
	fl.BoolVar(&f.noDestGroup, "no-"+flagFWDestGroup, false, "clear the destination firewall group")
	for _, pair := range [][2]string{
		{flagFWSourceIP, "no-" + flagFWSourceIP},
		{flagFWDestIP, "no-" + flagFWDestIP},
		{flagFWSourcePort, "no-" + flagFWSourcePort},
		{flagFWDestPort, "no-" + flagFWDestPort},
		{flagFWShare, flagFWNoShare},
		{flagFWEnableRule, flagFWDisableRule},
		{flagFWSourceGroup, "no-" + flagFWSourceGroup},
		{flagFWDestGroup, "no-" + flagFWDestGroup},
	} {
		cmd.MarkFlagsMutuallyExclusive(pair[0], pair[1])
	}
}

// buildFWRuleAttrs is upstream rule._get_common_attrs: --protocol and
// --action are lower-cased (argparse's type=), "any" sends a null protocol,
// and the address/port values go through verbatim — neutron parses the port
// range, as it does for upstream.
func buildFWRuleAttrs(ctx context.Context, client *gophercloud.ServiceClient, f *fwRuleFlags) (map[string]any, error) {
	attrs := map[string]any{}
	if f.name != "" {
		attrs["name"] = f.name
	}
	if f.description != "" {
		attrs["description"] = f.description
	}
	if f.protocol != "" {
		if p := strings.ToLower(f.protocol); p == "any" {
			attrs["protocol"] = nil
		} else {
			attrs["protocol"] = p
		}
	}
	if f.action != "" {
		a := strings.ToLower(f.action)
		if a != "allow" && a != "deny" && a != "reject" {
			return nil, fmt.Errorf("--action must be allow, deny or reject, got %q", f.action)
		}
		attrs["action"] = a
	}
	if f.ipVersion != "" {
		switch f.ipVersion {
		case "4":
			attrs["ip_version"] = 4
		case "6":
			attrs["ip_version"] = 6
		default:
			return nil, fmt.Errorf("--ip-version must be 4 or 6, got %q", f.ipVersion)
		}
	}
	for _, v := range []struct {
		key, val string
		clear    bool
	}{
		{"source_port", f.sourcePort, f.noSourcePort},
		{"source_ip_address", f.sourceIP, f.noSourceIP},
		{"destination_port", f.destPort, f.noDestPort},
		{"destination_ip_address", f.destIP, f.noDestIP},
	} {
		setOrClear(attrs, v.key, v.val, v.clear)
	}
	setBoolPair(attrs, "enabled", f.enableRule, f.disableRule)
	setBoolPair(attrs, "shared", f.share, f.noShare)
	if err := setFWRuleGroups(ctx, client, f, attrs); err != nil {
		return nil, err
	}
	return attrs, nil
}

// setOrClear sends val when given and null when its --no-* flag is.
func setOrClear(attrs map[string]any, key, val string, null bool) {
	if val != "" {
		attrs[key] = val
	}
	if null {
		attrs[key] = nil
	}
}

func setFWRuleGroups(ctx context.Context, client *gophercloud.ServiceClient, f *fwRuleFlags, attrs map[string]any) error {
	for _, g := range []struct {
		key, ref string
		clear    bool
	}{
		{"source_firewall_group_id", f.sourceGroup, f.noSourceGroup},
		{"destination_firewall_group_id", f.destGroup, f.noDestGroup},
	} {
		if g.ref != "" {
			id, err := resolveFirewallGroupID(ctx, client, g.ref)
			if err != nil {
				return err
			}
			attrs[g.key] = id
		}
		if g.clear {
			attrs[g.key] = nil
		}
	}
	return nil
}

// fwRuleProtocol is upstream's ProtocolColumn: a null protocol reads "any".
func fwRuleProtocol(p string) string {
	if p == "" {
		return "any"
	}
	return p
}

// fwRuleSummary is upstream ListFirewallRule.extend_list's summary cell.
func fwRuleSummary(r *fwRule) string {
	protocol := "ANY"
	if r.Protocol != "" {
		protocol = strings.ToUpper(r.Protocol)
	}
	srcIP, dstIP := "none specified", "none specified"
	srcPort, dstPort := "(none specified)", "(none specified)"
	if r.SourceIPAddress != "" {
		srcIP = strings.ToLower(r.SourceIPAddress)
	}
	if r.SourcePort != "" {
		srcPort = "(" + strings.ToLower(r.SourcePort) + ")"
	}
	if r.DestinationIPAddress != "" {
		dstIP = strings.ToLower(r.DestinationIPAddress)
	}
	if r.DestinationPort != "" {
		dstPort = "(" + strings.ToLower(r.DestinationPort) + ")"
	}
	action := "no-action"
	if r.Action != "" {
		action = r.Action
	}
	return strings.Join([]string{protocol, "source(port): " + srcIP + srcPort, "dest(port): " + dstIP + dstPort, action}, ",\n ")
}

// fwRuleShowFields renders upstream's show columns (display names, sorted).
// Upstream's Summary is always blank on show (the SDK computes it only for
// list); koc fills it in.
func fwRuleShowFields(r *fwRule) ([]string, []any) {
	return []string{
		"Action", "Description", "Destination Firewall Group ID", "Destination IP Address",
		"Destination Port", "Enabled", colFirewallPolicy, "ID", "IP Version", "Name", "Project",
		"Protocol", "Shared", "Source Firewall Group ID", "Source IP Address", "Source Port", "Summary",
	}, []any{
		r.Action, r.Description, r.DestinationFirewallGroupID, r.DestinationIPAddress,
		r.DestinationPort, r.Enabled, r.FirewallPolicyID, r.ID, r.IPVersion, r.Name, r.ProjectID,
		fwRuleProtocol(r.Protocol), r.Shared, r.SourceFirewallGroupID, r.SourceIPAddress, r.SourcePort, fwRuleSummary(r),
	}
}

// fwRuleResult is what the rules package's Create/Get/Update results share.
type fwRuleResult interface {
	ExtractIntoStructPtr(to any, label string) error
}

func writeFWRule(o *output.Options, w io.Writer, res fwRuleResult) error {
	var r fwRule
	if err := res.ExtractIntoStructPtr(&r, "firewall_rule"); err != nil {
		return err
	}
	fields, values := fwRuleShowFields(&r)
	return o.WriteSingle(w, fields, values)
}

func newFirewallRuleCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &fwRuleFlags{}
	cmd := &cobra.Command{
		Use:   "create [<name>]",
		Short: "Create a new firewall rule",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			name, err := fwCreateName(cmd, args, f.name, "firewall group rule create")
			if err != nil {
				return err
			}
			f.name = name
			ctx := cmd.Context()
			client, session, err := newNetworkSession(ctx, a)
			if err != nil {
				return err
			}
			if f.projectID, err = resolveProjectRef(ctx, session, f.project, f.projectDomain); err != nil {
				return err
			}
			return runFirewallRuleCreate(ctx, client, o, f, cmd.OutOrStdout())
		},
	}
	bindFWRuleCommonFlags(cmd, f)
	fl := cmd.Flags()
	fl.StringVar(&f.name, flagFWName, "", "(deprecated, pass the name as a positional argument) name of the firewall rule")
	fl.StringVar(&f.project, flagProject, "", "owner's project (name or ID)")
	fl.StringVar(&f.projectDomain, flagProjectDomain, "", projectDomainHelp)
	return cmd
}

func runFirewallRuleCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *fwRuleFlags, w io.Writer) (err error) {
	defer func() { err = fwaasErr(ctx, client, err) }()
	attrs, err := buildFWRuleAttrs(ctx, client, f)
	if err != nil {
		return err
	}
	if f.projectID != "" {
		attrs["project_id"] = f.projectID
	}
	res := rules.Create(ctx, client, fwRuleBody(attrs))
	if res.Err != nil {
		return fmt.Errorf("creating firewall rule: %w", res.Err)
	}
	return writeFWRule(o, w, res)
}

func newFirewallRuleDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <firewall-rule> [<firewall-rule> ...]",
		Short: "Delete firewall rule(s)",
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
			return runFirewallRuleDelete(ctx, client, args)
		},
	}
}

func runFirewallRuleDelete(ctx context.Context, client *gophercloud.ServiceClient, refs []string) error {
	return batchdelete.Each(refs, func(ref string) error {
		id, err := resolveFirewallRuleID(ctx, client, ref)
		if err == nil {
			err = rules.Delete(ctx, client, id).ExtractErr()
		}
		if err != nil {
			return fwaasErr(ctx, client, fmt.Errorf("deleting firewall rule %s: %w", ref, err))
		}
		return nil
	})
}

func newFirewallRuleListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var long bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List firewall rules",
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
			return runFirewallRuleList(ctx, client, o, long, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&long, "long", false, "list additional fields in output")
	return cmd
}

func runFirewallRuleList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, long bool, w io.Writer) (err error) {
	defer func() { err = fwaasErr(ctx, client, err) }()
	pages, err := rules.List(client, nil).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing firewall rules: %w", err)
	}
	page, ok := pages.(rules.RulePage)
	if !ok {
		return errors.New("parsing firewall rule list: unexpected page type")
	}
	var all []fwRule
	if err := page.ExtractIntoSlicePtr(&all, "firewall_rules"); err != nil {
		return fmt.Errorf("parsing firewall rule list: %w", err)
	}
	return o.WriteList(w, fwRuleListTable(all, long))
}

// fwRuleListTable renders upstream's columns: Summary replaces the address,
// port and action detail by default, and --long spells it out instead.
func fwRuleListTable(all []fwRule, long bool) output.Table {
	cols := []string{"ID", "Name", "Enabled", "Summary", colFirewallPolicy}
	if long {
		cols = []string{
			"ID", "Name", "Enabled", "Description", colFirewallPolicy, "IP Version", "Action",
			"Protocol", "Source IP Address", "Source Port", "Destination IP Address", "Destination Port",
			"Shared", "Project", "Source Firewall Group ID", "Destination Firewall Group ID",
		}
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(all))}
	for i := range all {
		r := &all[i]
		if !long {
			t.Rows = append(t.Rows, []any{r.ID, r.Name, r.Enabled, fwRuleSummary(r), r.FirewallPolicyID})
			continue
		}
		t.Rows = append(t.Rows, []any{
			r.ID, r.Name, r.Enabled, r.Description, r.FirewallPolicyID, r.IPVersion, r.Action,
			fwRuleProtocol(r.Protocol), r.SourceIPAddress, r.SourcePort, r.DestinationIPAddress, r.DestinationPort,
			r.Shared, r.ProjectID, r.SourceFirewallGroupID, r.DestinationFirewallGroupID,
		})
	}
	return t
}

func newFirewallRuleSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &fwRuleFlags{}
	cmd := &cobra.Command{
		Use:   "set <firewall-rule>",
		Short: "Set firewall rule properties",
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
			return runFirewallRuleSet(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	bindFWRuleCommonFlags(cmd, f)
	cmd.Flags().StringVar(&f.name, flagFWName, "", "new name of the firewall rule")
	return cmd
}

func runFirewallRuleSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, f *fwRuleFlags, w io.Writer) (err error) {
	defer func() { err = fwaasErr(ctx, client, err) }()
	attrs, err := buildFWRuleAttrs(ctx, client, f)
	if err != nil {
		return err
	}
	if err := fwRequireChange(attrs, "firewall group rule set"); err != nil {
		return err
	}
	return updateFWRule(ctx, client, o, ref, attrs, w)
}

func updateFWRule(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, attrs map[string]any, w io.Writer) error {
	id, err := resolveFirewallRuleID(ctx, client, ref)
	if err != nil {
		return err
	}
	res := rules.Update(ctx, client, id, fwRuleBody(attrs))
	if res.Err != nil {
		return fmt.Errorf("updating firewall rule %s: %w", ref, res.Err)
	}
	return writeFWRule(o, w, res)
}

func newFirewallRuleShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <firewall-rule>",
		Short: "Display firewall rule details",
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
			return runFirewallRuleShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runFirewallRuleShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, w io.Writer) (err error) {
	defer func() { err = fwaasErr(ctx, client, err) }()
	id, err := resolveFirewallRuleID(ctx, client, ref)
	if err != nil {
		return err
	}
	res := rules.Get(ctx, client, id)
	if res.Err != nil {
		return fmt.Errorf("getting firewall rule %s: %w", ref, res.Err)
	}
	return writeFWRule(o, w, res)
}

// fwRuleUnsetFlags mirrors upstream UnsetFirewallRule: every option is a
// switch that clears its attribute; --share and --enable-rule are upstream's
// deprecated spellings of set --no-share / --disable-rule.
type fwRuleUnsetFlags struct {
	sourceIP    bool
	destIP      bool
	sourcePort  bool
	destPort    bool
	share       bool
	enableRule  bool
	sourceGroup bool
	destGroup   bool
}

func newFirewallRuleUnsetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &fwRuleUnsetFlags{}
	cmd := &cobra.Command{
		Use:   "unset <firewall-rule>",
		Short: "Unset firewall rule properties",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			if f.share {
				fwDeprecated(cmd, `the --share option is deprecated; use "firewall group rule set --no-share" instead`)
			}
			if f.enableRule {
				fwDeprecated(cmd, `the --enable-rule option is deprecated; use "firewall group rule set --disable-rule" instead`)
			}
			ctx := cmd.Context()
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			return runFirewallRuleUnset(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&f.sourceIP, flagFWSourceIP, false, "clear the source IP address")
	fl.BoolVar(&f.destIP, flagFWDestIP, false, "clear the destination IP address")
	fl.BoolVar(&f.sourcePort, flagFWSourcePort, false, "clear the source port")
	fl.BoolVar(&f.destPort, flagFWDestPort, false, "clear the destination port")
	fl.BoolVar(&f.share, flagFWShare, false, `(deprecated, use "firewall group rule set --no-share") restrict the rule to its project`)
	fl.BoolVar(&f.enableRule, flagFWEnableRule, false, `(deprecated, use "firewall group rule set --disable-rule") disable the rule`)
	fl.BoolVar(&f.sourceGroup, flagFWSourceGroup, false, "clear the source firewall group")
	fl.BoolVar(&f.destGroup, flagFWDestGroup, false, "clear the destination firewall group")
	return cmd
}

func runFirewallRuleUnset(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, f *fwRuleUnsetFlags, w io.Writer) (err error) {
	defer func() { err = fwaasErr(ctx, client, err) }()
	attrs := map[string]any{}
	for _, c := range []struct {
		key string
		on  bool
	}{
		{"source_ip_address", f.sourceIP},
		{"source_port", f.sourcePort},
		{"destination_ip_address", f.destIP},
		{"destination_port", f.destPort},
		{"source_firewall_group_id", f.sourceGroup},
		{"destination_firewall_group_id", f.destGroup},
	} {
		if c.on {
			attrs[c.key] = nil
		}
	}
	if f.share {
		attrs["shared"] = false
	}
	if f.enableRule {
		attrs["enabled"] = false
	}
	if err := fwRequireChange(attrs, "firewall group rule unset"); err != nil {
		return err
	}
	return updateFWRule(ctx, client, o, ref, attrs, w)
}

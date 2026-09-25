package network

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/security/rules"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Flag names local to "security group rule". They live here rather than in
// flagnames.go because only this file uses them.
const (
	flagSGRuleIngress            = "ingress"
	flagSGRuleEgress             = "egress"
	flagSGRuleRemoteIP           = "remote-ip"
	flagSGRuleRemoteGroup        = "remote-group"
	flagSGRuleRemoteAddressGroup = "remote-address-group"
	flagSGRuleICMPType           = "icmp-type"
	flagSGRuleICMPCode           = "icmp-code"
	flagSGRuleDstPort            = "dst-port"
)

func newSecurityGroupRuleCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rule",
		Short: "Manage security group rules",
	}
	cmd.AddCommand(newSecurityGroupRuleListCommand(a, o))
	cmd.AddCommand(newSecurityGroupRuleShowCommand(a, o))
	cmd.AddCommand(newSecurityGroupRuleCreateCommand(a, o))
	cmd.AddCommand(newSecurityGroupRuleDeleteCommand(a, o))
	return cmd
}

func secGroupRuleShowFields(r *rules.SecGroupRule) ([]string, []any) {
	fields := []string{
		"id", "security_group_id", "direction", "ethertype", "protocol",
		"port_range_min", "port_range_max", "remote_ip_prefix", "remote_group_id",
		"remote_address_group_id", "description", "project_id", "revision_number",
		"created_at", "updated_at",
	}
	values := []any{
		r.ID, r.SecGroupID, r.Direction, r.EtherType, r.Protocol,
		r.PortRangeMin, r.PortRangeMax, r.RemoteIPPrefix, r.RemoteGroupID,
		r.RemoteAddressGroupID, r.Description, r.ProjectID, r.RevisionNumber,
		r.CreatedAt, r.UpdatedAt,
	}
	return fields, values
}

// secGroupRuleListFlags mirrors upstream ListSecurityGroupRule
// (network/v2/security_group_rule.py). --long is deprecated upstream and does
// nothing there either; it is accepted so scripts written for osc keep working.
type secGroupRuleListFlags struct {
	protocol      string
	ethertype     string
	ingress       bool
	egress        bool
	long          bool
	project       string
	projectDomain string
}

func newSecurityGroupRuleListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &secGroupRuleListFlags{}
	cmd := &cobra.Command{
		Use:   "list [<group>]",
		Short: "List security group rules, optionally for one group",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, session, err := newNetworkSession(ctx, a)
			if err != nil {
				return err
			}
			projectID, err := resolveProjectRef(ctx, session, f.project, f.projectDomain)
			if err != nil {
				return err
			}
			group := ""
			if len(args) == 1 {
				group = args[0]
			}
			return runSecurityGroupRuleList(ctx, client, o, group, f, projectID, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.protocol, "protocol", "", "list only rules with this IP protocol (name or number)")
	fl.StringVar(&f.ethertype, "ethertype", "", "list only rules with this ethertype (IPv4 or IPv6)")
	fl.BoolVar(&f.ingress, flagSGRuleIngress, false, "list only rules applied to incoming traffic")
	fl.BoolVar(&f.egress, flagSGRuleEgress, false, "list only rules applied to outgoing traffic")
	fl.BoolVar(&f.long, "long", false, "deprecated upstream and ignored; every column is always shown")
	fl.StringVar(&f.project, flagProject, "", "list only rules owned by this project (name or ID)")
	fl.StringVar(&f.projectDomain, flagProjectDomain, "", projectDomainHelp)
	cmd.MarkFlagsMutuallyExclusive(flagSGRuleIngress, flagSGRuleEgress)
	return cmd
}

// runSecurityGroupRuleList sends upstream's filters. Upstream parses --ethertype
// but never puts it in the query (a bug in osc 10.3.0); koc sends it as
// neutron's ethertype filter, which is what its help text promises.
func runSecurityGroupRuleList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	group string, f *secGroupRuleListFlags, projectID string, w io.Writer,
) error {
	opts := rules.ListOpts{ProjectID: projectID, Protocol: strings.ToLower(f.protocol)}
	if group != "" {
		gid, err := resolveSecGroupID(ctx, client, group)
		if err != nil {
			return err
		}
		opts.SecGroupID = gid
	}
	switch {
	case f.ingress:
		opts.Direction = string(rules.DirIngress)
	case f.egress:
		opts.Direction = string(rules.DirEgress)
	}
	if f.ethertype != "" {
		et, err := normalizeEtherType(f.ethertype)
		if err != nil {
			return err
		}
		opts.EtherType = string(et)
	}
	pages, err := rules.List(client, opts).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing security group rules: %w", err)
	}
	all, err := rules.ExtractRules(pages)
	if err != nil {
		return fmt.Errorf("parsing security group rule list: %w", err)
	}
	return o.WriteList(w, secGroupRuleListTable(all, group == ""))
}

// secGroupRuleListTable renders upstream's columns. The Security Group column
// appears only when the list is not already scoped to one group.
func secGroupRuleListTable(all []rules.SecGroupRule, withGroup bool) output.Table {
	cols := []string{
		"ID", "IP Protocol", "Ethertype", "IP Range", "Port Range", "Direction",
		"Remote Security Group", "Remote Address Group",
	}
	if withGroup {
		cols = append(cols, "Security Group")
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(all))}
	for i := range all {
		r := &all[i]
		row := []any{
			r.ID, r.Protocol, r.EtherType, remoteIPPrefixOrDefault(r), secGroupRulePortRange(r), r.Direction,
			r.RemoteGroupID, r.RemoteAddressGroupID,
		}
		if withGroup {
			row = append(row, r.SecGroupID)
		}
		t.Rows = append(t.Rows, row)
	}
	return t
}

// secGroupRulePortRange is upstream's format_network_port_range: an ICMP rule
// shows "type=N[:code=M]", any other rule "min:max" (a single port as "80:80"),
// and a rule with no range an empty cell.
func secGroupRulePortRange(r *rules.SecGroupRule) string {
	if isICMPProtocol(r.Protocol) {
		s := ""
		if r.PortRangeMin != 0 {
			s += "type=" + strconv.Itoa(r.PortRangeMin)
		}
		if r.PortRangeMax != 0 {
			s += ":code=" + strconv.Itoa(r.PortRangeMax)
		}
		return s
	}
	lo, hi := r.PortRangeMin, r.PortRangeMax
	if lo == 0 && hi == 0 {
		return ""
	}
	if lo == 0 {
		lo = hi
	}
	if hi == 0 {
		hi = lo
	}
	return fmt.Sprintf("%d:%d", lo, hi)
}

// remoteIPPrefixOrDefault is upstream's format_remote_ip_prefix: a rule with no
// remote prefix matches everything of its ethertype, so show that.
func remoteIPPrefixOrDefault(r *rules.SecGroupRule) string {
	if r.RemoteIPPrefix != "" {
		return r.RemoteIPPrefix
	}
	return defaultRemoteIPPrefix(rules.RuleEtherType(r.EtherType))
}

func defaultRemoteIPPrefix(et rules.RuleEtherType) string {
	switch et {
	case rules.EtherType4:
		return "0.0.0.0/0"
	case rules.EtherType6:
		return "::/0"
	default:
		return ""
	}
}

func newSecurityGroupRuleShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <rule>",
		Short: "Show details of a security group rule",
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
			return runSecurityGroupRuleShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
	return cmd
}

func runSecurityGroupRuleShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, id string, w io.Writer) error {
	r, err := rules.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("getting security group rule %s: %w", id, err)
	}
	// Upstream fills an absent remote prefix with its ethertype's match-all.
	r.RemoteIPPrefix = remoteIPPrefixOrDefault(r)
	fields, values := secGroupRuleShowFields(r)
	return o.WriteSingle(w, fields, values)
}

// secGroupRuleCreateFlags mirrors upstream CreateSecurityGroupRule.
// icmpType/icmpCode are non-nil only when the flag was given (RunE sets them),
// since 0 is a meaningful ICMP type and code.
type secGroupRuleCreateFlags struct {
	protocol           string
	ingress            bool
	egress             bool
	dstPort            string
	remoteIP           string
	ethertype          string
	remoteGroup        string
	remoteAddressGroup string
	description        string
	icmpTypeVal        int
	icmpCodeVal        int
	icmpType           *int
	icmpCode           *int
	project            string
	projectDomain      string
	// projectID is --project resolved by RunE, so the seam needs no identity client.
	projectID     string
	extraProperty []string
}

func newSecurityGroupRuleCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &secGroupRuleCreateFlags{}
	cmd := &cobra.Command{
		Use:   "create <group>",
		Short: "Create a new security group rule",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			fl := cmd.Flags()
			if fl.Changed(flagSGRuleICMPType) {
				f.icmpType = &f.icmpTypeVal
			}
			if fl.Changed(flagSGRuleICMPCode) {
				f.icmpCode = &f.icmpCodeVal
			}
			ctx := cmd.Context()
			client, session, err := newNetworkSession(ctx, a)
			if err != nil {
				return err
			}
			if f.projectID, err = resolveProjectRef(ctx, session, f.project, f.projectDomain); err != nil {
				return err
			}
			return runSecurityGroupRuleCreate(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.protocol, "protocol", "", "IP protocol (tcp, udp, icmp, ..., a number 0-255, or any; default any)")
	fl.BoolVar(&f.ingress, flagSGRuleIngress, false, "rule applies to incoming traffic (default)")
	fl.BoolVar(&f.egress, flagSGRuleEgress, false, "rule applies to outgoing traffic")
	fl.StringVar(&f.dstPort, flagSGRuleDstPort, "", "destination port or range (e.g. 80 or 8000:9000); ignored for ICMP")
	fl.StringVar(&f.remoteIP, flagSGRuleRemoteIP, "", "remote IP prefix (CIDR) to match (default 0.0.0.0/0 or ::/0)")
	fl.StringVar(&f.ethertype, "ethertype", "", "IPv4 or IPv6 (inferred from --remote-ip/--protocol when unset, else IPv4)")
	fl.StringVar(&f.remoteGroup, flagSGRuleRemoteGroup, "", "remote security group (name or ID) to match")
	fl.StringVar(&f.remoteAddressGroup, flagSGRuleRemoteAddressGroup, "", "remote address group (name or ID) to match")
	fl.StringVar(&f.description, flagDescription, "", "description for the rule")
	fl.IntVar(&f.icmpTypeVal, flagSGRuleICMPType, 0, "ICMP type, for an ICMP protocol (sent as port_range_min)")
	fl.IntVar(&f.icmpCodeVal, flagSGRuleICMPCode, 0, "ICMP code, for an ICMP protocol; needs --icmp-type (sent as port_range_max)")
	fl.StringVar(&f.project, flagProject, "", "owner's project (name or ID; admin)")
	fl.StringVar(&f.projectDomain, flagProjectDomain, "", projectDomainHelp)
	bindExtraPropertyFlag(fl, &f.extraProperty)
	cmd.MarkFlagsMutuallyExclusive(flagSGRuleIngress, flagSGRuleEgress)
	cmd.MarkFlagsMutuallyExclusive(flagSGRuleRemoteIP, flagSGRuleRemoteGroup, flagSGRuleRemoteAddressGroup)
	return cmd
}

func runSecurityGroupRuleCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, groupArg string, f *secGroupRuleCreateFlags, w io.Writer) error {
	opts, attrs, err := buildSecGroupRuleCreate(f)
	if err != nil {
		return err
	}
	if opts.SecGroupID, err = resolveSecGroupID(ctx, client, groupArg); err != nil {
		return err
	}
	switch {
	case f.remoteGroup != "":
		if opts.RemoteGroupID, err = resolveSecGroupID(ctx, client, f.remoteGroup); err != nil {
			return err
		}
	case f.remoteAddressGroup != "":
		if opts.RemoteAddressGroupID, err = resolveAddressGroupID(ctx, client, f.remoteAddressGroup); err != nil {
			return err
		}
	case f.remoteIP != "":
		opts.RemoteIPPrefix = f.remoteIP
	default:
		// Upstream spells the match-all out rather than leaving it null.
		opts.RemoteIPPrefix = defaultRemoteIPPrefix(opts.EtherType)
	}
	extra, err := parseExtraProperties(f.extraProperty, false)
	if err != nil {
		return err
	}
	attrs = mergeAttrs(attrs, extra)
	r, err := rules.Create(ctx, client, withSecGroupRuleCreateAttrs(opts, attrs)).Extract()
	if err != nil {
		return fmt.Errorf("creating security group rule: %w", err)
	}
	fields, values := secGroupRuleShowFields(r)
	return o.WriteSingle(w, fields, values)
}

// buildSecGroupRuleCreate derives the request from the flags that need no
// lookup: direction, protocol, ethertype, description, owner and the port
// range. The port range travels in attrs rather than the typed opts because
// CreateOpts drops a zero port_range_min/max, and ICMP type 0 (echo reply) and
// code 0 are real values upstream sends.
func buildSecGroupRuleCreate(f *secGroupRuleCreateFlags) (rules.CreateOpts, map[string]any, error) {
	opts := rules.CreateOpts{
		Direction:   rules.DirIngress,
		Protocol:    normalizeProtocol(f.protocol),
		Description: f.description,
		ProjectID:   f.projectID,
	}
	if f.egress {
		opts.Direction = rules.DirEgress
	}
	if f.ethertype != "" {
		et, err := normalizeEtherType(f.ethertype)
		if err != nil {
			return opts, nil, err
		}
		opts.EtherType = et
	} else {
		opts.EtherType = inferEtherType(f.remoteIP, string(opts.Protocol))
	}
	attrs, err := secGroupRulePortAttrs(f, string(opts.Protocol))
	return opts, attrs, err
}

// secGroupRulePortAttrs is upstream's --dst-port / --icmp-type / --icmp-code
// handling: the ICMP pair maps onto port_range_min/max and needs an ICMP
// protocol, it cannot be combined with --dst-port, and --dst-port is ignored
// for an ICMP protocol. A negative type or code is accepted and not sent, as
// upstream does. koc treats a given 0 as given where upstream's truthiness test
// lets "--icmp-type 0" slip past the --dst-port and protocol checks.
func secGroupRulePortAttrs(f *secGroupRuleCreateFlags, protocol string) (map[string]any, error) {
	icmpGiven := f.icmpType != nil || f.icmpCode != nil
	if f.dstPort != "" && icmpGiven {
		return nil, fmt.Errorf("--%s cannot be combined with --%s or --%s", flagSGRuleDstPort, flagSGRuleICMPType, flagSGRuleICMPCode)
	}
	if f.icmpType == nil && f.icmpCode != nil {
		return nil, fmt.Errorf("--%s requires --%s", flagSGRuleICMPCode, flagSGRuleICMPType)
	}
	icmp := isICMPProtocol(protocol)
	if icmpGiven && !icmp {
		return nil, fmt.Errorf("--%s and --%s need an ICMP --protocol (icmp, ipv6-icmp)", flagSGRuleICMPType, flagSGRuleICMPCode)
	}
	attrs := map[string]any{}
	if f.dstPort != "" && !icmp {
		lo, hi, err := parsePortRange(f.dstPort)
		if err != nil {
			return nil, err
		}
		attrs["port_range_min"], attrs["port_range_max"] = lo, hi
	}
	if f.icmpType != nil && *f.icmpType >= 0 {
		attrs["port_range_min"] = *f.icmpType
	}
	if f.icmpCode != nil && *f.icmpCode >= 0 {
		attrs["port_range_max"] = *f.icmpCode
	}
	if len(attrs) == 0 {
		return nil, nil
	}
	return attrs, nil
}

// normalizeEtherType maps a case-insensitive ethertype (e.g. "ipv6", "IPV4")
// onto neutron's canonical "IPv4"/"IPv6" spelling. Empty values are handled by
// the caller (the default applies).
func normalizeEtherType(v string) (rules.RuleEtherType, error) {
	switch strings.ToLower(v) {
	case "ipv4":
		return rules.EtherType4, nil
	case "ipv6":
		return rules.EtherType6, nil
	default:
		return "", fmt.Errorf("invalid --ethertype %q: want IPv4 or IPv6", v)
	}
}

// inferEtherType picks a default ethertype when --ethertype was not given. An
// IPv6-only protocol (upstream's is_ipv6_protocol: ipv6-*, icmpv6, or protocol
// numbers 41, 43, 44, 58, 59, 60) implies IPv6, as upstream does; koc also
// infers IPv6 from an IPv6 --remote-ip, which upstream would send as IPv4 and
// neutron would reject. Everything else is IPv4.
func inferEtherType(remoteIP, protocol string) rules.RuleEtherType {
	if strings.Contains(remoteIP, ":") {
		return rules.EtherType6
	}
	if strings.HasPrefix(protocol, "ipv6-") ||
		slices.Contains([]string{"icmpv6", "41", "43", "44", "58", "59", "60"}, protocol) {
		return rules.EtherType6
	}
	return rules.EtherType4
}

// isICMPProtocol is upstream's is_icmp_protocol: ICMP by name (including
// neutron's deprecated icmpv6) or number.
func isICMPProtocol(protocol string) bool {
	return slices.Contains([]string{"icmp", "icmpv6", "ipv6-icmp", "1", "58"}, protocol)
}

// normalizeProtocol lowercases the protocol so values like "TCP" become the
// "tcp" neutron expects. "any" means every protocol, which neutron spells as
// no protocol at all (upstream's get_protocol); empty stays empty.
func normalizeProtocol(v string) rules.RuleProtocol {
	p := strings.ToLower(v)
	if p == "any" {
		return rules.ProtocolAny
	}
	return rules.RuleProtocol(p)
}

func newSecurityGroupRuleDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <rule> [<rule> ...]",
		Short: "Delete security group rule(s)",
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
			return runSecurityGroupRuleDelete(ctx, client, args, cmd.OutOrStdout())
		},
	}
	return cmd
}

func runSecurityGroupRuleDelete(ctx context.Context, client *gophercloud.ServiceClient, ids []string, w io.Writer) error {
	return batchdelete.Each(ids, func(id string) error {
		if err := rules.Delete(ctx, client, id).ExtractErr(); err != nil {
			return fmt.Errorf("deleting security group rule %s: %w", id, err)
		}
		if _, err := fmt.Fprintf(w, "Deleted security group rule %s\n", id); err != nil {
			return err
		}
		return nil
	})
}

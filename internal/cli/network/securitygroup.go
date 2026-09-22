package network

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/security/groups"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/security/rules"
	"github.com/gophercloud/gophercloud/v2/pagination"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/allprojects"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// newSecurityGroupCommand builds the "group" child of the "security" parent,
// giving the two-word OSC command "security group ...". The rule subtree is
// nested as "security group rule ...".
func newSecurityGroupCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "group",
		Short: "Manage security groups",
	}
	cmd.AddCommand(newSecurityGroupListCommand(a, o))
	cmd.AddCommand(newSecurityGroupShowCommand(a, o))
	cmd.AddCommand(newSecurityGroupCreateCommand(a, o))
	cmd.AddCommand(newSecurityGroupDeleteCommand(a, o))
	cmd.AddCommand(newSecurityGroupSetCommand(a, o))
	cmd.AddCommand(newSecurityGroupRuleCommand(a, o))
	return cmd
}

func secGroupShowFields(g *groups.SecGroup) ([]string, []any) {
	fields := []string{"id", "name", "description", "stateful", "project_id", "tags", "created_at", "updated_at"}
	values := []any{g.ID, g.Name, g.Description, g.Stateful, g.ProjectID, g.Tags, g.CreatedAt, g.UpdatedAt}
	return fields, values
}

// secGroupListFlags holds the filters accepted by "security group list".
// Upstream OSC (network/v2/security_group.py ListSecurityGroup) takes --project,
// --project-domain and the tag filters; --name and --all-projects are
// koc-native additions (see allProjectsNetworkList for the latter). --name is a
// plain neutron query filter, so it is exact-match and server-side.
type secGroupListFlags struct {
	name          string
	project       string
	projectDomain string
	tags          []string
	anyTags       []string
	notTags       []string
	notAnyTags    []string
	share         bool
	noShare       bool
	allProjects   bool

	shared *bool
}

func newSecurityGroupListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &secGroupListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List security groups",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			fl := cmd.Flags()
			if err := mutuallyExclusive(fl, flagShare, flagNoShare); err != nil {
				return err
			}
			f.shared = enableDisable(fl, f.share, f.noShare, flagShare, flagNoShare)
			ctx := cmd.Context()
			client, session, err := newNetworkSession(ctx, a)
			if err != nil {
				return err
			}
			projectID, err := resolveProjectRef(ctx, session, f.project, f.projectDomain)
			if err != nil {
				return err
			}
			return runSecurityGroupList(ctx, client, o, f, projectID, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "list only security groups with this name")
	fl.StringVar(&f.project, "project", "", "list only security groups owned by this project (name or ID)")
	fl.StringVar(&f.projectDomain, "project-domain", "", "domain owning --project, to disambiguate the name (name or ID)")
	fl.StringSliceVar(&f.tags, "tags", nil, "list only security groups with all of these tags (comma-separated)")
	fl.StringSliceVar(&f.anyTags, "any-tags", nil, "list only security groups with any of these tags (comma-separated)")
	fl.StringSliceVar(&f.notTags, "not-tags", nil, "exclude security groups with all of these tags (comma-separated)")
	fl.StringSliceVar(&f.notAnyTags, "not-any-tags", nil, "exclude security groups with any of these tags (comma-separated)")
	fl.BoolVar(&f.share, flagShare, false, "list only security groups shared between projects")
	fl.BoolVar(&f.noShare, flagNoShare, false, "list only security groups not shared between projects")
	allprojects.Bind(cmd, &f.allProjects, allProjectsNetworkList)
	cmd.MarkFlagsMutuallyExclusive("project", "all-projects")
	return cmd
}

// SecGroupSharedAttr is neutron's `shared` attribute of a security group (the
// security-groups-shared-filtering extension), which gophercloud's SecGroup
// does not model. A pointer, so a cloud without the extension renders an empty
// cell rather than a misleading False.
type SecGroupSharedAttr struct {
	Shared *bool `json:"shared"`
}

// secGroupListRow is a SecGroup plus its shared attribute. Both are anonymous
// embeds so gophercloud's ExtractIntoSlicePtr decodes each separately —
// SecGroup's own UnmarshalJSON would otherwise swallow the extension field. The embed is
// exported because gophercloud reflects into each embedded struct, and
// reflect cannot read an unexported one (it panics).
type secGroupListRow struct {
	groups.SecGroup
	SecGroupSharedAttr
}

func runSecurityGroupList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	f *secGroupListFlags, projectID string, w io.Writer,
) error {
	opts := groups.ListOpts{
		Name:       f.name,
		ProjectID:  projectID,
		Tags:       strings.Join(f.tags, ","),
		TagsAny:    strings.Join(f.anyTags, ","),
		NotTags:    strings.Join(f.notTags, ","),
		NotTagsAny: strings.Join(f.notAnyTags, ","),
	}
	all, err := listSecGroups(ctx, client, opts, f.shared)
	if err != nil {
		return err
	}
	t := output.Table{
		Columns: []string{"ID", "Name", "Description", "Project", "Tags", "Shared"},
		Rows:    make([][]any, 0, len(all)),
	}
	for _, g := range all {
		t.Rows = append(t.Rows, []any{g.ID, g.Name, g.Description, g.ProjectID, g.Tags, derefOrNil(g.Shared)})
	}
	return o.WriteList(w, t)
}

// listSecGroups is groups.List with a `shared` filter. gophercloud's List takes
// the concrete ListOpts (no builder interface) and has no shared field, and
// there is no ExtractGroupsInto, so the pager is built here over the same URL
// and page type. Replace it once gophercloud models the attribute.
func listSecGroups(ctx context.Context, client *gophercloud.ServiceClient, opts groups.ListOpts, shared *bool) ([]secGroupListRow, error) {
	q, err := gophercloud.BuildQueryString(&opts)
	if err != nil {
		return nil, fmt.Errorf("building security group query: %w", err)
	}
	params := q.Query()
	if shared != nil {
		params.Set("shared", strconv.FormatBool(*shared))
	}
	u := client.ServiceURL("security-groups")
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	pages, err := pagination.NewPager(client, u, func(r pagination.PageResult) pagination.Page {
		return groups.SecGroupPage{LinkedPageBase: pagination.LinkedPageBase{PageResult: r}}
	}).AllPages(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing security groups: %w", err)
	}
	page, ok := pages.(groups.SecGroupPage)
	if !ok {
		return nil, fmt.Errorf("parsing security group list: unexpected page type %T", pages)
	}
	var all []secGroupListRow
	if err := page.ExtractIntoSlicePtr(&all, "security_groups"); err != nil {
		return nil, fmt.Errorf("parsing security group list: %w", err)
	}
	return all, nil
}

func newSecurityGroupShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <group>",
		Short: "Show details of a security group",
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
			return runSecurityGroupShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
	return cmd
}

func runSecurityGroupShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, nameOrID string, w io.Writer) error {
	id, err := resolveSecGroupID(ctx, client, nameOrID)
	if err != nil {
		return err
	}
	g, err := groups.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("getting security group %s: %w", nameOrID, err)
	}
	fields, values := secGroupShowFields(g)
	return o.WriteSingle(w, fields, values)
}

func newSecurityGroupCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var description string
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new security group",
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
			return runSecurityGroupCreate(ctx, client, o, args[0], description, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&description, "description", "", "description for the security group")
	return cmd
}

func runSecurityGroupCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, name, description string, w io.Writer) error {
	g, err := groups.Create(ctx, client, groups.CreateOpts{Name: name, Description: description}).Extract()
	if err != nil {
		return fmt.Errorf("creating security group: %w", err)
	}
	fields, values := secGroupShowFields(g)
	return o.WriteSingle(w, fields, values)
}

func newSecurityGroupDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <group> [<group> ...]",
		Short: "Delete security group(s)",
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
			return runSecurityGroupDelete(ctx, client, args, cmd.OutOrStdout())
		},
	}
	return cmd
}

func runSecurityGroupDelete(ctx context.Context, client *gophercloud.ServiceClient, names []string, w io.Writer) error {
	return batchdelete.Each(names, func(nameOrID string) error {
		id, err := resolveSecGroupID(ctx, client, nameOrID)
		if err != nil {
			return err
		}
		if err := groups.Delete(ctx, client, id).ExtractErr(); err != nil {
			return fmt.Errorf("deleting security group %s: %w", nameOrID, err)
		}
		if _, err := fmt.Fprintf(w, "Deleted security group %s\n", nameOrID); err != nil {
			return err
		}
		return nil
	})
}

type secGroupSetFlags struct {
	name        string
	description string
}

func newSecurityGroupSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &secGroupSetFlags{}
	cmd := &cobra.Command{
		Use:   "set <group>",
		Short: "Set security group properties",
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
			return runSecurityGroupSet(ctx, client, o, args[0], f, cmd.Flags(), cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "new security group name")
	fl.StringVar(&f.description, "description", "", "new description")
	return cmd
}

func runSecurityGroupSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, nameOrID string, f *secGroupSetFlags, flags flagSet, w io.Writer) error {
	id, err := resolveSecGroupID(ctx, client, nameOrID)
	if err != nil {
		return err
	}
	opts := groups.UpdateOpts{}
	changed := false
	if f.name != "" {
		opts.Name = f.name
		changed = true
	}
	if flags.Changed("description") {
		opts.Description = &f.description
		changed = true
	}
	if !changed {
		return fmt.Errorf("security group set requires at least one attribute flag")
	}
	g, err := groups.Update(ctx, client, id, opts).Extract()
	if err != nil {
		return fmt.Errorf("updating security group %s: %w", nameOrID, err)
	}
	fields, values := secGroupShowFields(g)
	return o.WriteSingle(w, fields, values)
}

// --- security group rule ---

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
		"description", "project_id", "created_at",
	}
	values := []any{
		r.ID, r.SecGroupID, r.Direction, r.EtherType, r.Protocol,
		r.PortRangeMin, r.PortRangeMax, r.RemoteIPPrefix, r.RemoteGroupID,
		r.Description, r.ProjectID, r.CreatedAt,
	}
	return fields, values
}

func newSecurityGroupRuleListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list [<group>]",
		Short: "List security group rules, optionally for one group",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			group := ""
			if len(args) == 1 {
				group = args[0]
			}
			return runSecurityGroupRuleList(ctx, client, o, group, cmd.OutOrStdout())
		},
	}
	return cmd
}

func runSecurityGroupRuleList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, group string, w io.Writer) error {
	opts := rules.ListOpts{}
	if group != "" {
		gid, err := resolveSecGroupID(ctx, client, group)
		if err != nil {
			return err
		}
		opts.SecGroupID = gid
	}
	pages, err := rules.List(client, opts).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing security group rules: %w", err)
	}
	all, err := rules.ExtractRules(pages)
	if err != nil {
		return fmt.Errorf("parsing security group rule list: %w", err)
	}
	t := output.Table{Columns: []string{"ID", "Direction", "Ether Type", "Protocol", "Port Range", "Remote IP Prefix", "Remote Group", "Security Group"}, Rows: make([][]any, 0, len(all))}
	for _, r := range all {
		t.Rows = append(t.Rows, []any{r.ID, r.Direction, r.EtherType, r.Protocol, portRangeString(r.PortRangeMin, r.PortRangeMax), r.RemoteIPPrefix, r.RemoteGroupID, r.SecGroupID})
	}
	return o.WriteList(w, t)
}

func portRangeString(lo, hi int) string {
	if lo == 0 && hi == 0 {
		return ""
	}
	if lo == hi {
		return fmt.Sprintf("%d", lo)
	}
	return fmt.Sprintf("%d:%d", lo, hi)
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
	fields, values := secGroupRuleShowFields(r)
	return o.WriteSingle(w, fields, values)
}

type secGroupRuleCreateFlags struct {
	protocol    string
	ingress     bool
	egress      bool
	dstPort     string
	remoteIP    string
	ethertype   string
	remoteGroup string
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
			if err := mutuallyExclusive(cmd.Flags(), "ingress", "egress"); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			return runSecurityGroupRuleCreate(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.protocol, "protocol", "", "IP protocol (tcp, udp, icmp, ...)")
	fl.BoolVar(&f.ingress, "ingress", false, "rule applies to incoming traffic (default)")
	fl.BoolVar(&f.egress, "egress", false, "rule applies to outgoing traffic")
	fl.StringVar(&f.dstPort, "dst-port", "", "destination port or range (e.g. 80 or 8000:9000)")
	fl.StringVar(&f.remoteIP, "remote-ip", "", "remote IP prefix (CIDR) to match")
	fl.StringVar(&f.ethertype, "ethertype", "", "IPv4 or IPv6 (inferred from --remote-ip/--protocol when unset, else IPv4)")
	fl.StringVar(&f.remoteGroup, "remote-group", "", "remote security group (name or ID) to match")
	cmd.MarkFlagsMutuallyExclusive("remote-ip", "remote-group")
	return cmd
}

func runSecurityGroupRuleCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, groupArg string, f *secGroupRuleCreateFlags, w io.Writer) error {
	gid, err := resolveSecGroupID(ctx, client, groupArg)
	if err != nil {
		return err
	}
	direction := rules.DirIngress
	if f.egress {
		direction = rules.DirEgress
	}
	var etherType rules.RuleEtherType
	if f.ethertype != "" {
		normalized, err := normalizeEtherType(f.ethertype)
		if err != nil {
			return err
		}
		etherType = normalized
	} else {
		etherType = inferEtherType(f.remoteIP, f.protocol)
	}
	opts := rules.CreateOpts{
		Direction:      direction,
		EtherType:      etherType,
		SecGroupID:     gid,
		Protocol:       normalizeProtocol(f.protocol),
		RemoteIPPrefix: f.remoteIP,
	}
	if f.dstPort != "" {
		lo, hi, err := parsePortRange(f.dstPort)
		if err != nil {
			return err
		}
		opts.PortRangeMin = lo
		opts.PortRangeMax = hi
	}
	if f.remoteGroup != "" {
		rgid, err := resolveSecGroupID(ctx, client, f.remoteGroup)
		if err != nil {
			return err
		}
		opts.RemoteGroupID = rgid
	}
	r, err := rules.Create(ctx, client, opts).Extract()
	if err != nil {
		return fmt.Errorf("creating security group rule: %w", err)
	}
	fields, values := secGroupRuleShowFields(r)
	return o.WriteSingle(w, fields, values)
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
// IPv6 remote CIDR (contains ':') or an IPv6-specific protocol
// (icmpv6/ipv6-*) implies IPv6; everything else defaults to IPv4, matching the
// upstream OSC behavior.
func inferEtherType(remoteIP, protocol string) rules.RuleEtherType {
	if strings.Contains(remoteIP, ":") {
		return rules.EtherType6
	}
	p := strings.ToLower(protocol)
	if p == "icmpv6" || strings.HasPrefix(p, "ipv6-") {
		return rules.EtherType6
	}
	return rules.EtherType4
}

// normalizeProtocol lowercases the protocol so values like "TCP" become the
// "tcp" neutron expects. Empty values are left untouched (no protocol set).
func normalizeProtocol(v string) rules.RuleProtocol {
	if v == "" {
		return ""
	}
	return rules.RuleProtocol(strings.ToLower(v))
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

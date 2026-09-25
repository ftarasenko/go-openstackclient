package network

import (
	"context"
	"fmt"
	"io"
	"slices"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/fwaas_v2/groups"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/fwaas_v2/policies"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/fwaas_v2/rules"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Literals repeated within this file.
const (
	flagFWIngressPolicy = "ingress-firewall-policy"
	flagFWEgressPolicy  = "egress-firewall-policy"
)

// FWaaS v2 (upstream network/v2/fwaas/{group,policy,rule}.py): "firewall group",
// "firewall group policy" and "firewall group rule". Every call goes through the
// fwaas_v2 plugin's URL tree, so each seam routes its error through
// explainMissingService — a cloud without the plugin answers 404 for everything.

// Flag names shared by the three FWaaS nouns (batch-specific, see flagnames.go).
const (
	flagFWShare   = "share"
	flagFWNoShare = "no-share"
	flagFWName    = "name"
	flagFWPort    = "port"
	flagFWNoPort  = "no-port"
)

// newFirewallCommands returns the top-level "firewall" noun. "firewall group"
// is both a noun with its own verbs and the parent of "policy" and "rule",
// exactly as upstream spells the commands.
func newFirewallCommands(a *auth.Options, o *output.Options) []*cobra.Command {
	firewall := &cobra.Command{
		Use:   "firewall",
		Short: "Manage FWaaS v2 firewall groups, policies and rules (requires the fwaas_v2 extension)",
	}
	group := &cobra.Command{
		Use:   "group",
		Short: "Manage firewall groups (requires the fwaas_v2 extension)",
	}
	group.AddCommand(
		newFirewallGroupCreateCommand(a, o),
		newFirewallGroupDeleteCommand(a, o),
		newFirewallGroupListCommand(a, o),
		newFirewallGroupSetCommand(a, o),
		newFirewallGroupShowCommand(a, o),
		newFirewallGroupUnsetCommand(a, o),
		newFirewallPolicyCommand(a, o),
		newFirewallRuleCommand(a, o),
	)
	firewall.AddCommand(group)
	return []*cobra.Command{firewall}
}

// fwaasErr names the missing plugin on a 404. Seams defer it over their named
// error so every endpoint they touch — lookups included — is covered.
func fwaasErr(ctx context.Context, client *gophercloud.ServiceClient, err error) error {
	return explainMissingService(ctx, client, err, extFWaaSv2)
}

// The typed fwaas_v2 opts cannot express what upstream sends: their update
// structs omit empty values, so `--no-ingress-firewall-policy` (null),
// `--no-port` ([]) and `--no-source-port` (null) are unreachable, the rule
// create insists on protocol and action, and the rule structs lack
// source/destination_firewall_group_id. None of them carries a RevisionNumber,
// so these small raw-body builders replace them outright: each wraps the
// attribute map upstream would send under the resource key and satisfies the
// package's builder interface.
type (
	fwGroupBody  map[string]any
	fwPolicyBody map[string]any
	fwRuleBody   map[string]any
)

func fwWrap(key string, attrs map[string]any) (map[string]any, error) {
	if attrs == nil {
		attrs = map[string]any{}
	}
	return map[string]any{key: attrs}, nil
}

func (b fwGroupBody) ToFirewallGroupCreateMap() (map[string]any, error) {
	return fwWrap("firewall_group", b)
}

func (b fwGroupBody) ToFirewallGroupUpdateMap() (map[string]any, error) {
	return fwWrap("firewall_group", b)
}

func (b fwPolicyBody) ToFirewallPolicyCreateMap() (map[string]any, error) {
	return fwWrap("firewall_policy", b)
}

func (b fwPolicyBody) ToFirewallPolicyUpdateMap() (map[string]any, error) {
	return fwWrap("firewall_policy", b)
}

func (b fwRuleBody) ToRuleCreateMap() (map[string]any, error) { return fwWrap("firewall_rule", b) }

func (b fwRuleBody) ToRuleUpdateMap() (map[string]any, error) { return fwWrap("firewall_rule", b) }

// resolveFirewallGroupID, resolveFirewallPolicyID and resolveFirewallRuleID
// resolve a name or ID through resolveByName: a UUID passes through, otherwise
// exactly one name match wins.
func resolveFirewallGroupID(ctx context.Context, client *gophercloud.ServiceClient, nameOrID string) (string, error) {
	return resolveByName(client, "firewall group", nameOrID, func(c *gophercloud.ServiceClient) ([]groups.Group, error) {
		pages, err := groups.List(c, groups.ListOpts{Name: nameOrID}).AllPages(ctx)
		if err != nil {
			return nil, err
		}
		return groups.ExtractGroups(pages)
	}, func(g groups.Group) string { return g.ID })
}

func resolveFirewallPolicyID(ctx context.Context, client *gophercloud.ServiceClient, nameOrID string) (string, error) {
	return resolveByName(client, "firewall policy", nameOrID, func(c *gophercloud.ServiceClient) ([]policies.Policy, error) {
		pages, err := policies.List(c, policies.ListOpts{Name: nameOrID}).AllPages(ctx)
		if err != nil {
			return nil, err
		}
		return policies.ExtractPolicies(pages)
	}, func(p policies.Policy) string { return p.ID })
}

func resolveFirewallRuleID(ctx context.Context, client *gophercloud.ServiceClient, nameOrID string) (string, error) {
	return resolveByName(client, "firewall rule", nameOrID, func(c *gophercloud.ServiceClient) ([]rules.Rule, error) {
		pages, err := rules.List(c, rules.ListOpts{Name: nameOrID}).AllPages(ctx)
		if err != nil {
			return nil, err
		}
		return rules.ExtractRules(pages)
	}, func(r rules.Rule) string { return r.ID })
}

// fwCreateName reconciles upstream's optional positional <name> with its
// deprecated --name option on "firewall group create" / "firewall group rule
// create": both at once is an error, --name alone warns.
func fwCreateName(cmd *cobra.Command, args []string, flagValue, verb string) (string, error) {
	positional := ""
	if len(args) > 0 {
		positional = args[0]
	}
	if positional != "" && flagValue != "" {
		return "", fmt.Errorf("cannot specify name as both a positional argument and with the --name option")
	}
	if flagValue != "" {
		fwDeprecated(cmd, fmt.Sprintf("the --name option of %q is deprecated; pass the name as a positional argument instead", verb))
		return flagValue, nil
	}
	return positional, nil
}

// fwDeprecated prints upstream's deprecation warning for an option that still
// works. pflag's MarkDeprecated would hide the flag and its message lands in a
// buffer cobra only prints on a parse error, so the warning is written here.
func fwDeprecated(cmd *cobra.Command, msg string) {
	_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "warning: "+msg)
}

// setBoolPair records an on/off flag pair: on sends true, off sends false,
// neither leaves the attribute out.
func setBoolPair(attrs map[string]any, key string, on, off bool) {
	switch {
	case on:
		attrs[key] = true
	case off:
		attrs[key] = false
	}
}

// fwRequireChange is koc's guard for a set/unset given nothing to change.
func fwRequireChange(attrs map[string]any, verb string) error {
	if len(attrs) == 0 {
		return fmt.Errorf("%s requires at least one attribute flag", verb)
	}
	return nil
}

// --- firewall group -----------------------------------------------------------

// fwGroupFlags carries the create/set options (upstream _get_common_parser plus
// the per-verb --name/--port/--no-port/--project).
type fwGroupFlags struct {
	name          string
	description   string
	ingress       string
	noIngress     bool
	egress        string
	noEgress      bool
	share         bool
	noShare       bool
	enable        bool
	disable       bool
	ports         []string
	noPort        bool
	project       string
	projectDomain string
	// projectID is --project resolved by RunE, so the seam needs no identity client.
	projectID string
}

func bindFWGroupCommonFlags(cmd *cobra.Command, f *fwGroupFlags) {
	fl := cmd.Flags()
	fl.StringVar(&f.description, flagDescription, "", "description of the firewall group")
	fl.StringVar(&f.ingress, flagFWIngressPolicy, "", "ingress firewall policy (name or ID)")
	fl.BoolVar(&f.noIngress, "no-ingress-firewall-policy", false, "detach the ingress firewall policy")
	fl.StringVar(&f.egress, flagFWEgressPolicy, "", "egress firewall policy (name or ID)")
	fl.BoolVar(&f.noEgress, "no-egress-firewall-policy", false, "detach the egress firewall policy")
	fl.BoolVar(&f.share, flagFWShare, false, "share the firewall group with all projects")
	fl.BoolVar(&f.noShare, flagFWNoShare, false, "restrict the firewall group to its project")
	fl.BoolVar(&f.enable, "enable", false, "enable the firewall group")
	fl.BoolVar(&f.disable, "disable", false, "disable the firewall group")
	fl.StringArrayVar(&f.ports, flagFWPort, nil, "port to apply the firewall group to (name or ID, repeatable)")
	fl.BoolVar(&f.noPort, flagFWNoPort, false, "detach every port from the firewall group")
	cmd.MarkFlagsMutuallyExclusive(flagFWIngressPolicy, "no-ingress-firewall-policy")
	cmd.MarkFlagsMutuallyExclusive(flagFWEgressPolicy, "no-egress-firewall-policy")
	cmd.MarkFlagsMutuallyExclusive(flagFWShare, flagFWNoShare)
	cmd.MarkFlagsMutuallyExclusive("enable", "disable")
}

// buildFWGroupAttrs is upstream group._get_common_attrs. groupID is "" on
// create; on set, --port without --no-port adds to the group's current ports.
func buildFWGroupAttrs(ctx context.Context, client *gophercloud.ServiceClient, f *fwGroupFlags, groupID string) (map[string]any, error) {
	attrs := map[string]any{}
	for _, p := range []struct {
		key, ref string
		clear    bool
	}{
		{"ingress_firewall_policy_id", f.ingress, f.noIngress},
		{"egress_firewall_policy_id", f.egress, f.noEgress},
	} {
		switch {
		case p.ref != "":
			id, err := resolveFirewallPolicyID(ctx, client, p.ref)
			if err != nil {
				return nil, err
			}
			attrs[p.key] = id
		case p.clear:
			attrs[p.key] = nil
		}
	}
	setBoolPair(attrs, "shared", f.share, f.noShare)
	setBoolPair(attrs, "admin_state_up", f.enable, f.disable)
	if f.name != "" {
		attrs["name"] = f.name
	}
	if f.description != "" {
		attrs["description"] = f.description
	}
	ports, err := fwGroupPorts(ctx, client, f, groupID)
	if err != nil {
		return nil, err
	}
	if ports != nil {
		attrs["ports"] = ports
	}
	return attrs, nil
}

// fwGroupPorts computes the ports list upstream sends: --port and --no-port
// together replace the list, --port alone adds to it (on set), --no-port alone
// empties it. nil means "leave ports alone".
func fwGroupPorts(ctx context.Context, client *gophercloud.ServiceClient, f *fwGroupFlags, groupID string) ([]string, error) {
	if len(f.ports) == 0 {
		if f.noPort {
			return []string{}, nil
		}
		return nil, nil
	}
	ids, err := resolveEach(ctx, client, f.ports, resolvePortID)
	if err != nil {
		return nil, err
	}
	if !f.noPort && groupID != "" {
		g, err := groups.Get(ctx, client, groupID).Extract()
		if err != nil {
			return nil, fmt.Errorf("getting firewall group %s: %w", groupID, err)
		}
		ids = append(ids, g.Ports...)
	}
	slices.Sort(ids)
	return slices.Compact(ids), nil
}

// fwGroupShowFields renders upstream's show columns (display names, sorted).
func fwGroupShowFields(g *groups.Group) ([]string, []any) {
	return []string{
		"Description", "Egress Policy ID", "ID", "Ingress Policy ID", "Name",
		"Ports", "Project", "Shared", "State", "Status",
	}, []any{
		g.Description, g.EgressFirewallPolicyID, g.ID, g.IngressFirewallPolicyID, g.Name,
		g.Ports, g.ProjectID, g.Shared, adminState(g.AdminStateUp), g.Status,
	}
}

func writeFWGroup(o *output.Options, w io.Writer, g *groups.Group) error {
	fields, values := fwGroupShowFields(g)
	return o.WriteSingle(w, fields, values)
}

func newFirewallGroupCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &fwGroupFlags{}
	cmd := &cobra.Command{
		Use:   "create [<name>]",
		Short: "Create a new firewall group",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			name, err := fwCreateName(cmd, args, f.name, "firewall group create")
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
			return runFirewallGroupCreate(ctx, client, o, f, cmd.OutOrStdout())
		},
	}
	bindFWGroupCommonFlags(cmd, f)
	fl := cmd.Flags()
	fl.StringVar(&f.name, flagFWName, "", "(deprecated, pass the name as a positional argument) name of the firewall group")
	fl.StringVar(&f.project, flagProject, "", "owner's project (name or ID)")
	fl.StringVar(&f.projectDomain, flagProjectDomain, "", projectDomainHelp)
	cmd.MarkFlagsMutuallyExclusive(flagFWPort, flagFWNoPort)
	return cmd
}

func runFirewallGroupCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *fwGroupFlags, w io.Writer) (err error) {
	defer func() { err = fwaasErr(ctx, client, err) }()
	attrs, err := buildFWGroupAttrs(ctx, client, f, "")
	if err != nil {
		return err
	}
	if f.projectID != "" {
		attrs["project_id"] = f.projectID
	}
	g, err := groups.Create(ctx, client, fwGroupBody(attrs)).Extract()
	if err != nil {
		return fmt.Errorf("creating firewall group: %w", err)
	}
	return writeFWGroup(o, w, g)
}

func newFirewallGroupDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <firewall-group> [<firewall-group> ...]",
		Short: "Delete firewall group(s)",
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
			return runFirewallGroupDelete(ctx, client, args)
		},
	}
}

func runFirewallGroupDelete(ctx context.Context, client *gophercloud.ServiceClient, refs []string) error {
	return batchdelete.Each(refs, func(ref string) error {
		id, err := resolveFirewallGroupID(ctx, client, ref)
		if err == nil {
			err = groups.Delete(ctx, client, id).ExtractErr()
		}
		if err != nil {
			return fwaasErr(ctx, client, fmt.Errorf("deleting firewall group %s: %w", ref, err))
		}
		return nil
	})
}

func newFirewallGroupListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var long bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List firewall groups",
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
			return runFirewallGroupList(ctx, client, o, long, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&long, "long", false, "list additional fields in output")
	return cmd
}

func runFirewallGroupList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, long bool, w io.Writer) (err error) {
	defer func() { err = fwaasErr(ctx, client, err) }()
	pages, err := groups.List(client, nil).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing firewall groups: %w", err)
	}
	all, err := groups.ExtractGroups(pages)
	if err != nil {
		return fmt.Errorf("parsing firewall group list: %w", err)
	}
	cols := []string{"ID", "Name", "Ingress Policy ID", "Egress Policy ID"}
	if long {
		cols = append(cols, "Description", "Status", "Ports", "State", "Shared", "Project")
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(all))}
	for _, g := range all {
		row := []any{g.ID, g.Name, g.IngressFirewallPolicyID, g.EgressFirewallPolicyID}
		if long {
			row = append(row, g.Description, g.Status, g.Ports, adminState(g.AdminStateUp), g.Shared, g.ProjectID)
		}
		t.Rows = append(t.Rows, row)
	}
	return o.WriteList(w, t)
}

func newFirewallGroupSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &fwGroupFlags{}
	cmd := &cobra.Command{
		Use:   "set <firewall-group>",
		Short: "Set firewall group properties",
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
			return runFirewallGroupSet(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	bindFWGroupCommonFlags(cmd, f)
	cmd.Flags().StringVar(&f.name, flagFWName, "", "new name of the firewall group")
	return cmd
}

func runFirewallGroupSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, f *fwGroupFlags, w io.Writer) (err error) {
	defer func() { err = fwaasErr(ctx, client, err) }()
	id, err := resolveFirewallGroupID(ctx, client, ref)
	if err != nil {
		return err
	}
	attrs, err := buildFWGroupAttrs(ctx, client, f, id)
	if err != nil {
		return err
	}
	if err := fwRequireChange(attrs, "firewall group set"); err != nil {
		return err
	}
	return updateFWGroup(ctx, client, o, ref, id, attrs, w)
}

func updateFWGroup(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref, id string, attrs map[string]any, w io.Writer) error {
	g, err := groups.Update(ctx, client, id, fwGroupBody(attrs)).Extract()
	if err != nil {
		return fmt.Errorf("updating firewall group %s: %w", ref, err)
	}
	return writeFWGroup(o, w, g)
}

func newFirewallGroupShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <firewall-group>",
		Short: "Display firewall group details",
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
			return runFirewallGroupShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runFirewallGroupShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, w io.Writer) (err error) {
	defer func() { err = fwaasErr(ctx, client, err) }()
	id, err := resolveFirewallGroupID(ctx, client, ref)
	if err != nil {
		return err
	}
	g, err := groups.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("getting firewall group %s: %w", ref, err)
	}
	return writeFWGroup(o, w, g)
}

// fwGroupUnsetFlags mirrors upstream UnsetFirewallGroup. --share and --enable
// are upstream's deprecated spellings of set --no-share / --disable.
type fwGroupUnsetFlags struct {
	ports   []string
	allPort bool
	ingress bool
	egress  bool
	share   bool
	enable  bool
}

func newFirewallGroupUnsetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &fwGroupUnsetFlags{}
	cmd := &cobra.Command{
		Use:   "unset <firewall-group>",
		Short: "Unset firewall group properties",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			if f.share {
				fwDeprecated(cmd, `the --share option is deprecated; use "firewall group set --no-share" instead`)
			}
			if f.enable {
				fwDeprecated(cmd, `the --enable option is deprecated; use "firewall group set --disable" instead`)
			}
			ctx := cmd.Context()
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			return runFirewallGroupUnset(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringArrayVar(&f.ports, flagFWPort, nil, "port to remove from the firewall group (name or ID, repeatable)")
	fl.BoolVar(&f.allPort, "all-port", false, "remove every port from the firewall group")
	fl.BoolVar(&f.ingress, flagFWIngressPolicy, false, "detach the ingress firewall policy")
	fl.BoolVar(&f.egress, flagFWEgressPolicy, false, "detach the egress firewall policy")
	fl.BoolVar(&f.share, flagFWShare, false, `(deprecated, use "firewall group set --no-share") restrict the firewall group to its project`)
	fl.BoolVar(&f.enable, "enable", false, `(deprecated, use "firewall group set --disable") disable the firewall group`)
	cmd.MarkFlagsMutuallyExclusive(flagFWPort, "all-port")
	return cmd
}

func runFirewallGroupUnset(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, f *fwGroupUnsetFlags, w io.Writer) (err error) {
	defer func() { err = fwaasErr(ctx, client, err) }()
	id, err := resolveFirewallGroupID(ctx, client, ref)
	if err != nil {
		return err
	}
	attrs := map[string]any{}
	if f.ingress {
		attrs["ingress_firewall_policy_id"] = nil
	}
	if f.egress {
		attrs["egress_firewall_policy_id"] = nil
	}
	if f.share {
		attrs["shared"] = false
	}
	if f.enable {
		attrs["admin_state_up"] = false
	}
	if len(f.ports) > 0 {
		g, err := groups.Get(ctx, client, id).Extract()
		if err != nil {
			return fmt.Errorf("getting firewall group %s: %w", ref, err)
		}
		removed, err := resolveEach(ctx, client, f.ports, resolvePortID)
		if err != nil {
			return err
		}
		kept := keepUnmatched(g.Ports, func(p string) bool { return slices.Contains(removed, p) })
		slices.Sort(kept)
		attrs["ports"] = slices.Compact(kept)
	}
	if f.allPort {
		attrs["ports"] = []string{}
	}
	if err := fwRequireChange(attrs, "firewall group unset"); err != nil {
		return err
	}
	return updateFWGroup(ctx, client, o, ref, id, attrs, w)
}

package network

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/security/groups"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/koc/internal/auth"
	"github.com/ftarasenko/koc/internal/cli/allprojects"
	"github.com/ftarasenko/koc/internal/cli/batchdelete"
	"github.com/ftarasenko/koc/internal/output"
	networkapi "github.com/ftarasenko/koc/pkg/openstack/network"
)

// Flag names local to "security group". They live here rather than in
// flagnames.go because only this file uses them.
const (
	flagSGStateful  = "stateful"
	flagSGStateless = "stateless"
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
	cmd.AddCommand(newSecurityGroupUnsetCommand(a, o))
	cmd.AddCommand(newSecurityGroupRuleCommand(a, o))
	return cmd
}

// secGroupExt is pkg/openstack/network's security group: gophercloud's plus
// the shared attribute and the rules as neutron sent them.
type secGroupExt = networkapi.SecurityGroup

// formatSecGroupRules is upstream's rules cell: one key='value' line per rule,
// keys sorted, empty values dropped, and the IDs the group itself already
// names (its own and its project's) left out.
func formatSecGroupRules(rules []map[string]any) string {
	lines := make([]string, 0, len(rules))
	for _, rule := range rules {
		keys := make([]string, 0, len(rule))
		for k, v := range rule {
			switch k {
			case "security_group_id", "project_id", "tenant_id":
				continue
			}
			if !emptyRuleValue(v) {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		pairs := make([]string, 0, len(keys))
		for _, k := range keys {
			pairs = append(pairs, fmt.Sprintf("%s='%s'", k, ruleValue(rule[k])))
		}
		lines = append(lines, strings.Join(pairs, ", "))
	}
	return strings.Join(lines, "\n")
}

// emptyRuleValue is Python's falsiness, which upstream's formatter filters on.
func emptyRuleValue(v any) bool {
	switch v := v.(type) {
	case nil:
		return true
	case string:
		return v == ""
	case bool:
		return !v
	case float64:
		return v == 0
	case []any:
		return len(v) == 0
	case map[string]any:
		return len(v) == 0
	}
	return false
}

func ruleValue(v any) string {
	if f, ok := v.(float64); ok {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return fmt.Sprint(v)
}

// secGroupShowFields renders upstream's show columns.
func secGroupShowFields(g *secGroupExt) ([]string, []any) {
	fields := []string{
		"id", "name", "description", "stateful", "shared", "project_id", "rules", "tags",
		"revision_number", "created_at", "updated_at",
	}
	values := []any{
		g.ID, g.Name, g.Description, g.Stateful, derefOrNil(g.Shared), g.ProjectID,
		formatSecGroupRules(g.RawRules), g.Tags, g.RevisionNumber, g.CreatedAt, g.UpdatedAt,
	}
	return fields, values
}

var secGroupColumns = fieldNames(secGroupShowFields(&secGroupExt{}))

// secGroupListFlags holds the filters accepted by "security group list".
// Upstream OSC (network/v2/security_group.py ListSecurityGroup) takes --project,
// --project-domain, --share/--no-share and the tag filters; --name and
// --all-projects are koc-native additions (see allProjectsNetworkList for the
// latter). --name is a plain neutron query filter, so it is exact-match and
// server-side.
type secGroupListFlags struct {
	name          string
	project       string
	projectDomain string
	share         bool
	noShare       bool
	allProjects   bool
	tagFilterFlags

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
	fl.StringVar(&f.project, flagProject, "", "list only security groups owned by this project (name or ID)")
	fl.StringVar(&f.projectDomain, flagProjectDomain, "", projectDomainHelp)
	bindTagFilterFlags(fl, &f.tagFilterFlags, "security groups")
	fl.BoolVar(&f.share, flagShare, false, "list only security groups shared between projects")
	fl.BoolVar(&f.noShare, flagNoShare, false, "list only security groups not shared between projects")
	allprojects.Bind(cmd, &f.allProjects, allProjectsNetworkList)
	cmd.MarkFlagsMutuallyExclusive(flagProject, "all-projects")
	return cmd
}

func runSecurityGroupList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	f *secGroupListFlags, projectID string, w io.Writer,
) error {
	opts := networkapi.SecurityGroupListOpts{ListOpts: groups.ListOpts{Name: f.name, ProjectID: projectID}, Shared: f.shared}
	f.apply(&opts.Tags, &opts.TagsAny, &opts.NotTags, &opts.NotTagsAny)
	all, err := networkapi.ListSecurityGroups(ctx, client, opts)
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
	g, err := networkapi.GetSecurityGroup(ctx, client, id)
	if err != nil {
		return relabel(err, "getting security group %s", nameOrID)
	}
	fields, values := secGroupShowFields(g)
	return o.WriteSingle(w, fields, values)
}

// secGroupCreateFlags mirrors upstream CreateSecurityGroup.
type secGroupCreateFlags struct {
	description   string
	stateful      bool
	stateless     bool
	project       string
	projectDomain string
	// projectID is --project resolved by RunE, so the seam needs no identity client.
	projectID     string
	extraProperty []string
	tagWriteFlags
}

func newSecurityGroupCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &secGroupCreateFlags{}
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new security group",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, session, err := newNetworkSession(ctx, a)
			if err != nil {
				return err
			}
			if f.projectID, err = resolveProjectRef(ctx, session, f.project, f.projectDomain); err != nil {
				return err
			}
			return runSecurityGroupCreate(ctx, client, o, args[0], f, cmd.Flags(), cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.description, flagDescription, "", "description for the security group (default: its name)")
	fl.BoolVar(&f.stateful, flagSGStateful, false, "security group is stateful (neutron's default)")
	fl.BoolVar(&f.stateless, flagSGStateless, false, "security group is stateless")
	fl.StringVar(&f.project, flagProject, "", "owner's project (name or ID; admin)")
	fl.StringVar(&f.projectDomain, flagProjectDomain, "", projectDomainHelp)
	bindExtraPropertyFlag(fl, &f.extraProperty)
	bindTagCreateFlags(cmd, &f.tagWriteFlags, nounSecurityGroup)
	cmd.MarkFlagsMutuallyExclusive(flagSGStateful, flagSGStateless)
	return cmd
}

// runSecurityGroupCreate follows upstream: an omitted --description defaults
// to the group's name, --stateful/--stateless is sent only when given, and
// tags are applied after the create.
func runSecurityGroupCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	name string, f *secGroupCreateFlags, flags flagSet, w io.Writer,
) error {
	if err := o.CheckColumns(secGroupColumns...); err != nil {
		return err
	}
	opts := groups.CreateOpts{
		Name:        name,
		Description: name,
		ProjectID:   f.projectID,
		Stateful:    enableDisable(flags, f.stateful, f.stateless, flagSGStateful, flagSGStateless),
	}
	if flags.Changed(flagDescription) {
		opts.Description = f.description
	}
	extra, err := parseExtraProperties(f.extraProperty, false)
	if err != nil {
		return err
	}
	g, err := networkapi.CreateSecurityGroup(ctx, client, networkapi.WithSecurityGroupCreateAttrs(opts, extra))
	if err != nil {
		return err
	}
	if g.Tags, err = applyTagsForSet(ctx, client, tagResourceSecurityGroups, g.ID, g.Tags, &f.tagWriteFlags); err != nil {
		return err
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
		if err := networkapi.DeleteSecurityGroup(ctx, client, id); err != nil {
			return relabel(err, "deleting security group %s", nameOrID)
		}
		if _, err := fmt.Fprintf(w, "Deleted security group %s\n", nameOrID); err != nil {
			return err
		}
		return nil
	})
}

type secGroupSetFlags struct {
	name          string
	description   string
	stateful      bool
	stateless     bool
	extraProperty []string
	tagWriteFlags
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
	fl.StringVar(&f.description, flagDescription, "", "new description")
	fl.BoolVar(&f.stateful, flagSGStateful, false, "make the security group stateful")
	fl.BoolVar(&f.stateless, flagSGStateless, false, "make the security group stateless")
	bindExtraPropertyFlag(fl, &f.extraProperty)
	bindTagSetFlags(fl, &f.tagWriteFlags, nounSecurityGroup)
	cmd.MarkFlagsMutuallyExclusive(flagSGStateful, flagSGStateless)
	return cmd
}

func runSecurityGroupSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, nameOrID string, f *secGroupSetFlags, flags flagSet, w io.Writer) error {
	if err := o.CheckColumns(secGroupColumns...); err != nil {
		return err
	}
	opts := groups.UpdateOpts{
		Name:     f.name,
		Stateful: enableDisable(flags, f.stateful, f.stateless, flagSGStateful, flagSGStateless),
	}
	if flags.Changed(flagDescription) {
		opts.Description = &f.description
	}
	extra, err := parseExtraProperties(f.extraProperty, false)
	if err != nil {
		return err
	}
	changed := f.name != "" || opts.Description != nil || opts.Stateful != nil || len(extra) > 0
	if !changed && !f.given() {
		return fmt.Errorf("security group set requires at least one attribute flag")
	}
	id, err := resolveSecGroupID(ctx, client, nameOrID)
	if err != nil {
		return err
	}
	var g *secGroupExt
	if changed {
		if g, err = networkapi.UpdateSecurityGroup(ctx, client, id, networkapi.WithSecurityGroupUpdateAttrs(opts, extra)); err != nil {
			return relabel(err, "updating security group %s", nameOrID)
		}
	} else if g, err = networkapi.GetSecurityGroup(ctx, client, id); err != nil {
		// Tags are the only change: upstream sends no update either.
		return relabel(err, "getting security group %s", nameOrID)
	}
	if g.Tags, err = applyTagsForSet(ctx, client, tagResourceSecurityGroups, id, g.Tags, &f.tagWriteFlags); err != nil {
		return err
	}
	fields, values := secGroupShowFields(g)
	return o.WriteSingle(w, fields, values)
}

// newSecurityGroupUnsetCommand is upstream UnsetSecurityGroup, which takes the
// tag flags and nothing else: a security group has no other attribute to clear.
func newSecurityGroupUnsetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &tagWriteFlags{}
	cmd := &cobra.Command{
		Use:   "unset <group>",
		Short: "Unset security group properties",
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
			return runSecurityGroupUnset(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	bindTagUnsetFlags(cmd, f, nounSecurityGroup)
	return cmd
}

func runSecurityGroupUnset(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, nameOrID string, f *tagWriteFlags, w io.Writer) error {
	if err := o.CheckColumns(secGroupColumns...); err != nil {
		return err
	}
	if !f.given() {
		return fmt.Errorf("security group unset requires --%s or --%s", flagTag, flagAllTag)
	}
	id, err := resolveSecGroupID(ctx, client, nameOrID)
	if err != nil {
		return err
	}
	g, err := networkapi.GetSecurityGroup(ctx, client, id)
	if err != nil {
		return relabel(err, "getting security group %s", nameOrID)
	}
	if g.Tags, err = applyTagsForUnset(ctx, client, tagResourceSecurityGroups, id, g.Tags, f); err != nil {
		return err
	}
	fields, values := secGroupShowFields(g)
	return o.WriteSingle(w, fields, values)
}

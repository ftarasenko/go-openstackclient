package network

import (
	"context"
	"fmt"
	"io"
	"strconv"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/security/groups"
	"github.com/gophercloud/gophercloud/v2/pagination"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/allprojects"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
	"github.com/ftarasenko/go-openstackclient/internal/output"
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

// SecGroupSharedAttr is neutron's `shared` attribute of a security group (the
// security-groups-shared-filtering extension), which gophercloud's SecGroup
// does not model. A pointer, so a cloud without the extension renders an empty
// cell rather than a misleading False.
type SecGroupSharedAttr struct {
	Shared *bool `json:"shared"`
}

// secGroupExt is a SecGroup plus its shared attribute. Both are anonymous
// embeds so gophercloud's ExtractInto decodes each separately — SecGroup's own
// UnmarshalJSON would otherwise swallow the extension field. The embed is
// exported because gophercloud reflects into each embedded struct, and
// reflect cannot read an unexported one (it panics).
type secGroupExt struct {
	groups.SecGroup
	SecGroupSharedAttr
}

// secGroupKey is the envelope of a single security group in neutron's
// responses. groups' results define no ExtractInto that knows it, so the
// label is passed to ExtractIntoStructPtr directly.
const secGroupKey = "security_group"

// secGroupShowFields renders upstream's show columns except `rules`, which koc
// lists with "security group rule list <group>" instead.
func secGroupShowFields(g *secGroupExt) ([]string, []any) {
	fields := []string{
		"id", "name", "description", "stateful", "shared", "project_id", "tags",
		"revision_number", "created_at", "updated_at",
	}
	values := []any{
		g.ID, g.Name, g.Description, g.Stateful, derefOrNil(g.Shared), g.ProjectID, g.Tags,
		g.RevisionNumber, g.CreatedAt, g.UpdatedAt,
	}
	return fields, values
}

func getSecGroup(ctx context.Context, client *gophercloud.ServiceClient, id string) (*secGroupExt, error) {
	var g secGroupExt
	if err := groups.Get(ctx, client, id).ExtractIntoStructPtr(&g, secGroupKey); err != nil {
		return nil, err
	}
	return &g, nil
}

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
	opts := groups.ListOpts{Name: f.name, ProjectID: projectID}
	f.apply(&opts.Tags, &opts.TagsAny, &opts.NotTags, &opts.NotTagsAny)
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
func listSecGroups(ctx context.Context, client *gophercloud.ServiceClient, opts groups.ListOpts, shared *bool) ([]secGroupExt, error) {
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
	var all []secGroupExt
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
	g, err := getSecGroup(ctx, client, id)
	if err != nil {
		return fmt.Errorf("getting security group %s: %w", nameOrID, err)
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
	var g secGroupExt
	if err := groups.Create(ctx, client, withSecGroupCreateAttrs(opts, extra)).ExtractIntoStructPtr(&g, secGroupKey); err != nil {
		return fmt.Errorf("creating security group: %w", err)
	}
	if g.Tags, err = applyTagsForSet(ctx, client, tagResourceSecurityGroups, g.ID, g.Tags, &f.tagWriteFlags); err != nil {
		return err
	}
	fields, values := secGroupShowFields(&g)
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
		g = &secGroupExt{}
		if err := groups.Update(ctx, client, id, withSecGroupUpdateAttrs(opts, extra)).ExtractIntoStructPtr(g, secGroupKey); err != nil {
			return fmt.Errorf("updating security group %s: %w", nameOrID, err)
		}
	} else if g, err = getSecGroup(ctx, client, id); err != nil {
		// Tags are the only change: upstream sends no update either.
		return fmt.Errorf("getting security group %s: %w", nameOrID, err)
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
	if !f.given() {
		return fmt.Errorf("security group unset requires --%s or --%s", flagTag, flagAllTag)
	}
	id, err := resolveSecGroupID(ctx, client, nameOrID)
	if err != nil {
		return err
	}
	g, err := getSecGroup(ctx, client, id)
	if err != nil {
		return fmt.Errorf("getting security group %s: %w", nameOrID, err)
	}
	if g.Tags, err = applyTagsForUnset(ctx, client, tagResourceSecurityGroups, id, g.Tags, f); err != nil {
		return err
	}
	fields, values := secGroupShowFields(g)
	return o.WriteSingle(w, fields, values)
}

package identity

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/groups"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/users"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Flag names follow upstream OSC (`openstack group ...`). UNVERIFIED against
// KeyStack docs (https://docs.keystack.ru/ returned HTTP 403 at implementation
// time); falls back to upstream OSC semantics.

func newGroupCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "group", Short: "Manage groups"}
	cmd.AddCommand(
		newGroupListCommand(a, o),
		newGroupShowCommand(a, o),
		newGroupCreateCommand(a, o),
		newGroupDeleteCommand(a, o),
		newGroupSetCommand(a, o),
		newGroupAddCommand(a, o),
		newGroupRemoveCommand(a, o),
		newGroupContainsCommand(a, o),
	)
	return cmd
}

// groupFields is the Field/Value view of a single group.
func groupFields(g *groups.Group) ([]string, []any) {
	return []string{"ID", "Name", colDomainID, "Description"},
		[]any{g.ID, g.Name, g.DomainID, g.Description}
}

// --- list ------------------------------------------------------------------

type groupListFlags struct {
	domain     string
	user       string
	userDomain string
}

func newGroupListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &groupListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List groups",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newIdentityClient(ctx, a)
			if err != nil {
				return err
			}
			return runGroupList(ctx, client, o, f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.domain, "domain", "", "filter by domain (name or ID)")
	fl.StringVar(&f.user, "user", "", "list only the groups this user belongs to (name or ID)")
	fl.StringVar(&f.userDomain, flagUserDomain, "", "domain owning --user, to disambiguate the name (name or ID)")
	return cmd
}

// runGroupList lists groups, optionally restricted to one user's memberships.
//
// --user switches endpoint rather than adding a filter: keystone has no
// ?user_id= on /v3/groups, the membership view lives at
// /v3/users/<user>/groups. That endpoint takes no filters of its own, so
// --domain is applied client-side when both are given.
// groupsOfUser lists the groups a user belongs to. users.ListGroups takes no
// domain filter, so --domain is applied to the result rather than the request.
func groupsOfUser(ctx context.Context, client *gophercloud.ServiceClient,
	f *groupListFlags, domainID string,
) ([]groups.Group, error) {
	userDomainID, err := resolveDomainID(ctx, client, f.userDomain)
	if err != nil {
		return nil, err
	}
	userID, err := resolveUserID(ctx, client, f.user, userDomainID)
	if err != nil {
		return nil, err
	}
	pages, err := users.ListGroups(client, userID).AllPages(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing groups of user %q: %w", f.user, err)
	}
	all, err := groups.ExtractGroups(pages)
	if err != nil {
		return nil, fmt.Errorf("parsing group list: %w", err)
	}
	if domainID == "" {
		return all, nil
	}
	filtered := make([]groups.Group, 0, len(all))
	for _, g := range all {
		if g.DomainID == domainID {
			filtered = append(filtered, g)
		}
	}
	return filtered, nil
}

// groupsInDomain lists every group, narrowed to one domain when domainID is set.
func groupsInDomain(ctx context.Context, client *gophercloud.ServiceClient,
	domainID string,
) ([]groups.Group, error) {
	pages, err := groups.List(client, groups.ListOpts{DomainID: domainID}).AllPages(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing groups: %w", err)
	}
	all, err := groups.ExtractGroups(pages)
	if err != nil {
		return nil, fmt.Errorf("parsing group list: %w", err)
	}
	return all, nil
}

func runGroupList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *groupListFlags, w io.Writer) error {
	domainID, err := resolveDomainID(ctx, client, f.domain)
	if err != nil {
		return err
	}

	var all []groups.Group
	if f.user != "" {
		all, err = groupsOfUser(ctx, client, f, domainID)
	} else {
		all, err = groupsInDomain(ctx, client, domainID)
	}
	if err != nil {
		return err
	}

	t := output.Table{Columns: []string{"ID", "Name", colDomainID, "Description"}, Rows: make([][]any, 0, len(all))}
	for _, g := range all {
		t.Rows = append(t.Rows, []any{g.ID, g.Name, g.DomainID, g.Description})
	}
	return o.WriteList(w, t)
}

// --- show ------------------------------------------------------------------

func newGroupShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var domain string
	cmd := &cobra.Command{
		Use:   "show <group>",
		Short: "Show group details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newIdentityClient(ctx, a)
			if err != nil {
				return err
			}
			return runGroupShow(ctx, client, o, args[0], domain, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&domain, "domain", "", "domain owning the group, to disambiguate the name (name or ID)")
	return cmd
}

func runGroupShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, nameOrID, domain string, w io.Writer) error {
	domainID, err := resolveDomainID(ctx, client, domain)
	if err != nil {
		return err
	}
	id, err := resolveGroupID(ctx, client, nameOrID, domainID)
	if err != nil {
		return err
	}
	g, err := groups.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("showing group %q: %w", nameOrID, err)
	}
	fields, values := groupFields(g)
	return o.WriteSingle(w, fields, values)
}

// --- create ----------------------------------------------------------------

type groupWriteFlags struct {
	description string
	domain      string
	name        string
}

func newGroupCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &groupWriteFlags{}
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new group",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newIdentityClient(ctx, a)
			if err != nil {
				return err
			}
			return runGroupCreate(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.description, "description", "", "group description")
	fl.StringVar(&f.domain, "domain", "", "domain owning the new group (name or ID)")
	return cmd
}

func runGroupCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, name string, f *groupWriteFlags, w io.Writer) error {
	domainID, err := resolveDomainID(ctx, client, f.domain)
	if err != nil {
		return err
	}
	g, err := groups.Create(ctx, client, groups.CreateOpts{
		Name:        name,
		Description: f.description,
		DomainID:    domainID,
	}).Extract()
	if err != nil {
		return fmt.Errorf("creating group %q: %w", name, err)
	}
	fields, values := groupFields(g)
	return o.WriteSingle(w, fields, values)
}

// --- delete ----------------------------------------------------------------

func newGroupDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var domain string
	cmd := &cobra.Command{
		Use:   "delete <group> [<group>...]",
		Short: "Delete one or more groups",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newIdentityClient(ctx, a)
			if err != nil {
				return err
			}
			return runGroupDelete(ctx, client, args, domain, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&domain, "domain", "", "domain owning the groups, to disambiguate names (name or ID)")
	return cmd
}

func runGroupDelete(ctx context.Context, client *gophercloud.ServiceClient, refs []string, domain string, w io.Writer) error {
	domainID, err := resolveDomainID(ctx, client, domain)
	if err != nil {
		return err
	}
	return batchdelete.Each(refs, func(ref string) error {
		id, rerr := resolveGroupID(ctx, client, ref, domainID)
		if rerr != nil {
			return rerr
		}
		if derr := groups.Delete(ctx, client, id).ExtractErr(); derr != nil {
			return fmt.Errorf("deleting group %q: %w", ref, derr)
		}
		if _, werr := fmt.Fprintf(w, "Deleted group %s\n", ref); werr != nil {
			return werr
		}
		return nil
	})
}

// --- set -------------------------------------------------------------------

func newGroupSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &groupWriteFlags{}
	cmd := &cobra.Command{
		Use:   "set <group>",
		Short: "Update a group's name or description",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			fl := cmd.Flags()
			if !fl.Changed("name") && !fl.Changed("description") {
				return fmt.Errorf("nothing to set: pass --name and/or --description")
			}
			ctx := cmd.Context()
			client, err := newIdentityClient(ctx, a)
			if err != nil {
				return err
			}
			return runGroupSet(ctx, client, o, args[0], f, fl.Changed("description"), cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "new group name")
	fl.StringVar(&f.description, "description", "", "new group description")
	fl.StringVar(&f.domain, "domain", "", "domain owning the group, to disambiguate the name (name or ID)")
	return cmd
}

// runGroupSet updates a group. descriptionSet distinguishes "--description ”"
// (clear it) from "--description not given" (leave it alone): groups.UpdateOpts
// takes Description as a *string precisely so an empty value can be sent.
func runGroupSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	nameOrID string, f *groupWriteFlags, descriptionSet bool, w io.Writer,
) error {
	domainID, err := resolveDomainID(ctx, client, f.domain)
	if err != nil {
		return err
	}
	id, err := resolveGroupID(ctx, client, nameOrID, domainID)
	if err != nil {
		return err
	}
	opts := groups.UpdateOpts{Name: f.name}
	if descriptionSet {
		desc := f.description
		opts.Description = &desc
	}
	g, err := groups.Update(ctx, client, id, opts).Extract()
	if err != nil {
		return fmt.Errorf("updating group %q: %w", nameOrID, err)
	}
	fields, values := groupFields(g)
	return o.WriteSingle(w, fields, values)
}

// --- add/remove user -------------------------------------------------------

// groupMembershipFlags carries the name-disambiguation domains for the two
// nouns a membership change names: the group and the user.
type groupMembershipFlags struct {
	groupDomain string
	userDomain  string
}

// newGroupAddCommand models the two-word noun `group add user` as a nested
// parent so cobra resolves it unambiguously (AGENTS.md → command pattern).
func newGroupAddCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "add", Short: "Add members to a group"}
	cmd.AddCommand(newGroupMembershipCommand(a, o, membershipAdd))
	return cmd
}

func newGroupRemoveCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "remove", Short: "Remove members from a group"}
	cmd.AddCommand(newGroupMembershipCommand(a, o, membershipRemove))
	return cmd
}

// membershipVerb is the shared shape of `group add user` / `group remove user`,
// which differ only in the keystone call and the confirmation wording.
type membershipVerb struct {
	short   string
	past    string
	prep    string
	apply   func(ctx context.Context, client *gophercloud.ServiceClient, groupID, userID string) error
	rollsUp string
}

var membershipAdd = membershipVerb{
	short:   "Add a user to a group",
	past:    "Added",
	prep:    "to",
	rollsUp: "adding user %q to group %q: %w",
	apply: func(ctx context.Context, client *gophercloud.ServiceClient, groupID, userID string) error {
		return users.AddToGroup(ctx, client, groupID, userID).ExtractErr()
	},
}

var membershipRemove = membershipVerb{
	short:   "Remove a user from a group",
	past:    "Removed",
	prep:    "from",
	rollsUp: "removing user %q from group %q: %w",
	apply: func(ctx context.Context, client *gophercloud.ServiceClient, groupID, userID string) error {
		return users.RemoveFromGroup(ctx, client, groupID, userID).ExtractErr()
	},
}

func newGroupMembershipCommand(a *auth.Options, o *output.Options, v membershipVerb) *cobra.Command {
	f := &groupMembershipFlags{}
	cmd := &cobra.Command{
		Use:   "user <group> <user> [<user>...]",
		Short: v.short,
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newIdentityClient(ctx, a)
			if err != nil {
				return err
			}
			return runGroupMembership(ctx, client, v, args[0], args[1:], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.groupDomain, "group-domain", "", "domain owning the group, to disambiguate its name (name or ID)")
	fl.StringVar(&f.userDomain, flagUserDomain, "", "domain owning the users, to disambiguate their names (name or ID)")
	return cmd
}

func runGroupMembership(ctx context.Context, client *gophercloud.ServiceClient, v membershipVerb,
	groupRef string, userRefs []string, f *groupMembershipFlags, w io.Writer,
) error {
	groupDomainID, err := resolveDomainID(ctx, client, f.groupDomain)
	if err != nil {
		return err
	}
	userDomainID, err := resolveDomainID(ctx, client, f.userDomain)
	if err != nil {
		return err
	}
	groupID, err := resolveGroupID(ctx, client, groupRef, groupDomainID)
	if err != nil {
		return err
	}
	for _, userRef := range userRefs {
		userID, uerr := resolveUserID(ctx, client, userRef, userDomainID)
		if uerr != nil {
			return uerr
		}
		if aerr := v.apply(ctx, client, groupID, userID); aerr != nil {
			return fmt.Errorf(v.rollsUp, userRef, groupRef, aerr)
		}
		if _, werr := fmt.Fprintf(w, "%s user %s %s group %s\n", v.past, userRef, v.prep, groupRef); werr != nil {
			return werr
		}
	}
	return nil
}

// --- contains user ---------------------------------------------------------

func newGroupContainsCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "contains", Short: "Check group membership"}
	f := &groupMembershipFlags{}
	sub := &cobra.Command{
		Use:   "user <group> <user>",
		Short: "Check whether a user belongs to a group",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newIdentityClient(ctx, a)
			if err != nil {
				return err
			}
			return runGroupContains(ctx, client, args[0], args[1], f, cmd.OutOrStdout())
		},
	}
	fl := sub.Flags()
	fl.StringVar(&f.groupDomain, "group-domain", "", "domain owning the group, to disambiguate its name (name or ID)")
	fl.StringVar(&f.userDomain, flagUserDomain, "", "domain owning the user, to disambiguate its name (name or ID)")
	cmd.AddCommand(sub)
	return cmd
}

func runGroupContains(ctx context.Context, client *gophercloud.ServiceClient,
	groupRef, userRef string, f *groupMembershipFlags, w io.Writer,
) error {
	groupDomainID, err := resolveDomainID(ctx, client, f.groupDomain)
	if err != nil {
		return err
	}
	userDomainID, err := resolveDomainID(ctx, client, f.userDomain)
	if err != nil {
		return err
	}
	groupID, err := resolveGroupID(ctx, client, groupRef, groupDomainID)
	if err != nil {
		return err
	}
	userID, err := resolveUserID(ctx, client, userRef, userDomainID)
	if err != nil {
		return err
	}
	ok, err := users.IsMemberOfGroup(ctx, client, groupID, userID).Extract()
	if err != nil {
		return fmt.Errorf("checking whether user %q is in group %q: %w", userRef, groupRef, err)
	}
	if !ok {
		return fmt.Errorf("user %q is not in group %q", userRef, groupRef)
	}
	if _, err := fmt.Fprintf(w, "User %s is in group %s\n", userRef, groupRef); err != nil {
		return err
	}
	return nil
}

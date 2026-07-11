package identity

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/roles"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Flag names follow upstream OSC (`openstack role ...`). The KeyStack command
// reference at https://docs.keystack.ru/ returned HTTP 403 at implementation
// time, so these flags are UNVERIFIED against KeyStack and fall back to upstream
// OSC semantics.

func newRoleCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "role", Short: "Manage roles and role assignments"}
	cmd.AddCommand(
		newRoleListCommand(a, o),
		newRoleShowCommand(a, o),
		newRoleAddCommand(a, o),
		newRoleRemoveCommand(a, o),
		newRoleAssignmentCommand(a, o),
	)
	return cmd
}

func newRoleListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var domain string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List roles",
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
			return runRoleList(ctx, client, o, domain, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&domain, "domain", "", "filter by domain (name or ID)")
	return cmd
}

func runRoleList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, domainNameOrID string, w io.Writer) error {
	domainID, err := resolveDomainID(ctx, client, domainNameOrID)
	if err != nil {
		return err
	}
	pages, err := roles.List(client, roles.ListOpts{DomainID: domainID}).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing roles: %w", err)
	}
	all, err := roles.ExtractRoles(pages)
	if err != nil {
		return fmt.Errorf("parsing role list: %w", err)
	}
	t := output.Table{Columns: []string{"ID", "Name", "Domain ID"}, Rows: make([][]any, 0, len(all))}
	for _, r := range all {
		t.Rows = append(t.Rows, []any{r.ID, r.Name, r.DomainID})
	}
	return o.WriteList(w, t)
}

func newRoleShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <role>",
		Short: "Show role details",
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
			return runRoleShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runRoleShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, nameOrID string, w io.Writer) error {
	id, err := resolveRoleID(ctx, client, nameOrID, "")
	if err != nil {
		return err
	}
	r, err := roles.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("showing role %q: %w", nameOrID, err)
	}
	return o.WriteSingle(w,
		[]string{"ID", "Name", "Domain ID", "Description"},
		[]any{r.ID, r.Name, r.DomainID, r.Description})
}

// grantFlags carries the actor (user/group) and scope (project/domain) shared
// by "role add" and "role remove".
type grantFlags struct {
	user    string
	group   string
	project string
	domain  string
}

func addGrantFlags(cmd *cobra.Command, f *grantFlags) {
	fl := cmd.Flags()
	fl.StringVar(&f.user, "user", "", "user to grant the role to (name or ID)")
	fl.StringVar(&f.group, "group", "", "group to grant the role to (name or ID)")
	fl.StringVar(&f.project, "project", "", "project to scope the grant to (name or ID)")
	fl.StringVar(&f.domain, "domain", "", "domain to scope the grant to (name or ID)")
}

// resolveGrant resolves the role plus the actor/scope IDs and validates the xor
// constraints keystone enforces (exactly one actor, exactly one scope).
func resolveGrant(ctx context.Context, client *gophercloud.ServiceClient, roleNameOrID string, f *grantFlags) (roleID, userID, groupID, projectID, domainID string, err error) {
	if (f.user == "") == (f.group == "") {
		return "", "", "", "", "", fmt.Errorf("exactly one of --user or --group is required")
	}
	if (f.project == "") == (f.domain == "") {
		return "", "", "", "", "", fmt.Errorf("exactly one of --project or --domain is required")
	}

	// The scope domain also qualifies name lookups for the actor and role.
	domainID, err = resolveDomainID(ctx, client, f.domain)
	if err != nil {
		return
	}
	if f.project != "" {
		projectID, err = resolveProjectID(ctx, client, f.project, "")
		if err != nil {
			return
		}
	}
	if f.user != "" {
		userID, err = resolveUserID(ctx, client, f.user, domainID)
		if err != nil {
			return
		}
	}
	if f.group != "" {
		groupID, err = resolveGroupID(ctx, client, f.group, domainID)
		if err != nil {
			return
		}
	}
	roleID, err = resolveRoleID(ctx, client, roleNameOrID, "")
	return
}

func newRoleAddCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &grantFlags{}
	cmd := &cobra.Command{
		Use:   "add <role>",
		Short: "Grant a role to a user or group on a project or domain",
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
			return runRoleAdd(ctx, client, args[0], f)
		},
	}
	addGrantFlags(cmd, f)
	return cmd
}

func runRoleAdd(ctx context.Context, client *gophercloud.ServiceClient, roleNameOrID string, f *grantFlags) error {
	roleID, userID, groupID, projectID, domainID, err := resolveGrant(ctx, client, roleNameOrID, f)
	if err != nil {
		return err
	}
	opts := roles.AssignOpts{UserID: userID, GroupID: groupID, ProjectID: projectID, DomainID: domainID}
	if err := roles.Assign(ctx, client, roleID, opts).ExtractErr(); err != nil {
		return fmt.Errorf("granting role %q: %w", roleNameOrID, err)
	}
	return nil
}

func newRoleRemoveCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &grantFlags{}
	cmd := &cobra.Command{
		Use:   "remove <role>",
		Short: "Revoke a role from a user or group on a project or domain",
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
			return runRoleRemove(ctx, client, args[0], f)
		},
	}
	addGrantFlags(cmd, f)
	return cmd
}

func runRoleRemove(ctx context.Context, client *gophercloud.ServiceClient, roleNameOrID string, f *grantFlags) error {
	roleID, userID, groupID, projectID, domainID, err := resolveGrant(ctx, client, roleNameOrID, f)
	if err != nil {
		return err
	}
	opts := roles.UnassignOpts{UserID: userID, GroupID: groupID, ProjectID: projectID, DomainID: domainID}
	if err := roles.Unassign(ctx, client, roleID, opts).ExtractErr(); err != nil {
		return fmt.Errorf("revoking role %q: %w", roleNameOrID, err)
	}
	return nil
}

// newRoleAssignmentCommand builds "role assignment list".
func newRoleAssignmentCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "assignment", Short: "Query role assignments"}
	cmd.AddCommand(newRoleAssignmentListCommand(a, o))
	return cmd
}

type assignmentListFlags struct {
	user    string
	group   string
	project string
	domain  string
	names   bool
}

func newRoleAssignmentListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &assignmentListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List role assignments",
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
			return runRoleAssignmentList(ctx, client, o, f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.user, "user", "", "filter by user (name or ID)")
	fl.StringVar(&f.group, "group", "", "filter by group (name or ID)")
	fl.StringVar(&f.project, "project", "", "filter by project (name or ID)")
	fl.StringVar(&f.domain, "domain", "", "filter by domain (name or ID)")
	fl.BoolVar(&f.names, "names", false, "display names instead of IDs (requires keystone 3.6+)")
	return cmd
}

func runRoleAssignmentList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *assignmentListFlags, w io.Writer) error {
	domainID, err := resolveDomainID(ctx, client, f.domain)
	if err != nil {
		return err
	}
	projectID, err := resolveProjectID(ctx, client, f.project, domainID)
	if err != nil {
		return err
	}
	userID, err := resolveUserID(ctx, client, f.user, domainID)
	if err != nil {
		return err
	}
	groupID, err := resolveGroupID(ctx, client, f.group, domainID)
	if err != nil {
		return err
	}
	opts := roles.ListAssignmentsOpts{
		UserID:         userID,
		GroupID:        groupID,
		ScopeProjectID: projectID,
		ScopeDomainID:  domainID,
	}
	if f.names {
		opts.IncludeNames = &f.names
	}
	pages, err := roles.ListAssignments(client, opts).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing role assignments: %w", err)
	}
	all, err := roles.ExtractRoleAssignments(pages)
	if err != nil {
		return fmt.Errorf("parsing role assignment list: %w", err)
	}
	t := output.Table{Columns: []string{"Role", "User", "Group", "Project", "Domain"}, Rows: make([][]any, 0, len(all))}
	for _, ra := range all {
		role := ra.Role.ID
		user := ra.User.ID
		group := ra.Group.ID
		project := ra.Scope.Project.ID
		domain := ra.Scope.Domain.ID
		if f.names {
			role = firstNonEmpty(ra.Role.Name, role)
			user = firstNonEmpty(ra.User.Name, user)
			group = firstNonEmpty(ra.Group.Name, group)
			project = firstNonEmpty(ra.Scope.Project.Name, project)
			domain = firstNonEmpty(ra.Scope.Domain.Name, domain)
		}
		t.Rows = append(t.Rows, []any{role, user, group, project, domain})
	}
	return o.WriteList(w, t)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

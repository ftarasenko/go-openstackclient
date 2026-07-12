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
//
// Names are unique only within a domain, so each actor/scope reference carries
// its own domain qualifier (mirroring OSC's --user-domain/--group-domain/
// --project-domain/--role-domain). The scope --domain is used ONLY to scope a
// domain-level grant; it never qualifies the actor/role name lookups. When a
// qualifier is empty it falls back to the pre-existing single-domain behavior
// (project/role resolved with no domain filter, user/group resolved within the
// scope domain).
type grantFlags struct {
	user    string
	group   string
	project string
	domain  string

	userDomain    string
	groupDomain   string
	projectDomain string
	roleDomain    string
}

func addGrantFlags(cmd *cobra.Command, f *grantFlags) {
	fl := cmd.Flags()
	fl.StringVar(&f.user, "user", "", "user to grant the role to (name or ID)")
	fl.StringVar(&f.group, "group", "", "group to grant the role to (name or ID)")
	fl.StringVar(&f.project, "project", "", "project to scope the grant to (name or ID)")
	fl.StringVar(&f.domain, "domain", "", "domain to scope the grant to (name or ID)")
	fl.StringVar(&f.userDomain, "user-domain", "", "domain owning --user (name or ID)")
	fl.StringVar(&f.groupDomain, "group-domain", "", "domain owning --group (name or ID)")
	fl.StringVar(&f.projectDomain, "project-domain", "", "domain owning --project (name or ID)")
	fl.StringVar(&f.roleDomain, "role-domain", "", "domain owning the role (name or ID)")
}

// qualifierDomainID resolves a per-actor domain qualifier, falling back to the
// supplied default (typically the scope domain, or empty) when the qualifier
// was not set. This preserves the pre-existing single-domain resolution while
// letting callers name a cross-domain actor/role explicitly.
func qualifierDomainID(ctx context.Context, client *gophercloud.ServiceClient, qualifier, fallbackID string) (string, error) {
	if qualifier == "" {
		return fallbackID, nil
	}
	return resolveDomainID(ctx, client, qualifier)
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

	// Scope domain (only meaningful for a domain-scoped grant). It is the
	// fallback qualifier for the actor lookups so domain-scoped grants keep
	// resolving actors within that domain.
	domainID, err = resolveDomainID(ctx, client, f.domain)
	if err != nil {
		return
	}
	if f.project != "" {
		var projDomID string
		if projDomID, err = qualifierDomainID(ctx, client, f.projectDomain, ""); err != nil {
			return
		}
		if projectID, err = resolveProjectID(ctx, client, f.project, projDomID); err != nil {
			return
		}
	}
	if f.user != "" {
		var userDomID string
		if userDomID, err = qualifierDomainID(ctx, client, f.userDomain, domainID); err != nil {
			return
		}
		if userID, err = resolveUserID(ctx, client, f.user, userDomID); err != nil {
			return
		}
	}
	if f.group != "" {
		var groupDomID string
		if groupDomID, err = qualifierDomainID(ctx, client, f.groupDomain, domainID); err != nil {
			return
		}
		if groupID, err = resolveGroupID(ctx, client, f.group, groupDomID); err != nil {
			return
		}
	}
	var roleDomID string
	if roleDomID, err = qualifierDomainID(ctx, client, f.roleDomain, ""); err != nil {
		return
	}
	roleID, err = resolveRoleID(ctx, client, roleNameOrID, roleDomID)
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
	user        string
	group       string
	project     string
	domain      string
	userDomain  string
	groupDomain string
	names       bool
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
	fl.StringVar(&f.project, "project", "", "filter by project scope (name or ID)")
	fl.StringVar(&f.domain, "domain", "", "filter by domain scope (name or ID)")
	fl.StringVar(&f.userDomain, "user-domain", "", "domain owning --user (name or ID)")
	fl.StringVar(&f.groupDomain, "group-domain", "", "domain owning --group (name or ID)")
	fl.BoolVar(&f.names, "names", false, "display names instead of IDs (requires keystone 3.6+)")
	return cmd
}

func runRoleAssignmentList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *assignmentListFlags, w io.Writer) error {
	// Keystone rejects a scope that carries both a project and a domain.
	if f.project != "" && f.domain != "" {
		return fmt.Errorf("--project and --domain are mutually exclusive for role assignment scope")
	}
	// --domain is scope-only here; the actor lookups use their own domain
	// qualifiers so names are resolved in the correct domain.
	domainID, err := resolveDomainID(ctx, client, f.domain)
	if err != nil {
		return err
	}
	projectID, err := resolveProjectID(ctx, client, f.project, domainID)
	if err != nil {
		return err
	}
	userDomID, err := resolveDomainID(ctx, client, f.userDomain)
	if err != nil {
		return err
	}
	userID, err := resolveUserID(ctx, client, f.user, userDomID)
	if err != nil {
		return err
	}
	groupDomID, err := resolveDomainID(ctx, client, f.groupDomain)
	if err != nil {
		return err
	}
	groupID, err := resolveGroupID(ctx, client, f.group, groupDomID)
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

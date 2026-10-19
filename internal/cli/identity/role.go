package identity

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/roles"
	"github.com/gophercloud/gophercloud/v2/pagination"
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
		newRoleCreateCommand(a, o),
		newRoleSetCommand(a, o),
		newRoleDeleteCommand(a, o),
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
	t := output.Table{Columns: []string{"ID", "Name", colDomainID}, Rows: make([][]any, 0, len(all))}
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
		[]string{"ID", "Name", colDomainID, "Description"},
		[]any{r.ID, r.Name, r.DomainID, r.Description})
}

// grantFlags carries the actor (user/group) and scope (project/domain/system)
// shared by "role add" and "role remove".
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
	system  string

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
	fl.StringVar(&f.system, flagSystem, "", helpSystemGrant)
	fl.StringVar(&f.userDomain, flagUserDomain, "", "domain owning --user (name or ID)")
	fl.StringVar(&f.groupDomain, "group-domain", "", "domain owning --group (name or ID)")
	fl.StringVar(&f.projectDomain, "project-domain", "", "domain owning --project (name or ID)")
	fl.StringVar(&f.roleDomain, "role-domain", "", "domain owning the role (name or ID)")
}

// systemScope validates a --system value. Keystone has one system target,
// "all"; anything else is a 404 from the server, so it is refused up front.
func systemScope(v string) (bool, error) {
	switch v {
	case "":
		return false, nil
	case systemAll:
		return true, nil
	}
	return false, fmt.Errorf("--system %q: the only system scope is %q", v, systemAll)
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

// grant is a resolved role assignment target: the role, exactly one actor and
// exactly one scope.
type grant struct {
	roleID, userID, groupID, projectID, domainID string
	system                                       bool
}

// resolveGrant resolves the role plus the actor/scope IDs and validates the xor
// constraints keystone enforces (exactly one actor, exactly one scope).
func resolveGrant(ctx context.Context, client *gophercloud.ServiceClient, roleNameOrID string, f *grantFlags) (grant, error) {
	var g grant
	if (f.user == "") == (f.group == "") {
		return g, fmt.Errorf("exactly one of --user or --group is required")
	}
	system, err := systemScope(f.system)
	if err != nil {
		return g, err
	}
	if scopes := countSet(f.project != "", f.domain != "", system); scopes != 1 {
		return g, fmt.Errorf("exactly one of --project, --domain or --system is required")
	}
	g.system = system

	// Scope domain (only meaningful for a domain-scoped grant). It is the
	// fallback qualifier for the actor lookups so domain-scoped grants keep
	// resolving actors within that domain.
	if g.domainID, err = resolveDomainID(ctx, client, f.domain); err != nil {
		return g, err
	}
	if f.project != "" {
		projDomID, err := qualifierDomainID(ctx, client, f.projectDomain, "")
		if err != nil {
			return g, err
		}
		if g.projectID, err = resolveProjectID(ctx, client, f.project, projDomID); err != nil {
			return g, err
		}
	}
	if g.userID, g.groupID, err = resolveActor(ctx, client, f.user, f.userDomain, f.group, f.groupDomain, g.domainID); err != nil {
		return g, err
	}
	roleDomID, err := qualifierDomainID(ctx, client, f.roleDomain, "")
	if err != nil {
		return g, err
	}
	g.roleID, err = resolveRoleID(ctx, client, roleNameOrID, roleDomID)
	return g, err
}

// resolveActor resolves whichever of --user/--group is set, each within its own
// qualifier or, absent one, within fallbackDomainID.
func resolveActor(ctx context.Context, client *gophercloud.ServiceClient,
	user, userDomain, group, groupDomain, fallbackDomainID string,
) (userID, groupID string, err error) {
	if user != "" {
		userDomID, err := qualifierDomainID(ctx, client, userDomain, fallbackDomainID)
		if err != nil {
			return "", "", err
		}
		userID, err = resolveUserID(ctx, client, user, userDomID)
		if err != nil {
			return "", "", err
		}
	}
	if group != "" {
		groupDomID, err := qualifierDomainID(ctx, client, groupDomain, fallbackDomainID)
		if err != nil {
			return "", "", err
		}
		groupID, err = resolveGroupID(ctx, client, group, groupDomID)
		if err != nil {
			return "", "", err
		}
	}
	return userID, groupID, nil
}

// countSet counts the true values.
func countSet(vals ...bool) int {
	n := 0
	for _, v := range vals {
		if v {
			n++
		}
	}
	return n
}

func newRoleAddCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &grantFlags{}
	cmd := &cobra.Command{
		Use:   "add <role>",
		Short: "Grant a role to a user or group on a project, domain or the system",
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
	g, err := resolveGrant(ctx, client, roleNameOrID, f)
	if err != nil {
		return err
	}
	opts := roles.AssignOpts{UserID: g.userID, GroupID: g.groupID, ProjectID: g.projectID, DomainID: g.domainID, System: g.system}
	if err := roles.Assign(ctx, client, g.roleID, opts).ExtractErr(); err != nil {
		return fmt.Errorf("granting role %q: %w", roleNameOrID, err)
	}
	return nil
}

func newRoleRemoveCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &grantFlags{}
	cmd := &cobra.Command{
		Use:   "remove <role>",
		Short: "Revoke a role from a user or group on a project, domain or the system",
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
	g, err := resolveGrant(ctx, client, roleNameOrID, f)
	if err != nil {
		return err
	}
	opts := roles.UnassignOpts{UserID: g.userID, GroupID: g.groupID, ProjectID: g.projectID, DomainID: g.domainID, System: g.system}
	if err := roles.Unassign(ctx, client, g.roleID, opts).ExtractErr(); err != nil {
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
	system      string
	role        string
	userDomain  string
	groupDomain string
	roleDomain  string
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
	fl.StringVar(&f.system, flagSystem, "", "filter by system scope (only \"all\")")
	fl.StringVar(&f.role, "role", "", "filter by role (name or ID)")
	fl.StringVar(&f.userDomain, flagUserDomain, "", "domain owning --user (name or ID)")
	fl.StringVar(&f.groupDomain, "group-domain", "", "domain owning --group (name or ID)")
	fl.StringVar(&f.roleDomain, "role-domain", "", "domain owning --role (name or ID)")
	fl.BoolVar(&f.names, "names", false, "display names instead of IDs (requires keystone 3.6+)")
	return cmd
}

func runRoleAssignmentList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *assignmentListFlags, w io.Writer) error {
	opts, err := assignmentListOpts(ctx, client, f)
	if err != nil {
		return err
	}
	pages, err := roles.ListAssignments(client, opts).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing role assignments: %w", err)
	}
	all, err := roles.ExtractRoleAssignments(pages)
	if err != nil {
		return fmt.Errorf("parsing role assignment list: %w", err)
	}
	extra, err := extractAssignmentExtras(pages)
	if err != nil {
		return fmt.Errorf("parsing role assignment list: %w", err)
	}
	t := output.Table{
		Columns: []string{"Role", "User", "Group", "Project", "Domain", "System", "Inherited"},
		Rows:    make([][]any, 0, len(all)),
	}
	for i, ra := range all {
		var x assignmentExtra
		if i < len(extra) {
			x = extra[i]
		}
		role := ra.Role.ID
		user := ra.User.ID
		group := ra.Group.ID
		project := ra.Scope.Project.ID
		domain := ra.Scope.Domain.ID
		if f.names {
			// Upstream qualifies every name but the scope domain's own with the
			// domain it lives in: admin@Default.
			role = qualified(ra.Role.Name, x.Role.Domain.Name, role)
			user = qualified(ra.User.Name, ra.User.Domain.Name, user)
			group = qualified(ra.Group.Name, ra.Group.Domain.Name, group)
			project = qualified(ra.Scope.Project.Name, ra.Scope.Project.Domain.Name, project)
			domain = firstNonEmpty(ra.Scope.Domain.Name, domain)
		}
		system := ""
		if ra.Scope.System != nil && ra.Scope.System.All {
			system = "all"
		}
		t.Rows = append(t.Rows, []any{role, user, group, project, domain, system, x.Scope.InheritedTo != ""})
	}
	return o.WriteList(w, t)
}

// assignmentListOpts resolves the filters of "role assignment list" into the
// query keystone takes.
func assignmentListOpts(ctx context.Context, client *gophercloud.ServiceClient, f *assignmentListFlags) (roles.ListAssignmentsOpts, error) {
	var opts roles.ListAssignmentsOpts
	system, err := systemScope(f.system)
	if err != nil {
		return opts, err
	}
	// Keystone rejects a scope that carries more than one target.
	if countSet(f.project != "", f.domain != "", system) > 1 {
		return opts, fmt.Errorf("--project, --domain and --system are mutually exclusive for role assignment scope")
	}
	if system {
		opts.ScopeSystem = systemAll
	}
	// --domain is scope-only here; the actor lookups use their own domain
	// qualifiers so names are resolved in the correct domain.
	if opts.ScopeDomainID, err = resolveDomainID(ctx, client, f.domain); err != nil {
		return opts, err
	}
	if opts.ScopeProjectID, err = resolveProjectID(ctx, client, f.project, opts.ScopeDomainID); err != nil {
		return opts, err
	}
	userDomID, err := resolveDomainID(ctx, client, f.userDomain)
	if err != nil {
		return opts, err
	}
	if opts.UserID, err = resolveUserID(ctx, client, f.user, userDomID); err != nil {
		return opts, err
	}
	groupDomID, err := resolveDomainID(ctx, client, f.groupDomain)
	if err != nil {
		return opts, err
	}
	if opts.GroupID, err = resolveGroupID(ctx, client, f.group, groupDomID); err != nil {
		return opts, err
	}
	if f.role != "" {
		roleDomID, err := resolveDomainID(ctx, client, f.roleDomain)
		if err != nil {
			return opts, err
		}
		if opts.RoleID, err = resolveRoleID(ctx, client, f.role, roleDomID); err != nil {
			return opts, err
		}
	}
	if f.names {
		opts.IncludeNames = &f.names
	}
	return opts, nil
}

// assignmentExtra is what gophercloud's RoleAssignment leaves out: the
// OS-INHERIT marker on an inherited assignment's scope, and a domain-specific
// role's domain.
type assignmentExtra struct {
	Role struct {
		Domain roles.Domain `json:"domain"`
	} `json:"role"`
	Scope struct {
		InheritedTo string `json:"OS-INHERIT:inherited_to"`
	} `json:"scope"`
}

// extractAssignmentExtras reads assignmentExtra from the same page, in order.
func extractAssignmentExtras(page pagination.Page) ([]assignmentExtra, error) {
	p, ok := page.(roles.RoleAssignmentPage)
	if !ok {
		return nil, fmt.Errorf("unexpected role assignment page type %T", page)
	}
	var out []assignmentExtra
	if err := p.ExtractIntoSlicePtr(&out, "role_assignments"); err != nil {
		return nil, err
	}
	return out, nil
}

// qualified renders name@domain, or the fallback when keystone sent no name.
func qualified(name, domain, fallback string) string {
	switch {
	case name == "":
		return fallback
	case domain == "":
		return name
	}
	return name + "@" + domain
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

package identity

import (
	"context"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/roles"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
	"github.com/ftarasenko/go-openstackclient/internal/output"
	identityapi "github.com/ftarasenko/go-openstackclient/pkg/openstack/identity"
)

// Role writes and the role-inference (implied role) rules.
//
// Flag names follow upstream OSC (`openstack role ...`, `openstack implied role
// ...`). UNVERIFIED against KeyStack docs (https://docs.keystack.ru/ returned
// HTTP 403 at implementation time); falls back to upstream OSC semantics.

// --- role create ------------------------------------------------------------

type roleCreateFlags struct {
	description string
	domain      string
	orShow      bool
}

func newRoleCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &roleCreateFlags{}
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new role",
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
			return runRoleCreate(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.description, "description", "", "description of the role")
	fl.StringVar(&f.domain, "domain", "", helpDomainRole)
	fl.BoolVar(&f.orShow, "or-show", false, "return the existing role if it already exists")
	return cmd
}

// runRoleCreate creates the role, honouring --or-show. Keystone answers a
// duplicate name with 409; upstream turns that into a show of the existing role
// so a provisioning script stays idempotent, and the lookup is only attempted on
// that specific outcome.
func runRoleCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	name string, f *roleCreateFlags, w io.Writer,
) error {
	if err := o.CheckColumns(roleColumns...); err != nil {
		return err
	}
	domainID, err := resolveDomainID(ctx, client, f.domain)
	if err != nil {
		return err
	}
	r, err := identityapi.CreateRole(ctx, client, roles.CreateOpts{
		Name:        name,
		Description: f.description,
		DomainID:    domainID,
	})
	if err != nil {
		if f.orShow && gophercloud.ResponseCodeIs(err, 409) {
			return runRoleShow(ctx, client, o, name, w)
		}
		return relabel(err, "creating role %q", name)
	}
	return writeRole(o, w, r)
}

// roleColumns are the fields of a single role.
var roleColumns = []string{"ID", "Name", colDomainID, "Description"}

func writeRole(o *output.Options, w io.Writer, r *roles.Role) error {
	return o.WriteSingle(w,
		roleColumns,
		[]any{r.ID, r.Name, r.DomainID, r.Description})
}

// --- role delete ------------------------------------------------------------

func newRoleDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var domain string
	cmd := &cobra.Command{
		Use:   "delete <role> [<role> ...]",
		Short: "Delete role(s)",
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
			return runRoleDelete(ctx, client, args, domain)
		},
	}
	cmd.Flags().StringVar(&domain, "domain", "", helpDomainRole)
	return cmd
}

func runRoleDelete(ctx context.Context, client *gophercloud.ServiceClient, refs []string, domainNameOrID string) error {
	domainID, err := resolveDomainID(ctx, client, domainNameOrID)
	if err != nil {
		return err
	}
	return batchdelete.Each(refs, func(ref string) error {
		id, err := resolveRoleID(ctx, client, ref, domainID)
		if err != nil {
			return err
		}
		return relabel(identityapi.DeleteRole(ctx, client, id), "deleting role %q", ref)
	})
}

// --- role set ---------------------------------------------------------------

func newRoleSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var name, description, domain string
	cmd := &cobra.Command{
		Use:   "set <role>",
		Short: "Set role properties",
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
			// An explicitly empty --description clears the field, which is not the
			// same as omitting the flag; UpdateOpts.Description is a *string so the
			// two cases stay distinguishable on the wire.
			return runRoleSet(ctx, client, args[0], name, description, domain, cmd.Flags().Changed("description"))
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&name, "name", "", "new role name")
	fl.StringVar(&description, "description", "", "new description")
	fl.StringVar(&domain, "domain", "", helpDomainRole)
	return cmd
}

func runRoleSet(ctx context.Context, client *gophercloud.ServiceClient, ref, name, description, domainNameOrID string, descSet bool) error {
	domainID, err := resolveDomainID(ctx, client, domainNameOrID)
	if err != nil {
		return err
	}
	id, err := resolveRoleID(ctx, client, ref, domainID)
	if err != nil {
		return err
	}
	opts := roles.UpdateOpts{Name: name}
	if descSet {
		opts.Description = &description
	}
	_, err = identityapi.UpdateRole(ctx, client, id, opts)
	return relabel(err, "updating role %q", ref)
}

// --- implied role -----------------------------------------------------------

// newImpliedRoleCommand builds "implied role ...". Upstream spells the noun as
// two words, so it is modelled as a nested parent for unambiguous resolution —
// the same treatment as "floating ip" and "security group rule".
func newImpliedRoleCommand(a *auth.Options, o *output.Options) *cobra.Command {
	role := &cobra.Command{Use: "role", Short: "Manage role-inference rules"}
	role.AddCommand(
		newImpliedRoleCreateCommand(a, o),
		newImpliedRoleDeleteCommand(a, o),
		newImpliedRoleListCommand(a, o),
	)
	cmd := &cobra.Command{Use: "implied", Short: "Manage implied roles"}
	cmd.AddCommand(role)
	return cmd
}

func newImpliedRoleCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var implied string
	cmd := &cobra.Command{
		Use:   "create <role>",
		Short: "Create a role-inference rule: <role> implies --implied-role",
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
			return runImpliedRoleCreate(ctx, client, o, args[0], implied, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&implied, flagImpliedRole, "", "role implied by the prior role (name or ID)")
	_ = cmd.MarkFlagRequired(flagImpliedRole)
	return cmd
}

// impliedRoleColumns are the fields implied role create renders.
var impliedRoleColumns = []string{"Prior Role ID", "Prior Role Name", "Implied Role ID", "Implied Role Name"}

func runImpliedRoleCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	priorRef, impliedRef string, w io.Writer,
) error {
	if err := o.CheckColumns(impliedRoleColumns...); err != nil {
		return err
	}
	priorID, impliedID, err := resolveInferencePair(ctx, client, priorRef, impliedRef)
	if err != nil {
		return err
	}
	rule, err := identityapi.CreateRoleInferenceRule(ctx, client, priorID, impliedID)
	if err != nil {
		return relabel(err, "creating role-inference rule %q implies %q", priorRef, impliedRef)
	}
	inf := rule.RoleInference
	return o.WriteSingle(w,
		impliedRoleColumns,
		[]any{inf.PriorRole.ID, inf.PriorRole.Name, inf.ImpliedRole.ID, inf.ImpliedRole.Name})
}

func newImpliedRoleDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var implied string
	cmd := &cobra.Command{
		Use:   "delete <role>",
		Short: "Delete a role-inference rule",
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
			return runImpliedRoleDelete(ctx, client, args[0], implied)
		},
	}
	cmd.Flags().StringVar(&implied, flagImpliedRole, "", "role implied by the prior role (name or ID)")
	_ = cmd.MarkFlagRequired(flagImpliedRole)
	return cmd
}

func runImpliedRoleDelete(ctx context.Context, client *gophercloud.ServiceClient, priorRef, impliedRef string) error {
	priorID, impliedID, err := resolveInferencePair(ctx, client, priorRef, impliedRef)
	if err != nil {
		return err
	}
	return relabel(identityapi.DeleteRoleInferenceRule(ctx, client, priorID, impliedID),
		"deleting role-inference rule %q implies %q", priorRef, impliedRef)
}

func newImpliedRoleListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List role-inference rules",
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
			return runImpliedRoleList(ctx, client, o, cmd.OutOrStdout())
		},
	}
}

// runImpliedRoleList flattens keystone's nested shape — one object per prior
// role carrying every role it implies — into one row per (prior, implied) pair,
// matching upstream's lister.
func runImpliedRoleList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, w io.Writer) error {
	list, err := identityapi.ListRoleInferenceRules(ctx, client)
	if err != nil {
		return err
	}
	t := output.Table{Columns: []string{"Prior Role ID", "Prior Role Name", "Implied Role ID", "Implied Role Name"}}
	for _, rule := range list {
		for _, implied := range rule.ImpliedRoles {
			t.Rows = append(t.Rows, []any{rule.PriorRole.ID, rule.PriorRole.Name, implied.ID, implied.Name})
		}
	}
	return o.WriteList(w, t)
}

func resolveInferencePair(ctx context.Context, client *gophercloud.ServiceClient, priorRef, impliedRef string) (string, string, error) {
	priorID, err := resolveRoleID(ctx, client, priorRef, "")
	if err != nil {
		return "", "", err
	}
	impliedID, err := resolveRoleID(ctx, client, impliedRef, "")
	if err != nil {
		return "", "", err
	}
	return priorID, impliedID, nil
}

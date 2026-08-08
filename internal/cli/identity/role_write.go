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

// Role writes and the role-inference (implied role) rules.
//
// Flag names follow upstream OSC (`openstack role ...`, `openstack implied role
// ...`). UNVERIFIED against KeyStack docs (https://docs.keystack.ru/ returned
// HTTP 403 at implementation time); falls back to upstream OSC semantics.

// --- role create ------------------------------------------------------------

func newRoleCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var description, domain string
	var orShow bool
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
			return runRoleCreate(ctx, client, o, args[0], description, domain, orShow, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&description, "description", "", "description of the role")
	fl.StringVar(&domain, "domain", "", "domain the role belongs to (name or ID)")
	fl.BoolVar(&orShow, "or-show", false, "return the existing role if it already exists")
	return cmd
}

// runRoleCreate creates the role, honouring --or-show. Keystone answers a
// duplicate name with 409; upstream turns that into a show of the existing role
// so a provisioning script stays idempotent, and the lookup is only attempted on
// that specific outcome.
func runRoleCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	name, description, domainNameOrID string, orShow bool, w io.Writer,
) error {
	domainID, err := resolveDomainID(ctx, client, domainNameOrID)
	if err != nil {
		return err
	}
	r, err := roles.Create(ctx, client, roles.CreateOpts{
		Name:        name,
		Description: description,
		DomainID:    domainID,
	}).Extract()
	if err != nil {
		if orShow && gophercloud.ResponseCodeIs(err, 409) {
			return runRoleShow(ctx, client, o, name, w)
		}
		return fmt.Errorf("creating role %q: %w", name, err)
	}
	return writeRole(o, w, r)
}

func writeRole(o *output.Options, w io.Writer, r *roles.Role) error {
	return o.WriteSingle(w,
		[]string{"ID", "Name", "Domain ID", "Description"},
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
	cmd.Flags().StringVar(&domain, "domain", "", "domain the role belongs to (name or ID)")
	return cmd
}

func runRoleDelete(ctx context.Context, client *gophercloud.ServiceClient, refs []string, domainNameOrID string) error {
	domainID, err := resolveDomainID(ctx, client, domainNameOrID)
	if err != nil {
		return err
	}
	for _, ref := range refs {
		id, err := resolveRoleID(ctx, client, ref, domainID)
		if err != nil {
			return err
		}
		if err := roles.Delete(ctx, client, id).ExtractErr(); err != nil {
			return fmt.Errorf("deleting role %q: %w", ref, err)
		}
	}
	return nil
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
	fl.StringVar(&domain, "domain", "", "domain the role belongs to (name or ID)")
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
	if _, err := roles.Update(ctx, client, id, opts).Extract(); err != nil {
		return fmt.Errorf("updating role %q: %w", ref, err)
	}
	return nil
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
	cmd.Flags().StringVar(&implied, "implied-role", "", "role implied by the prior role (name or ID)")
	_ = cmd.MarkFlagRequired("implied-role")
	return cmd
}

func runImpliedRoleCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	priorRef, impliedRef string, w io.Writer,
) error {
	priorID, impliedID, err := resolveInferencePair(ctx, client, priorRef, impliedRef)
	if err != nil {
		return err
	}
	rule, err := roles.CreateRoleInferenceRule(ctx, client, priorID, impliedID).Extract()
	if err != nil {
		return fmt.Errorf("creating role-inference rule %q implies %q: %w", priorRef, impliedRef, err)
	}
	inf := rule.RoleInference
	return o.WriteSingle(w,
		[]string{"Prior Role ID", "Prior Role Name", "Implied Role ID", "Implied Role Name"},
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
	cmd.Flags().StringVar(&implied, "implied-role", "", "role implied by the prior role (name or ID)")
	_ = cmd.MarkFlagRequired("implied-role")
	return cmd
}

func runImpliedRoleDelete(ctx context.Context, client *gophercloud.ServiceClient, priorRef, impliedRef string) error {
	priorID, impliedID, err := resolveInferencePair(ctx, client, priorRef, impliedRef)
	if err != nil {
		return err
	}
	if err := roles.DeleteRoleInferenceRule(ctx, client, priorID, impliedID).ExtractErr(); err != nil {
		return fmt.Errorf("deleting role-inference rule %q implies %q: %w", priorRef, impliedRef, err)
	}
	return nil
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
	list, err := roles.ListRoleInferenceRules(ctx, client).Extract()
	if err != nil {
		return fmt.Errorf("listing role-inference rules: %w", err)
	}
	t := output.Table{Columns: []string{"Prior Role ID", "Prior Role Name", "Implied Role ID", "Implied Role Name"}}
	for _, rule := range list.RoleInferenceRuleList {
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

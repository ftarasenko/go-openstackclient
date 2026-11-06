package keymanager

import (
	"context"
	"fmt"
	"io"
	"slices"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/keymanager/v1/acls"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/koc/internal/auth"
	"github.com/ftarasenko/koc/internal/output"
	kmapi "github.com/ftarasenko/koc/pkg/openstack/keymanager"
)

func newACLCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "acl",
		Short: "Manage the access control lists of secrets and containers",
	}
	user := &cobra.Command{
		Use:   "user",
		Short: "Add or remove the users of an ACL",
	}
	user.AddCommand(
		newACLWriteCommand(a, o, "add", "Add users to the ACL of a secret or container", aclVerb{aclAddUsers, true}),
		newACLWriteCommand(a, o, "remove", "Remove users from the ACL of a secret or container", aclVerb{aclRemoveUsers, true}),
	)
	cmd.AddCommand(
		newACLGetCommand(a, o),
		newACLWriteCommand(a, o, "submit", "Replace the ACL of a secret or container", aclVerb{change: aclSubmit}),
		newACLDeleteCommand(a),
		user,
	)
	return cmd
}

// aclEntity is the secret or container an ACL command names. Upstream tells
// the two apart from the href alone ("secrets" or "containers" in its path), so
// a bare UUID is refused: it could name either.
type aclEntity struct {
	ref       string // the href as given, without a trailing "/"
	id        string
	container bool
}

// parseACLRef reads the href of a secret or container (kmapi.ParseACLRef).
func parseACLRef(ref string) (aclEntity, error) {
	t, err := kmapi.ParseACLRef(ref)
	return aclEntity{ref: t.Ref, id: t.ID, container: t.Container}, err
}

func (e aclEntity) target() kmapi.ACLTarget {
	return kmapi.ACLTarget{Ref: e.ref, ID: e.id, Container: e.container}
}

func (e aclEntity) get(ctx context.Context, client *gophercloud.ServiceClient) (acls.ACL, error) {
	return kmapi.GetACL(ctx, client, e.target())
}

func (e aclEntity) set(ctx context.Context, client *gophercloud.ServiceClient, opts acls.SetOpts) error {
	return kmapi.SetACL(ctx, client, e.target(), opts)
}

func (e aclEntity) render(o *output.Options, acl acls.ACL, w io.Writer) error {
	refColumn := "Secret ACL Ref"
	if e.container {
		refColumn = "Container ACL Ref"
	}
	t := output.Table{Columns: []string{"Operation Type", "Project Access", "Users", "Created", "Updated", refColumn}}
	ops := make([]string, 0, len(acl))
	for op := range acl {
		ops = append(ops, op)
	}
	slices.Sort(ops)
	for _, op := range ops {
		d := acl[op]
		users := d.Users
		if users == nil {
			users = []string{}
		}
		t.Rows = append(t.Rows, []any{op, d.ProjectAccess, users, d.Created, d.Updated, e.ref + "/acl"})
	}
	return o.WriteList(w, t)
}

func newACLGetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "get <uri>",
		Short: "Retrieve the ACL of a secret or container by its href",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			e, err := parseACLRef(args[0])
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newKeyManagerClient(ctx, a)
			if err != nil {
				return err
			}
			return runACLGet(ctx, client, o, e, cmd.OutOrStdout())
		},
	}
}

func runACLGet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, e aclEntity, w io.Writer) error {
	acl, err := e.get(ctx, client)
	if err != nil {
		return err
	}
	return e.render(o, acl, w)
}

// aclFlags are the per-ACL options of `acl submit` and `acl user add|remove`,
// named as python-barbicanclient's ArgMixin.add_per_acl_args names them;
// UNVERIFIED against KeyStack. projectAccess is nil when neither
// --project-access nor --no-project-access was given.
type aclFlags struct {
	users         []string
	projectAccess *bool
	operationType string
}

// aclVerb is one ACL write. change turns the flags and the ACL currently on the
// server into the complete ACL to PUT — barbican's ACL PATCH is not used: like
// upstream, every write replaces the whole document — and readsServer says
// whether change needs that current ACL at all.
type aclVerb struct {
	change      func(server acls.ACL, f *aclFlags) acls.SetOpts
	readsServer bool
}

// change is the flags as a kmapi.ACLChange.
func (f *aclFlags) change() kmapi.ACLChange {
	return kmapi.ACLChange{OperationType: f.operationType, Users: f.users, ProjectAccess: f.projectAccess}
}

// aclSubmit replaces the ACL with exactly the flags' operation type
// (kmapi.SubmitACLOpts).
func aclSubmit(_ acls.ACL, f *aclFlags) acls.SetOpts { return kmapi.SubmitACLOpts(f.change()) }

// aclAddUsers adds the flags' users to the ACL on the server
// (kmapi.AddACLUsersOpts).
func aclAddUsers(server acls.ACL, f *aclFlags) acls.SetOpts {
	return kmapi.AddACLUsersOpts(server, f.change())
}

// aclRemoveUsers drops the flags' users from the ACL on the server
// (kmapi.RemoveACLUsersOpts).
func aclRemoveUsers(server acls.ACL, f *aclFlags) acls.SetOpts {
	return kmapi.RemoveACLUsersOpts(server, f.change())
}

func newACLWriteCommand(a *auth.Options, o *output.Options, use, short string, verb aclVerb) *cobra.Command {
	f := &aclFlags{}
	var projectAccess, noProjectAccess bool
	cmd := &cobra.Command{
		Use:   use + " <uri>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			e, err := parseACLRef(args[0])
			if err != nil {
				return err
			}
			switch {
			case projectAccess:
				f.projectAccess = &projectAccess
			case noProjectAccess:
				f.projectAccess = new(bool)
			}
			ctx := cmd.Context()
			client, err := newKeyManagerClient(ctx, a)
			if err != nil {
				return err
			}
			return runACLWrite(ctx, client, o, e, f, verb, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringArrayVarP(&f.users, "user", "u", nil, "a Keystone user ID for the ACL; repeatable")
	fl.BoolVar(&projectAccess, "project-access", false, "enable project access")
	fl.BoolVar(&noProjectAccess, "no-project-access", false, "disable project access")
	fl.StringVarP(&f.operationType, "operation-type", "o", "read", "the barbican operation the ACL is for: read")
	cmd.MarkFlagsMutuallyExclusive("project-access", "no-project-access")
	return cmd
}

func runACLWrite(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, e aclEntity, f *aclFlags, verb aclVerb, w io.Writer) error {
	if f.operationType != "read" {
		return fmt.Errorf("--operation-type %q: the only operation type is read", f.operationType)
	}
	var server acls.ACL
	if verb.readsServer {
		var err error
		if server, err = e.get(ctx, client); err != nil {
			return err
		}
	}
	if err := e.set(ctx, client, verb.change(server, f)); err != nil {
		return err
	}
	return runACLGet(ctx, client, o, e, w)
}

func newACLDeleteCommand(a *auth.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <uri>",
		Short: "Delete the ACL of a secret or container by its href",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := parseACLRef(args[0])
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newKeyManagerClient(ctx, a)
			if err != nil {
				return err
			}
			return runACLDelete(ctx, client, e)
		},
	}
}

func runACLDelete(ctx context.Context, client *gophercloud.ServiceClient, e aclEntity) error {
	return kmapi.DeleteACL(ctx, client, e.target())
}

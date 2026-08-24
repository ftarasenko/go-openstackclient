package compute

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/keypairs"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/roles"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
	"github.com/ftarasenko/go-openstackclient/internal/cli/resolve"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Flag names and semantics below follow upstream python-openstackclient
// (`openstack keypair ...`). The KeyStack command reference at
// https://docs.keystack.ru/ was not reachable at implementation time (HTTP
// 403), so these are UNVERIFIED against KeyStack and fall back to upstream OSC
// semantics — see the PR description.

// newKeypairCommand builds "keypair ...".
func newKeypairCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "keypair",
		Short: "Manage compute SSH keypairs",
	}
	cmd.AddCommand(
		newKeypairListCommand(a, o),
		newKeypairShowCommand(a, o),
		newKeypairCreateCommand(a, o),
		newKeypairDeleteCommand(a, o),
	)
	return cmd
}

// ---------------------------------------------------------------------------
// keypair list
// ---------------------------------------------------------------------------

// keypairListFlags holds the owner filters accepted by "keypair list". Nova keys
// keypairs on the user, so --user is a native filter (microversion 2.10+) while
// --project has to be expanded into the project's users first — see
// runKeypairList.
type keypairListFlags struct {
	user          string
	userDomain    string
	project       string
	projectDomain string
}

func newKeypairListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &keypairListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List keypairs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			if f.user != "" && f.project != "" {
				return fmt.Errorf("--user and --project are mutually exclusive")
			}
			ctx := cmd.Context()
			client, session, err := newComputeSession(ctx, a)
			if err != nil {
				return err
			}
			userIDs, err := keypairOwners(ctx, session, f)
			if err != nil {
				return err
			}
			return runKeypairList(ctx, client, o, userIDs, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.user, "user", "", "list keypairs owned by this user (name or ID; nova >= 2.10, admin)")
	fl.StringVar(&f.userDomain, "user-domain", "", "domain owning --user, to disambiguate the name (name or ID)")
	fl.StringVar(&f.project, "project", "", "list keypairs of every user with a role on this project (name or ID, admin)")
	fl.StringVar(&f.projectDomain, "project-domain", "", "domain owning --project, to disambiguate the name (name or ID)")
	return cmd
}

// keypairOwners turns --user / --project into the set of nova user IDs to list
// keypairs for. Nil means "the caller's own keypairs" (no user_id filter).
//
// --project has no nova equivalent: keypairs belong to users, not projects. It is
// expanded through keystone's role assignments — the users with a role on the
// project — which is how upstream OSC presents the same flag. That means one
// keypair list per user, so the fan-out is proportional to the project's user
// count.
func keypairOwners(ctx context.Context, session *auth.Client, f *keypairListFlags) ([]string, error) {
	if f.user == "" && f.project == "" {
		return nil, nil
	}
	identity, err := session.Identity()
	if err != nil {
		return nil, err
	}
	if f.user != "" {
		userID, uerr := resolve.UserIDInDomain(ctx, identity, f.user, f.userDomain)
		if uerr != nil {
			return nil, uerr
		}
		return []string{userID}, nil
	}

	projectID, err := resolve.ProjectIDInDomain(ctx, identity, f.project, f.projectDomain)
	if err != nil {
		return nil, err
	}
	pages, err := roles.ListAssignments(identity, roles.ListAssignmentsOpts{
		ScopeProjectID: projectID,
		Effective:      gophercloud.Enabled,
	}).AllPages(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing users with a role on project %q: %w", f.project, err)
	}
	assignments, err := roles.ExtractRoleAssignments(pages)
	if err != nil {
		return nil, fmt.Errorf("parsing role assignments for project %q: %w", f.project, err)
	}
	seen := make(map[string]bool, len(assignments))
	userIDs := make([]string, 0, len(assignments))
	for _, assignment := range assignments {
		// Effective assignments name a user even when the grant came via a group,
		// but an assignment scoped to a group with no members has no user.
		if assignment.User.ID == "" || seen[assignment.User.ID] {
			continue
		}
		seen[assignment.User.ID] = true
		userIDs = append(userIDs, assignment.User.ID)
	}
	if len(userIDs) == 0 {
		return nil, fmt.Errorf("no users have a role on project %q", f.project)
	}
	sort.Strings(userIDs)
	return userIDs, nil
}

// runKeypairList lists keypairs for each owner in userIDs, or the caller's own
// when it is empty. The owning user is added as a column whenever more than one
// user is involved, since otherwise the rows would be indistinguishable.
func runKeypairList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	userIDs []string, w io.Writer,
) error {
	type row struct {
		userID string
		pair   keypairs.KeyPair
	}
	var rows []row

	if len(userIDs) == 0 {
		userIDs = []string{""}
	}
	for _, userID := range userIDs {
		pages, err := keypairs.List(client, keypairs.ListOpts{UserID: userID}).AllPages(ctx)
		if err != nil {
			return fmt.Errorf("listing keypairs: %w", err)
		}
		all, err := keypairs.ExtractKeyPairs(pages)
		if err != nil {
			return fmt.Errorf("parsing keypair list: %w", err)
		}
		for _, k := range all {
			rows = append(rows, row{userID: userID, pair: k})
		}
	}

	cols := []string{"Name", "Fingerprint", "Type"}
	perUser := len(userIDs) > 1
	if perUser {
		cols = append(cols, colUserID)
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(rows))}
	for _, r := range rows {
		out := []any{r.pair.Name, r.pair.Fingerprint, r.pair.Type}
		if perUser {
			// Prefer the ID nova reports; fall back to the one we queried for, since
			// older microversions omit it from the list response.
			userID := r.pair.UserID
			if userID == "" {
				userID = r.userID
			}
			out = append(out, userID)
		}
		t.Rows = append(t.Rows, out)
	}
	return o.WriteList(w, t)
}

// ---------------------------------------------------------------------------
// keypair show
// ---------------------------------------------------------------------------

type keypairShowFlags struct {
	publicKey  bool
	user       string
	userDomain string
}

func newKeypairShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &keypairShowFlags{}
	cmd := &cobra.Command{
		Use:   "show <name>",
		Short: "Display keypair details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, session, err := newComputeSession(ctx, a)
			if err != nil {
				return err
			}
			userID := ""
			if f.user != "" {
				identity, ierr := session.Identity()
				if ierr != nil {
					return ierr
				}
				userID, ierr = resolve.UserIDInDomain(ctx, identity, f.user, f.userDomain)
				if ierr != nil {
					return ierr
				}
			}
			return runKeypairShow(ctx, client, o, args[0], userID, f.publicKey, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&f.publicKey, "public-key", false, "print only the public key, unformatted, for piping into a file")
	fl.StringVar(&f.user, "user", "", "show a keypair owned by this user (name or ID; nova >= 2.10, admin)")
	fl.StringVar(&f.userDomain, "user-domain", "", "domain owning --user, to disambiguate the name (name or ID)")
	return cmd
}

// runKeypairShow renders the keypair, or with publicKeyOnly emits just the public
// key verbatim so it can be redirected into an authorized_keys file — bypassing
// the table layer the way "keypair create" does for a generated private key.
func runKeypairShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	name, userID string, publicKeyOnly bool, w io.Writer,
) error {
	k, err := keypairs.Get(ctx, client, name, keypairs.GetOpts{UserID: userID}).Extract()
	if err != nil {
		return fmt.Errorf("showing keypair %q: %w", name, err)
	}
	if publicKeyOnly {
		if _, err := fmt.Fprintln(w, k.PublicKey); err != nil {
			return fmt.Errorf("writing public key: %w", err)
		}
		return nil
	}
	fields := []string{"Name", "Fingerprint", "Type", colUserID, "Public Key"}
	values := []any{k.Name, k.Fingerprint, k.Type, k.UserID, k.PublicKey}
	return o.WriteSingle(w, fields, values)
}

// ---------------------------------------------------------------------------
// keypair create
// ---------------------------------------------------------------------------

type keypairCreateFlags struct {
	publicKey string // path to an OpenSSH public key file to import
}

func newKeypairCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &keypairCreateFlags{}
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create or import a keypair",
		Long: "Create a new keypair. Without --public-key, nova generates the pair and " +
			"the private key is printed to stdout (save it; it is not retrievable later). " +
			"With --public-key FILE, the given OpenSSH public key is imported instead.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newComputeClient(ctx, a)
			if err != nil {
				return err
			}
			return runKeypairCreate(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&f.publicKey, "public-key", "", "path to a public key file to import (otherwise a new key is generated)")
	return cmd
}

func runKeypairCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, name string, f *keypairCreateFlags, w io.Writer) error {
	opts := keypairs.CreateOpts{Name: name}
	imported := f.publicKey != ""
	if imported {
		data, err := os.ReadFile(f.publicKey)
		if err != nil {
			return fmt.Errorf("reading public key file %q: %w", f.publicKey, err)
		}
		opts.PublicKey = string(data)
	}

	k, err := keypairs.Create(ctx, client, opts).Extract()
	if err != nil {
		return fmt.Errorf("creating keypair %q: %w", name, err)
	}

	// When nova generates the key, the private key is only returned once. Emit
	// it verbatim to stdout so it can be captured/redirected to a file.
	if !imported && k.PrivateKey != "" {
		if _, err := fmt.Fprint(w, k.PrivateKey); err != nil {
			return fmt.Errorf("writing private key: %w", err)
		}
		return nil
	}

	fields := []string{"Name", "Fingerprint", "Type", colUserID}
	values := []any{k.Name, k.Fingerprint, k.Type, k.UserID}
	return o.WriteSingle(w, fields, values)
}

// ---------------------------------------------------------------------------
// keypair delete
// ---------------------------------------------------------------------------

func newKeypairDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <name> [<name> ...]",
		Short: "Delete keypair(s)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newComputeClient(ctx, a)
			if err != nil {
				return err
			}
			return runKeypairDelete(ctx, client, args, cmd.OutOrStdout())
		},
	}
	return cmd
}

func runKeypairDelete(ctx context.Context, client *gophercloud.ServiceClient, names []string, _ io.Writer) error {
	return batchdelete.Each(names, func(name string) error {
		if err := keypairs.Delete(ctx, client, name, keypairs.DeleteOpts{}).ExtractErr(); err != nil {
			return fmt.Errorf("deleting keypair %q: %w", name, err)
		}
		return nil
	})
}

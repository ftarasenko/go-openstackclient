package compute

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/keypairs"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
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

func newKeypairListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List keypairs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newComputeClient(ctx, a)
			if err != nil {
				return err
			}
			return runKeypairList(ctx, client, o, cmd.OutOrStdout())
		},
	}
	return cmd
}

func runKeypairList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, w io.Writer) error {
	pages, err := keypairs.List(client, keypairs.ListOpts{}).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing keypairs: %w", err)
	}
	all, err := keypairs.ExtractKeyPairs(pages)
	if err != nil {
		return fmt.Errorf("parsing keypair list: %w", err)
	}
	t := output.Table{Columns: []string{"Name", "Fingerprint", "Type"}, Rows: make([][]any, 0, len(all))}
	for _, k := range all {
		t.Rows = append(t.Rows, []any{k.Name, k.Fingerprint, k.Type})
	}
	return o.WriteList(w, t)
}

// ---------------------------------------------------------------------------
// keypair show
// ---------------------------------------------------------------------------

func newKeypairShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <name>",
		Short: "Display keypair details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newComputeClient(ctx, a)
			if err != nil {
				return err
			}
			return runKeypairShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
	return cmd
}

func runKeypairShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, name string, w io.Writer) error {
	k, err := keypairs.Get(ctx, client, name, keypairs.GetOpts{}).Extract()
	if err != nil {
		return fmt.Errorf("showing keypair %q: %w", name, err)
	}
	fields := []string{"Name", "Fingerprint", "Type", "User ID", "Public Key"}
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

	fields := []string{"Name", "Fingerprint", "Type", "User ID"}
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
	for _, name := range names {
		if err := keypairs.Delete(ctx, client, name, keypairs.DeleteOpts{}).ExtractErr(); err != nil {
			return fmt.Errorf("deleting keypair %q: %w", name, err)
		}
	}
	return nil
}

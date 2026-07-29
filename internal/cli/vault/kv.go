package vaultcli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
	"github.com/ftarasenko/go-openstackclient/internal/vault"
)

// vaultLong documents the connection flags on the group itself: the global
// --vault-* flags are hidden from `koc --help` (they are advanced knobs for
// --creds-from-vault), but they are this group's primary input, so they have to
// be discoverable here.
const vaultLong = `Read, list and copy HashiCorp Vault KV v2 secrets.

This group is koc-specific (Vault is not an OpenStack service) and does not
authenticate against Keystone: only Vault credentials are used.

The Vault to talk to — and the destination of "kv copy" — is described by the
global flags:

  --vault-addr, --vault-namespace           (env VAULT_ADDR, VAULT_NAMESPACE)
  --vault-token                             (env VAULT_TOKEN, or ~/.vault-token)
  --vault-role-id, --vault-secret-id        (env VAULT_ROLE_ID, VAULT_SECRET_ID)
  --vault-approle-path                      (env VAULT_APPROLE_PATH)
  --vault-kv-mount, --vault-kv-prefix       (env VAULT_KV_MOUNT, VAULT_KV_PREFIX)
  --vault-cacert, --insecure-vault          (env VAULT_CACERT, VAULT_SKIP_VERIFY)

On an LCM cluster node these are auto-discovered from the k0s-system/lcm-config
ConfigMap and the cert-manager/vault-approle Secret, so no flags are needed.

A PATH may be given as "<mount>/a/b" (the Vault CLI form), "/a/b" (absolute), or
"a/b" (relative to --vault-kv-prefix).`

// NewCommand builds the "vault" command group.
func NewCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "vault",
		Short: "Work with HashiCorp Vault KV v2 secrets (koc-specific)",
		Long:  vaultLong,
	}
	cmd.AddCommand(newKVCommand(a, o))
	return cmd
}

// newKVCommand builds the "kv" child of the "vault" parent, giving the two-word
// command "vault kv ...". A separate child keeps room for future non-KV groups
// (vault pki, vault policy).
func newKVCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "kv",
		Short: "Manage KV v2 secrets",
	}
	cmd.AddCommand(newKVCopyCommand(a, o))
	cmd.AddCommand(newKVListCommand(a, o))
	cmd.AddCommand(newKVGetCommand(a, o))
	cmd.AddCommand(newKVExportCommand(a, o))
	cmd.AddCommand(newKVDecryptCommand(o))
	return cmd
}

const kvCopyLong = `Copy KV v2 secrets from one Vault (or path) to another.

Without -r, SRC must be a single secret. With -r, the whole subtree under SRC is
mirrored under DST.

The source defaults to the same Vault as the destination, so copying between two
paths of one Vault needs no extra flags. Point it elsewhere with the
--src-vault-* overrides; each one not given is inherited from the destination.

Only secret data is copied — KV v2 custom_metadata, version history and
delete_version_after are not, so the result is a copy of the current values, not
a replica. Secret values are never printed (use "vault kv get" for that).`

func newKVCopyCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &copyFlags{}
	src := &srcFlags{}
	cmd := &cobra.Command{
		Use:     "copy <src-path> <dst-path>",
		Short:   "Copy KV v2 secrets between Vaults or paths",
		Long:    kvCopyLong,
		Args:    cobra.ExactArgs(2),
		Example: kvCopyExample,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			srcClient, dstClient, err := newVaultClientPair(ctx, a, src)
			if err != nil {
				return err
			}
			// Paths are resolved after the clients are built: cluster
			// auto-discovery may have filled in the mount and prefix.
			opts := copyOptions{
				srcPath: vault.ResolvePath(
					src.str("src-vault-kv-prefix", "VAULT_SRC_PREFIX", src.kvPrefix, a.VaultKVPrefix),
					srcClient.KVMount(), args[0]),
				dstPath:    vault.ResolvePath(a.VaultKVPrefix, dstClient.KVMount(), args[1]),
				copyFlags:  *f,
				srcDisplay: args[0],
			}
			return runKVCopy(ctx, srcClient, dstClient, o, opts, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVarP(&f.recursive, "recursive", "r", false, "copy every secret under the source path")
	fl.BoolVar(&f.dryRun, "dry-run", false, "report what would be copied without writing")
	fl.BoolVar(&f.skipExisting, "skip-existing", false, "leave destination secrets that already exist untouched")
	fl.IntVar(&f.srcVersion, "src-version", 0, "copy this source secret version instead of the latest (single secret only)")
	src.addTo(fl)
	return cmd
}

const kvCopyExample = `  # Within one Vault: copy a deployment's secrets to another prefix
  koc vault kv copy -r deployments/itkey/dev deployments/itkey/e2e

  # From another Vault, previewing first
  koc vault kv copy -r --dry-run \
    --src-vault-addr https://vault.old --src-vault-token s.xxxx \
    --src-vault-kv-mount secret_v2 \
    deployments/itkey/prod deployments/itkey/e2e`

func newKVListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list <path>",
		Short: "List the keys and folders under a KV v2 path",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newVaultClient(ctx, a)
			if err != nil {
				return err
			}
			path := vault.ResolvePath(a.VaultKVPrefix, client.KVMount(), args[0])
			return runKVList(ctx, client, o, path, cmd.OutOrStdout())
		},
	}
	return cmd
}

const kvExportLong = `Export every secret under a path as an encrypted JUnit XML report.

The report is the one the KeyStack e2e pipeline publishes via
artifacts:reports:junit — one test case per secret, so GitLab still shows which
paths exist, which are empty (skipped) and which could not be read (failure) —
except that each payload is encrypted to --recipient instead of embedded in
cleartext. There is no plaintext mode: --recipient is required.

Each secret is sealed with a fresh AES-256-GCM key, itself wrapped to the
recipient's RSA public key with OAEP-SHA256. CI therefore holds only the public
key and a leaked runner cannot read the export; only the holder of the private
key can, with "koc vault kv decrypt".

Secret paths stay readable — they are what makes the report useful — but they are
authenticated: each path is the payload's additional authenticated data, so
moving a payload to another test case makes decryption fail.

A secret named ssl_certificates is expanded one test case per key, so each
certificate is separately visible and separately encrypted.`

const kvExportExample = `  # once, on the operator's machine
  openssl genrsa -out koc-export.key 4096
  openssl rsa -in koc-export.key -pubout -out koc-export.pub

  # in CI, with the public key only
  koc vault kv export deployments/itkey/dev --recipient koc-export.pub -o .junit/vault.xml

  # later, by the key holder
  koc vault kv decrypt .junit/vault.xml --identity koc-export.key`

func newKVExportCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &exportFlags{}
	cmd := &cobra.Command{
		Use:     "export <path>",
		Short:   "Export a subtree as an encrypted JUnit XML report",
		Long:    kvExportLong,
		Example: kvExportExample,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			pub, err := loadRecipient(f.recipient)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newVaultClient(ctx, a)
			if err != nil {
				return err
			}
			w, closeOut, err := openExportOutput(f.output)
			if err != nil {
				return err
			}
			defer func() { _ = closeOut() }()

			path := vault.ResolvePath(a.VaultKVPrefix, client.KVMount(), args[0])
			if err := runKVExport(ctx, client, pub, path, w); err != nil {
				return err
			}
			return closeOut()
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.recipient, "recipient", os.Getenv("KOC_EXPORT_RECIPIENT"),
		"PEM file with the RSA public key (or certificate) to encrypt to (required; env KOC_EXPORT_RECIPIENT)")
	fl.StringVarP(&f.output, "output", "o", "", "write the report here instead of stdout (created 0600)")
	if err := cmd.MarkFlagRequired("recipient"); err != nil {
		panic(err)
	}
	return cmd
}

func newKVDecryptCommand(o *output.Options) *cobra.Command {
	var identity string
	cmd := &cobra.Command{
		Use:   "decrypt <report.xml>",
		Short: "Decrypt a report written by \"vault kv export\"",
		Long: `Decrypt a report written by "vault kv export".

Prints the recovered secrets as Path/Key/Value rows (so -f json/yaml/csv and -c
work). This prints secret values in cleartext, and never writes to a Vault.

Pass "-" to read the report from stdin. This command needs no Vault access.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			priv, err := loadIdentity(identity)
			if err != nil {
				return err
			}
			r := cmd.InOrStdin()
			if args[0] != "-" {
				f, err := os.Open(args[0]) //nolint:gosec // G304: operator-supplied report path
				if err != nil {
					return fmt.Errorf("opening %q: %w", args[0], err)
				}
				defer func() { _ = f.Close() }()
				r = f
			}
			return runKVDecrypt(r, priv, o, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVarP(&identity, "identity", "i", os.Getenv("KOC_EXPORT_IDENTITY"),
		"PEM file with the RSA private key matching the export's recipient (required; env KOC_EXPORT_IDENTITY)")
	if err := cmd.MarkFlagRequired("identity"); err != nil {
		panic(err)
	}
	return cmd
}

func newKVGetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var version int
	cmd := &cobra.Command{
		Use:   "get <path>",
		Short: "Show a KV v2 secret",
		Long:  "Show a KV v2 secret. This prints secret values in cleartext.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newVaultClient(ctx, a)
			if err != nil {
				return err
			}
			path := vault.ResolvePath(a.VaultKVPrefix, client.KVMount(), args[0])
			return runKVGet(ctx, client, o, path, version, cmd.OutOrStdout())
		},
	}
	cmd.Flags().IntVar(&version, "version", 0, "secret version to read (default: latest)")
	return cmd
}

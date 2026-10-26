package keymanager

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/keymanager/v1/secrets"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Flag names used by more than one command in this package, or looked up as
// well as declared.
const (
	flagPayloadContentType     = "payload-content-type"
	flagPayloadContentEncoding = "payload-content-encoding"
	flagPayload                = "payload"
	flagFile                   = "file"
	flagExpiration             = "expiration"
)

// secretColumns are upstream's SecretFormatter columns, in its order.
var secretColumns = []string{
	"Secret href", "Name", "Created", "Status", "Content types",
	"Algorithm", "Bit length", "Secret type", "Mode", "Expiration",
}

func secretRow(s *secrets.Secret) []any {
	var contentTypes any
	if len(s.ContentTypes) > 0 {
		contentTypes = s.ContentTypes
	}
	return []any{
		s.SecretRef, s.Name, s.Created, s.Status, contentTypes,
		s.Algorithm, nonZero(s.BitLength), s.SecretType, s.Mode, s.Expiration,
	}
}

// secretListFlags holds "secret list"'s filters. Flag names follow
// python-barbicanclient's ListSecret; UNVERIFIED against KeyStack.
type secretListFlags struct {
	limit, offset, bitLength int
	name, algorithm, mode    string
	secretType               string
}

func newSecretListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &secretListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List secrets",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newKeyManagerClient(ctx, a)
			if err != nil {
				return err
			}
			return runSecretList(ctx, client, o, f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.IntVarP(&f.limit, "limit", "l", 10, "the number of secrets to list (maximum: 100)")
	fl.IntVarP(&f.offset, "offset", "o", 0, "the page offset")
	fl.StringVarP(&f.name, "name", "n", "", "filter by secret name")
	fl.StringVarP(&f.algorithm, "algorithm", "a", "", "filter by algorithm")
	fl.IntVarP(&f.bitLength, "bit-length", "b", 0, "filter by bit length")
	fl.StringVarP(&f.mode, "mode", "m", "", "filter by algorithm mode")
	fl.StringVarP(&f.secretType, "secret-type", "s", "", "filter by secret type")
	return cmd
}

func runSecretList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *secretListFlags, w io.Writer) error {
	opts := secrets.ListOpts{
		Limit: f.limit, Offset: f.offset, Name: f.name, Alg: f.algorithm,
		Mode: f.mode, Bits: f.bitLength, SecretType: secrets.SecretType(f.secretType),
	}
	page, err := firstPage(ctx, secrets.List(client, opts))
	if err != nil {
		return fmt.Errorf("listing secrets: %w", err)
	}
	t := output.Table{Columns: secretColumns}
	if page == nil {
		return o.WriteList(w, t)
	}
	all, err := secrets.ExtractSecrets(page)
	if err != nil {
		return fmt.Errorf("parsing secret list: %w", err)
	}
	for i := range all {
		t.Rows = append(t.Rows, secretRow(&all[i]))
	}
	return o.WriteList(w, t)
}

// secretGetFlags holds "secret get"'s options. Flag names follow
// python-barbicanclient's GetSecret; UNVERIFIED against KeyStack.
type secretGetFlags struct {
	decrypt, payload   bool
	file               string
	payloadContentType string
}

func newSecretGetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &secretGetFlags{}
	cmd := &cobra.Command{
		Use:   "get <uri>",
		Short: "Retrieve a secret by its URI, or its decrypted payload",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newKeyManagerClient(ctx, a)
			if err != nil {
				return err
			}
			return runSecretGet(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVarP(&f.decrypt, "decrypt", "d", false, "retrieve the unencrypted secret data")
	fl.BoolVarP(&f.payload, flagPayload, "p", false, "retrieve the unencrypted secret data")
	fl.StringVarP(&f.file, flagFile, "F", "", "save the payload to a new file with the given name")
	fl.StringVarP(&f.payloadContentType, flagPayloadContentType, "t", "text/plain", "the content type of the decrypted secret")
	// Upstream spells this one flag with underscores on `secret get` only
	// (`--payload_content_type`), where `secret store` uses hyphens. Both work;
	// the underscored form is hidden.
	fl.StringVar(&f.payloadContentType, "payload_content_type", "text/plain", "alias for --payload-content-type")
	_ = fl.MarkHidden("payload_content_type")
	cmd.MarkFlagsMutuallyExclusive("decrypt", flagPayload, flagFile)
	return cmd
}

func runSecretGet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, f *secretGetFlags, w io.Writer) error {
	id, err := refUUID(ref, "Secret")
	if err != nil {
		return err
	}
	if !f.decrypt && !f.payload && f.file == "" {
		return showSecret(ctx, client, o, id, w)
	}
	payload, err := secrets.GetPayload(ctx, client, id, secrets.GetPayloadOpts{PayloadContentType: f.payloadContentType}).Extract()
	if err != nil {
		return fmt.Errorf("getting payload of secret %s: %w", id, err)
	}
	if f.file != "" {
		return writeNewFile(f.file, payload)
	}
	return o.WriteSingle(w, []string{"Payload"}, []any{string(payload)})
}

// writeNewFile writes data to a file that must not exist yet, as upstream
// refuses to overwrite one.
func writeNewFile(path string, data []byte) error {
	fh, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // G304: operator-supplied --file path
	if errors.Is(err, os.ErrExist) {
		return fmt.Errorf("--file %s: file already exists", path)
	}
	if err != nil {
		return fmt.Errorf("--file: %w", err)
	}
	if _, err := fh.Write(data); err != nil {
		_ = fh.Close()
		return fmt.Errorf("--file %s: %w", path, err)
	}
	return fh.Close()
}

func showSecret(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, id string, w io.Writer) error {
	s, err := secrets.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("getting secret %s: %w", id, err)
	}
	return o.WriteSingle(w, secretColumns, secretRow(s))
}

// secretStoreFlags holds "secret store"'s options. Flag names and defaults
// follow python-barbicanclient's StoreSecret; UNVERIFIED against KeyStack.
type secretStoreFlags struct {
	name, secretType                           string
	payloadContentType, payloadContentEncoding string
	algorithm, mode, expiration                string
	bitLength                                  int
	payload, file                              string
}

func newSecretStoreCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &secretStoreFlags{}
	cmd := &cobra.Command{
		Use:   "store",
		Short: "Store a secret in barbican",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			opts, err := f.createOpts()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newKeyManagerClient(ctx, a)
			if err != nil {
				return err
			}
			return runSecretStore(ctx, client, o, opts, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVarP(&f.name, "name", "n", "", "a human-friendly name")
	fl.StringVarP(&f.secretType, "secret-type", "s", "opaque",
		"the secret type: symmetric, public, private, certificate, passphrase or opaque")
	fl.StringVarP(&f.payloadContentType, flagPayloadContentType, "t", "",
		`the type/format of the secret data; "text/plain" is assumed to be UTF-8`)
	fl.StringVarP(&f.payloadContentEncoding, flagPayloadContentEncoding, "e", "",
		`required if --payload-content-type is "application/octet-stream"`)
	fl.StringVarP(&f.algorithm, "algorithm", "a", "aes", "the algorithm")
	fl.IntVarP(&f.bitLength, "bit-length", "b", 256, "the bit length")
	fl.StringVarP(&f.mode, "mode", "m", "cbc", "the algorithm mode; used only for reference")
	fl.StringVarP(&f.expiration, flagExpiration, "x", "", "the expiration time for the secret in ISO 8601 format")
	fl.StringVarP(&f.payload, flagPayload, "p", "", "the unencrypted secret data")
	fl.StringVarP(&f.file, flagFile, "F", "", "a file containing the secret payload")
	cmd.MarkFlagsMutuallyExclusive(flagPayload, flagFile)
	return cmd
}

// createOpts builds the POST body the way barbicanclient's Secret.store does
// for the CLI, which always hands it bytes: with an explicit content type or
// encoding the payload is sent as given, and otherwise it is base64-encoded and
// sent as application/octet-stream.
func (f *secretStoreFlags) createOpts() (secrets.CreateOpts, error) {
	opts := secrets.CreateOpts{
		Name: f.name, SecretType: secrets.SecretType(f.secretType),
		Algorithm: f.algorithm, BitLength: f.bitLength, Mode: f.mode,
	}
	if f.expiration != "" {
		t, err := parseExpiration(f.expiration)
		if err != nil {
			return opts, err
		}
		opts.Expiration = t
	}
	var data []byte
	switch {
	case f.payload != "":
		data = []byte(f.payload)
	case f.file != "":
		b, err := os.ReadFile(f.file)
		if err != nil {
			return opts, fmt.Errorf("--file: %w", err)
		}
		if len(b) == 0 {
			return opts, errors.New("invalid payload: cannot be empty")
		}
		data = b
	}
	switch {
	case f.payloadContentType != "" || f.payloadContentEncoding != "":
		opts.Payload = string(data)
		opts.PayloadContentType = f.payloadContentType
		opts.PayloadContentEncoding = f.payloadContentEncoding
	case data != nil:
		opts.Payload = base64.StdEncoding.EncodeToString(data)
		opts.PayloadContentType = "application/octet-stream"
		opts.PayloadContentEncoding = "base64"
	}
	return opts, nil
}

func runSecretStore(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, opts secrets.CreateOpts, w io.Writer) error {
	created, err := secrets.Create(ctx, client, opts).Extract()
	if err != nil {
		return fmt.Errorf("storing secret: %w", err)
	}
	id, err := refUUID(created.SecretRef, "Secret")
	if err != nil {
		return fmt.Errorf("storing secret: barbican answered secret_ref %q: %w", created.SecretRef, err)
	}
	return showSecret(ctx, client, o, id, w)
}

func newSecretUpdateCommand(a *auth.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "update <uri> <payload>",
		Short: "Add the payload to a secret stored without one",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			client, err := newKeyManagerClient(ctx, a)
			if err != nil {
				return err
			}
			return runSecretUpdate(ctx, client, args[0], args[1])
		},
	}
}

// runSecretUpdate PUTs the payload as text/plain, as barbicanclient does for
// the str the CLI passes it.
func runSecretUpdate(ctx context.Context, client *gophercloud.ServiceClient, ref, payload string) error {
	id, err := refUUID(ref, "Secret")
	if err != nil {
		return err
	}
	opts := secrets.UpdateOpts{ContentType: "text/plain", Payload: payload}
	if err := secrets.Update(ctx, client, id, opts).ExtractErr(); err != nil {
		return fmt.Errorf("updating secret %s: %w", id, err)
	}
	return nil
}

// errSecretHasConsumers is upstream's SecretHasConsumers message.
var errSecretHasConsumers = errors.New("secret cannot be deleted as it has consumers; use --force to delete it anyway")

func newSecretDeleteCommand(a *auth.Options) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <uri>",
		Short: "Delete a secret by its URI",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			client, err := newKeyManagerClient(ctx, a)
			if err != nil {
				return err
			}
			return runSecretDelete(ctx, client, args[0], force)
		},
	}
	// Upstream also spells it -f, which koc's global --format holds.
	cmd.Flags().BoolVar(&force, "force", false, "delete the secret even if it has consumers")
	return cmd
}

// runSecretDelete follows barbicanclient below microversion 1.2: it reads the
// secret's consumers and refuses, client-side, unless --force is given. At
// 1.1 barbican itself deletes a secret with consumers without asking.
func runSecretDelete(ctx context.Context, client *gophercloud.ServiceClient, ref string, force bool) error {
	id, err := refUUID(ref, "Secret")
	if err != nil {
		return err
	}
	var s struct {
		Consumers []json.RawMessage `json:"consumers"`
	}
	if err := secrets.Get(ctx, client, id).ExtractInto(&s); err != nil {
		return fmt.Errorf("getting secret %s: %w", id, err)
	}
	if len(s.Consumers) > 0 && !force {
		return errSecretHasConsumers
	}
	if err := secrets.Delete(ctx, client, id).ExtractErr(); err != nil {
		return fmt.Errorf("deleting secret %s: %w", id, err)
	}
	return nil
}

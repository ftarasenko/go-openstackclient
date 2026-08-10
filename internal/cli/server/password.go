package server

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// "koc server password show" — the read side of nova's os-server-password.
//
// koc-native: `nova get-password` was never ported to python-openstackclient
// (its entry_points.txt registers no server-password command at all), yet the
// API is the only way to recover the Windows/cloudbase-init administrator
// password of a server booted with a keypair. `nova clear-password` *is*
// reachable from koc, as `server set --no-password`, so only the read is new.

func newServerPasswordCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "password",
		Short: "Read the server's generated admin password",
	}
	cmd.AddCommand(newServerPasswordShowCommand(a, o))
	return cmd
}

func newServerPasswordShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var keyPath string
	cmd := &cobra.Command{
		Use:   "show <server>",
		Short: "Show the admin password nova stored for a server",
		Long: `Show the admin password nova stored for a server.

The guest agent (cloudbase-init on Windows, cloud-init with the password
module elsewhere) encrypts the generated password with the public half of the
keypair the server booted with, then POSTs it to nova. Without --private-key
this prints that ciphertext, base64-encoded; with it, koc decrypts locally —
the private key is never sent anywhere.

The key must be an RSA key in PEM form (PKCS#1 or PKCS#8) — what
"koc keypair create" hands back. An OpenSSH-format key (the ssh-keygen default
since OpenSSH 7.8) converts with:

    ssh-keygen -p -m PEM -f <key>

Only RSA can carry this scheme, so a server booted with an ed25519 keypair has
no recoverable password.

This is koc-native: the upstream "openstack" client never ported
"nova get-password". Clearing the stored password is "server set --no-password".`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			client, err := newComputeClient(cmd.Context(), a)
			if err != nil {
				return err
			}
			return runServerPasswordShow(cmd.Context(), client, o, args[0], keyPath, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&keyPath, "private-key", "",
		"RSA private key (PEM) to decrypt the password with; \"-\" reads stdin")
	return cmd
}

func runServerPasswordShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	ref, keyPath string, w io.Writer,
) error {
	var key *rsa.PrivateKey
	if keyPath != "" {
		// Read the key before the API call: a typo in the path should not cost a
		// round trip, and it should not look like a server problem.
		var err error
		if key, err = loadRSAPrivateKey(keyPath); err != nil {
			return err
		}
	}
	id, err := resolveServerID(ctx, client, ref)
	if err != nil {
		return err
	}
	password, err := servers.GetPassword(ctx, client, id).ExtractPassword(key)
	if err != nil {
		return fmt.Errorf("reading the admin password of server %q: %w", ref, err)
	}
	if password == "" {
		// Nova answers 200 with an empty string until the guest agent posts one,
		// which is a different situation from a decryption failure.
		return fmt.Errorf("server %q has no stored admin password: "+
			"the guest agent has not posted one, or it was cleared with \"server set --no-password\"", ref)
	}
	return o.WriteSingle(w, []string{"password"}, []any{password})
}

// loadRSAPrivateKey reads a PEM RSA private key from path, or from stdin when
// path is "-".
func loadRSAPrivateKey(path string) (*rsa.PrivateKey, error) {
	var raw []byte
	var err error
	if path == "-" {
		raw, err = io.ReadAll(os.Stdin)
	} else {
		raw, err = os.ReadFile(path) //nolint:gosec // G304: operator-supplied private key path
	}
	if err != nil {
		return nil, fmt.Errorf("reading the private key: %w", err)
	}
	return parseRSAPrivateKey(raw)
}

// parseRSAPrivateKey accepts the two PEM encodings crypto/x509 can read. The
// formats it cannot are common enough that guessing "invalid key" would be
// unhelpful, so each gets its own message and, where there is one, the command
// that converts it.
func parseRSAPrivateKey(raw []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("the private key is not PEM-encoded (expected a \"-----BEGIN ...-----\" block)")
	}
	if block.Type == "OPENSSH PRIVATE KEY" {
		return nil, errors.New("the private key is in OpenSSH format, which koc cannot read; " +
			"convert it with: ssh-keygen -p -m PEM -f <key>")
	}
	// x509.DecryptPEMBlock is deprecated and its cipher suite is not
	// authenticated, so passphrase-protected keys are refused rather than
	// half-supported.
	if _, encrypted := block.Headers["DEK-Info"]; encrypted || strings.Contains(block.Type, "ENCRYPTED") {
		return nil, errors.New("the private key is passphrase-protected; " +
			"decrypt it first with: openssl rsa -in <key> -out <key>.pem")
	}

	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parsing the private key: %w", err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		// Nova encrypts with RSA PKCS#1 v1.5; no other key type can decrypt it.
		return nil, fmt.Errorf("the private key is %T, but nova encrypts the password with RSA", parsed)
	}
	return key, nil
}

package server

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
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
	"golang.org/x/crypto/ssh"
	"golang.org/x/term"

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

--private-key takes either encoding: an OpenSSH key (the ssh-keygen default
since OpenSSH 7.8, "-----BEGIN OPENSSH PRIVATE KEY-----") or a PEM key, PKCS#1
or PKCS#8 — what "koc keypair create" hands back. If the key is
passphrase-protected, koc asks for the passphrase on the terminal, without
echoing it. Non-interactively, redirect the passphrase in instead:

    koc server password show <server> --private-key <key> < passphrase

or strip it from the key with "ssh-keygen -p -f <key>".

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
		"RSA private key (OpenSSH or PEM) to decrypt the password with; \"-\" reads stdin")
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

// loadRSAPrivateKey reads a private key from path, or from stdin when path is
// "-".
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
	return parseRSAPrivateKey(raw, passphraseSource(path == "-"))
}

// parseRSAPrivateKey accepts every private-key encoding x/crypto/ssh reads:
// OpenSSH (the ssh-keygen default since 7.8) and PEM, PKCS#1 or PKCS#8, plain
// or passphrase-protected. passphrase is called only for the protected ones and
// may be nil, meaning there is no way to ask.
func parseRSAPrivateKey(raw []byte, passphrase func() ([]byte, error)) (*rsa.PrivateKey, error) {
	if block, _ := pem.Decode(raw); block == nil {
		// Both encodings are PEM-framed, so this is a public key, a stray file,
		// or a truncated one — worth saying before ssh's terser "no key found".
		return nil, errors.New("the private key is not PEM-encoded (expected a \"-----BEGIN ...-----\" block)")
	}
	parsed, err := ssh.ParseRawPrivateKey(raw)
	var protected *ssh.PassphraseMissingError
	if errors.As(err, &protected) {
		if passphrase == nil {
			return nil, errors.New("the private key is passphrase-protected and there is nowhere to ask for the passphrase; " +
				"redirect it in (\"--private-key <key> < passphrase\") or strip it with: ssh-keygen -p -f <key>")
		}
		var secret []byte
		if secret, err = passphrase(); err != nil {
			return nil, err
		}
		parsed, err = ssh.ParseRawPrivateKeyWithPassphrase(raw, secret)
	}
	if errors.Is(err, x509.IncorrectPasswordError) {
		return nil, errors.New("the passphrase does not decrypt the private key")
	}
	if err != nil {
		return nil, fmt.Errorf("parsing the private key: %w", err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		// Nova encrypts with RSA PKCS#1 v1.5; no other key type can decrypt it.
		return nil, fmt.Errorf("the private key is %s, but nova encrypts the password with RSA", keyTypeName(parsed))
	}
	return key, nil
}

// keyTypeName names a key the way ssh-keygen's -t does, so a wrong-key-type
// error does not print a Go type at the operator.
func keyTypeName(key any) string {
	switch key.(type) {
	case ed25519.PrivateKey, *ed25519.PrivateKey:
		return "an ed25519 key"
	case *ecdsa.PrivateKey:
		return "an ECDSA key"
	}
	return fmt.Sprintf("a %T", key)
}

// passphraseSource decides where a protected key's passphrase comes from. A
// redirect beats the terminal — a script that pipes the passphrase in must not
// be stopped by a prompt — and when the key itself came from stdin the
// controlling terminal is all that is left.
func passphraseSource(keyFromStdin bool) func() ([]byte, error) {
	stdinIsTerminal := term.IsTerminal(int(os.Stdin.Fd()))
	switch {
	case stdinIsTerminal:
		return func() ([]byte, error) { return readPassphrase(os.Stdin, int(os.Stdin.Fd())) }
	case !keyFromStdin:
		return func() ([]byte, error) { return readRedirectedPassphrase(os.Stdin) }
	default:
		return promptOnControllingTerminal
	}
}

// readRedirectedPassphrase takes the first line of r, for the non-interactive
// form: "koc server password show <server> --private-key <key> < passphrase".
func readRedirectedPassphrase(r io.Reader) ([]byte, error) {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("reading the passphrase: %w", err)
	}
	// Only the line ending goes: a passphrase may end in a space, and a file
	// written by an editor ends in a newline.
	secret := strings.TrimRight(line, "\r\n")
	if secret == "" {
		return nil, errors.New("the private key is passphrase-protected and no passphrase arrived on stdin; " +
			"redirect it in (\"--private-key <key> < passphrase\") or strip it with: ssh-keygen -p -f <key>")
	}
	return []byte(secret), nil
}

// promptOnControllingTerminal asks on /dev/tty, the only source left once the
// key has taken stdin.
func promptOnControllingTerminal() ([]byte, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, errors.New("the private key is passphrase-protected and there is no terminal to ask on; " +
			"the passphrase cannot share stdin with the key, so pass the key by path and redirect the passphrase in, " +
			"or strip it with: ssh-keygen -p -f <key>")
	}
	defer func() { _ = tty.Close() }()
	return readPassphrase(tty, int(tty.Fd()))
}

// readPassphrase asks on a terminal, without echoing what is typed.
func readPassphrase(prompt io.Writer, fd int) ([]byte, error) {
	if _, err := fmt.Fprint(prompt, "Enter passphrase for the private key: "); err != nil {
		return nil, err
	}
	secret, err := term.ReadPassword(fd)
	_, _ = fmt.Fprintln(prompt)
	if err != nil {
		return nil, fmt.Errorf("reading the passphrase: %w", err)
	}
	return secret, nil
}

package auth

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"golang.org/x/term"
)

// Two ways to supply the Keystone password other than --os-password /
// OS_PASSWORD, both of which exist because that pair is a poor place for a
// secret: a flag value is visible in `ps` and lands in the shell history, and
// an environment variable is inherited by every child process.
//
//   - --os-password-stdin reads the password from standard input. It is
//     koc-native — python-openstackclient has no equivalent — and follows
//     `docker login --password-stdin`: koc reads stdin, strips one trailing
//     line ending, and uses the rest verbatim.
//   - The interactive prompt is python-openstackclient parity. osc-lib asks on
//     the terminal (getpass) when the chosen auth type needs a password and
//     nothing supplied one, rather than failing; koc now does the same.
//
// koc prompts only when stdin is a terminal. A non-interactive run must fail
// with the usual "no credentials found" error instead of blocking forever on a
// pipe nobody is going to write to.

const flagOSPasswordStdin = "os-password-stdin"

// passwordPrompt matches what osc-lib writes, so an operator moving between the
// two clients sees the same line. It goes to stderr: stdout may be a redirected
// -f json document.
const passwordPrompt = "Password: "

// applyPasswordStdin consumes stdin into o.Password when --os-password-stdin is
// set. It is called before any credential is used, so a conflicting source is
// reported before the first network round trip.
func (o *Options) applyPasswordStdin() error {
	if !o.PasswordStdin || o.forced[flagOSPassword] {
		// Already read. Authenticate can run more than once in a process, and
		// stdin can only be consumed once, so the first read stands.
		return nil
	}
	switch {
	case o.Password != "" && o.explicitlySet(flagOSPassword):
		return fmt.Errorf("--%s and --os-password are mutually exclusive", flagOSPasswordStdin)
	case o.CredsFromNS != "":
		return fmt.Errorf("--%s cannot be combined with --creds-from-ns, which brings its own credentials", flagOSPasswordStdin)
	case o.CredsFromVault != "":
		return fmt.Errorf("--%s cannot be combined with --creds-from-vault, which brings its own credentials", flagOSPasswordStdin)
	}

	pw, err := readPasswordStdin(o.stdin())
	if err != nil {
		return err
	}
	o.rememberPassword(pw)
	return nil
}

// readPasswordStdin takes the whole of r as the password, less one trailing
// line ending.
func readPasswordStdin(r io.Reader) (string, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return "", fmt.Errorf("--%s: reading stdin: %w", flagOSPasswordStdin, err)
	}
	// Only the line ending goes: a password may legitimately begin or end with
	// a space, and `echo`, a here-doc and every text editor append a newline.
	pw := strings.TrimSuffix(string(raw), "\n")
	pw = strings.TrimSuffix(pw, "\r")

	switch {
	case pw == "":
		return "", fmt.Errorf("--%s: no password on stdin", flagOSPasswordStdin)
	case strings.ContainsAny(pw, "\r\n"):
		// Almost always a whole openrc or secrets file piped in by mistake.
		// Authenticating with the first line and failing is the confusing
		// outcome; say what happened instead.
		return "", fmt.Errorf("--%s: stdin holds more than one line, so it is not just a password", flagOSPasswordStdin)
	}
	return pw, nil
}

// stdin is the reader --os-password-stdin consumes, seamed for tests.
func (o *Options) stdin() io.Reader {
	if o.passwordStdinSrc != nil {
		return o.passwordStdinSrc
	}
	return os.Stdin
}

// promptMissingPassword asks for the password on the terminal when the resolved
// auth options need one and no source produced it. With no terminal to ask on
// it changes nothing: the caller's own "no credentials" error is the better
// message, and gophercloud rejects the request either way.
func (o *Options) promptMissingPassword(ao *gophercloud.AuthOptions, w io.Writer) error {
	if !needsPassword(ao) {
		return nil
	}
	ask := o.terminalPassword()
	if ask == nil {
		return nil
	}
	pw, err := ask(w)
	if err != nil {
		return err
	}
	if pw == "" {
		return errors.New("no password given at the prompt")
	}
	ao.Password = pw
	o.rememberPassword(pw)
	return nil
}

// rememberPassword records a password that reached koc from stdin or the
// terminal as though --os-password had carried it — it is as deliberate as one
// typed on the command line, so a named cloud's stored password must not
// outrank it, and a second authentication in the same process
// must not ask for it again (gophercloud's own reauth replays the auth options
// and never gets here).
func (o *Options) rememberPassword(pw string) {
	o.Password = pw
	o.markForced(flagOSPassword)
}

// needsPassword reports whether ao describes password authentication for a
// named user with the password still missing. Application credentials and a
// pre-issued token authenticate without one, so neither is prompted for.
func needsPassword(ao *gophercloud.AuthOptions) bool {
	if ao.Password != "" || ao.TokenID != "" {
		return false
	}
	if ao.ApplicationCredentialID != "" || ao.ApplicationCredentialName != "" {
		return false
	}
	return ao.Username != "" || ao.UserID != ""
}

// willPromptForPassword reports whether a missing password will be asked for
// rather than rejected, for the env path's up-front credential check — which
// runs before the auth options exist and so tests o's own fields.
func (o *Options) willPromptForPassword() bool {
	if o.Username == "" && o.UserID == "" {
		return false
	}
	return o.terminalPassword() != nil
}

// terminalPassword returns the function that asks for a password, or nil when
// there is no terminal to ask on.
func (o *Options) terminalPassword() func(io.Writer) (string, error) {
	if o.promptPassword != nil {
		return o.promptPassword
	}
	if o.PasswordStdin || !term.IsTerminal(int(os.Stdin.Fd())) {
		return nil
	}
	return readTerminalPassword
}

// readTerminalPassword prompts on w and reads stdin without echo.
func readTerminalPassword(w io.Writer) (string, error) {
	if _, err := fmt.Fprint(w, passwordPrompt); err != nil {
		return "", err
	}
	pw, err := term.ReadPassword(int(os.Stdin.Fd()))
	// The Enter the operator typed was swallowed with the echo, so the next
	// thing written to the terminal would otherwise land on the prompt line.
	if _, perr := fmt.Fprintln(w); perr != nil && err == nil {
		err = perr
	}
	if err != nil {
		return "", fmt.Errorf("reading the password: %w", err)
	}
	return string(pw), nil
}

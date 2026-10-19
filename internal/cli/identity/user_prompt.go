package identity

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// flagPasswordPrompt is upstream's `user create|set --password-prompt`: the
// password is read twice from the terminal without echo, so it never lands in
// the shell history or the process list the way --password does.
const flagPasswordPrompt = "password-prompt"

// promptUserPassword asks for a user's new password; tests replace it.
var promptUserPassword = readNewUserPassword

// addPasswordPromptFlag registers --password-prompt next to --password, which
// it replaces.
func addPasswordPromptFlag(cmd *cobra.Command, prompt *bool) {
	cmd.Flags().BoolVar(prompt, flagPasswordPrompt, false, "prompt interactively for the password")
	cmd.MarkFlagsMutuallyExclusive("password", flagPasswordPrompt)
}

// readNewUserPassword prompts on w and reads stdin without echo, twice, with
// upstream's wording. It refuses a stdin that is not a terminal rather than
// read a password that would be echoed or come from a pipe by accident.
func readNewUserPassword(w io.Writer) (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", errors.New("--password-prompt needs an interactive terminal; use --password instead")
	}
	read := func(prompt string) (string, error) {
		if _, err := fmt.Fprint(w, prompt); err != nil {
			return "", err
		}
		pw, err := term.ReadPassword(fd)
		// The Enter the operator typed was swallowed with the echo.
		if _, perr := fmt.Fprintln(w); perr != nil && err == nil {
			err = perr
		}
		if err != nil {
			return "", fmt.Errorf("reading the password: %w", err)
		}
		return string(pw), nil
	}
	first, err := read("User Password: ")
	if err != nil {
		return "", err
	}
	second, err := read("Repeat User Password: ")
	if err != nil {
		return "", err
	}
	if first != second {
		return "", errors.New("passwords do not match")
	}
	return first, nil
}

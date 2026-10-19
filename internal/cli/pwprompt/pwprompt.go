// Package pwprompt reads a new password from the terminal twice, without echo,
// for the verbs that take one interactively (`user create|set
// --password-prompt`, `server set --root-password`).
package pwprompt

import (
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/term"
)

// Wording is what one prompt says; each verb keeps upstream's.
type Wording struct {
	Flag     string // the flag that asked, named when stdin is not a terminal
	First    string // the first prompt
	Second   string // the confirmation prompt
	ReadErr  string // prefix of a read failure
	Mismatch string // the error when the two answers differ
}

// Confirmed prompts on w and reads stdin without echo, twice. It refuses a
// stdin that is not a terminal rather than read a password that would be
// echoed or come from a pipe by accident.
func Confirmed(w io.Writer, s Wording) (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", fmt.Errorf("%s needs an interactive terminal; use --password instead", s.Flag)
	}
	return confirm(w, s, func() ([]byte, error) { return term.ReadPassword(fd) })
}

func confirm(w io.Writer, s Wording, read func() ([]byte, error)) (string, error) {
	ask := func(prompt string) (string, error) {
		if _, err := fmt.Fprint(w, prompt); err != nil {
			return "", err
		}
		pw, err := read()
		// The Enter the operator typed was swallowed with the echo.
		if _, perr := fmt.Fprintln(w); perr != nil && err == nil {
			err = perr
		}
		if err != nil {
			return "", fmt.Errorf("%s: %w", s.ReadErr, err)
		}
		return string(pw), nil
	}
	first, err := ask(s.First)
	if err != nil {
		return "", err
	}
	second, err := ask(s.Second)
	if err != nil {
		return "", err
	}
	if first != second {
		return "", errors.New(s.Mismatch)
	}
	return first, nil
}

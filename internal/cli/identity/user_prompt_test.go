package identity

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"golang.org/x/term"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// --password-prompt asks before anything else happens, on stderr, and its
// answer replaces --password: a failed prompt stops the command there.
func TestUserCreateSet_PasswordPromptRunsFirst(t *testing.T) {
	errStub := errors.New("stub prompt")
	for name, args := range map[string][]string{
		"create": {"bob", "--password-prompt"},
		"set":    {"bob", "--password-prompt"},
	} {
		t.Run(name, func(t *testing.T) {
			var promptedOn io.Writer
			saved := promptUserPassword
			t.Cleanup(func() { promptUserPassword = saved })
			promptUserPassword = func(w io.Writer) (string, error) {
				promptedOn = w
				return "", errStub
			}
			o := &output.Options{Format: output.FormatTable}
			cmd := newUserCreateCommand(nil, o)
			if name == "set" {
				cmd = newUserSetCommand(nil, o)
			}
			var stdout, stderr bytes.Buffer
			cmd.SetArgs(args)
			cmd.SetOut(&stdout)
			cmd.SetErr(&stderr)
			if err := cmd.Execute(); !errors.Is(err, errStub) {
				t.Fatalf("err = %v, want the prompt's", err)
			}
			if promptedOn != &stderr {
				t.Errorf("prompted on %v, want the command's stderr", promptedOn)
			}
		})
	}
}

func TestUserCreate_PasswordAndPromptExclusive(t *testing.T) {
	cmd := newUserCreateCommand(nil, &output.Options{Format: output.FormatTable})
	cmd.SetArgs([]string{"bob", "--password", "x", "--password-prompt"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "none of the others can be") {
		t.Fatalf("err = %v, want cobra's mutually-exclusive error", err)
	}
}

// Under go test stdin is not a terminal, which is the refusal path.
func TestReadNewUserPassword_NeedsATerminal(t *testing.T) {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		t.Skip("stdin is a terminal: the prompt would wait for input")
	}
	var w bytes.Buffer
	if _, err := readNewUserPassword(&w); err == nil || !strings.Contains(err.Error(), "needs an interactive terminal") {
		t.Fatalf("err = %v", err)
	}
	if w.Len() != 0 {
		t.Errorf("prompted %q without a terminal", w.String())
	}
}

package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// execRoot runs the real command tree with args, returning stdout+stderr and the
// error Execute produced (which main turns into a non-zero exit).
func execRoot(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := NewRootCommand("test")
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs(args)
	err := root.Execute()
	return buf.String(), err
}

// An unknown verb under a known noun must fail. It used to print help to stdout
// and exit 0, so a script could not tell `koc zone list` from `koc zone lst`.
// Upstream cliff raises ValueError("Unknown command") and exits 2.
func TestUnknownSubcommand_IsAnError(t *testing.T) {
	for _, args := range [][]string{
		{"zone", "bogusverb"},
		{"catalog", "bogusverb"},
		{"server", "bogusverb"},
		{"loadbalancer", "quota", "bogusverb"},
		{"baremetal", "node", "bogusverb"},
		{"bogusnoun"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			_, err := execRoot(t, args...)
			if err == nil {
				t.Fatalf("koc %s exited 0; want an error", strings.Join(args, " "))
			}
			if !strings.Contains(err.Error(), "unknown command") {
				t.Errorf("error = %v, want an \"unknown command\" message", err)
			}
		})
	}
}

// A bare group is still a help request, as it is in cobra.
func TestBareGroup_PrintsHelpAndSucceeds(t *testing.T) {
	out, err := execRoot(t, "zone")
	if err != nil {
		t.Fatalf("koc zone returned an error: %v", err)
	}
	if !strings.Contains(out, "Available Commands:") {
		t.Errorf("koc zone should print help, got:\n%s", out)
	}
}

// requireSubcommands gives group commands a RunE, which would otherwise make
// cobra advertise them as runnable. docs/coverage.md counts leaf commands by
// reading runnability off the Usage block, so a pure group must still show
// only the "<path> [command]" form.
func TestGroupUsageBlock_StaysGroupOnly(t *testing.T) {
	out, err := execRoot(t, "zone", "--help")
	if err != nil {
		t.Fatalf("koc zone --help returned an error: %v", err)
	}
	if !strings.Contains(out, "koc zone [command]") {
		t.Errorf("group usage form missing:\n%s", out)
	}
	if strings.Contains(out, "koc zone [flags]") {
		t.Errorf("a pure group must not advertise a runnable usage form:\n%s", out)
	}
}

// Real leaf commands must be untouched: they keep their own Args validator, so a
// missing or surplus positional is still cobra's error, not the group one.
func TestLeafCommand_KeepsItsOwnArgsValidation(t *testing.T) {
	_, err := execRoot(t, "zone", "show")
	if err == nil {
		t.Fatal("koc zone show with no argument should fail")
	}
	if strings.Contains(err.Error(), "unknown command") {
		t.Errorf("leaf arg validation was replaced by the group handler: %v", err)
	}
}

// Every group in the assembled tree must carry the annotation and a RunE, so no
// service can add a noun that silently exits 0 on a typo.
func TestEveryGroupInTheTreeRequiresASubcommand(t *testing.T) {
	root := NewRootCommand("test")
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			walk(sub)
		}
		if !c.HasSubCommands() || c.Annotations[groupAnnotation] == "" {
			return
		}
		if c.RunE == nil {
			t.Errorf("%q is a group but has no RunE to reject unknown verbs", c.CommandPath())
		}
	}
	walk(root)
}

// execRootArgs runs an already-built root with args, returning its output.
func execRootArgs(t *testing.T, root *cobra.Command, args []string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs(args)
	err := root.Execute()
	return buf.String(), err
}

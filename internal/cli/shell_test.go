package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ftarasenko/go-openstackclient/internal/cli/shell"
)

// runShell runs `koc shell` over piped lines through the real command tree.
func runShell(t *testing.T, lines ...string) (stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	if err := shell.Run(context.Background(), shellConfig("test"), strings.NewReader(strings.Join(lines, "\n")+"\n"), &out, &errOut, nil); err != nil {
		t.Fatal(err)
	}
	return out.String(), errOut.String()
}

// TestShell_NoFlagLeakBetweenLines is the shell's half of
// TestRun_NoFlagLeakBetweenRuns: `server list --long` and then `server list`
// on the next line, against the mock.
func TestShell_NoFlagLeakBetweenLines(t *testing.T) {
	k := &keystoneFleet{}
	startFleet(t, k)

	plain, _ := runShell(t, "server list")
	both, stderr := runShell(t, "server list --long", "server list", "server lst", "server list")
	if stderr == "" || !strings.Contains(stderr, `unknown command "lst"`) {
		t.Errorf("the bad line was not reported: %q", stderr)
	}
	if !strings.HasSuffix(both, plain+plain) {
		t.Errorf("after --long, server list rendered differently:\nplain:\n%s\nsession:\n%s", plain, both)
	}
	if strings.Count(both, plain) != 2 {
		t.Errorf("want the plain table exactly twice after the --long one:\n%s", both)
	}
	// One authentication per line that reached a command: the fresh tree per
	// line starts with nothing memoized. The rejected line never authenticates.
	if got := k.tokens.Load(); got != 4 {
		t.Errorf("authenticated %d times, want 4 (one per command line)", got)
	}
}

//go:build functional

package functional

import (
	"strings"
	"testing"
	"time"
)

// Node console and secure boot, on a fake-hardware node.

// eventually polls cond for an asynchronous conductor action.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Second) {
		if cond() {
			return
		}
	}
	t.Errorf("timed out waiting for %s", what)
}

func TestBaremetalNodeConsole(t *testing.T) {
	c := ft(t)
	requireFeature(t, "ironic")
	r := defaultRunner(c)
	node, _ := newBareNode(t, r)
	r.ok(t, "baremetal", "node", "set", node, "--console-interface", "fake")

	enabled := func() string {
		return field(r.show(t, "baremetal", "node", "console", "show", node), "console_enabled")
	}
	if got := enabled(); got != "false" {
		t.Errorf("console_enabled = %q before enable", got)
	}
	r.ok(t, "baremetal", "node", "console", "enable", node)
	eventually(t, "console_enabled true", func() bool { return enabled() == "true" })
	r.ok(t, "baremetal", "node", "console", "disable", node)
	eventually(t, "console_enabled false", func() bool { return enabled() == "false" })
}

// fake-hardware's management interface has no secure boot, so ironic refuses
// the change; the test proves koc reaches the endpoint and reports the refusal.
func TestBaremetalNodeSecureBoot(t *testing.T) {
	c := ft(t)
	requireFeature(t, "ironic")
	r := defaultRunner(c)
	node, _ := newBareNode(t, r)

	on := r.run(t, "baremetal", "node", "secure", "boot", "on", node)
	off := r.run(t, "baremetal", "node", "secure", "boot", "off", node)
	for verb, res := range map[string]result{"on": on, "off": off} {
		// Exit 0 means ironic accepted (202) and the conductor failed later:
		// that lands in last_error, not in koc's exit code.
		if res.code != 0 && !strings.Contains(res.stderr, "secure boot "+verb) {
			t.Errorf("secure boot %s: stderr %q does not name the operation", verb, res.stderr)
		}
	}
}

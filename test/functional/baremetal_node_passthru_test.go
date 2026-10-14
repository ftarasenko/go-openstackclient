//go:build functional

package functional

import (
	"slices"
	"strings"
	"testing"
)

// Node vendor passthru, on a fake-hardware node.

func TestBaremetalNodePassthru(t *testing.T) {
	c := ft(t)
	requireFeature(t, "ironic")
	r := systemRunner(c)
	node, _ := newBareNode(t, r)
	r.ok(t, "baremetal", "node", "set", node, "--vendor-interface", "fake")

	methods := column(r.list(t, "baremetal", "node", "passthru", "list", node), "Name")
	if !slices.Contains(methods, "third_method_sync") {
		t.Fatalf("passthru list = %v, want fake vendor B's methods", methods)
	}
	// fake vendor B's synchronous method answers whether bar is "meow".
	if out := strings.TrimSpace(r.ok(t, "baremetal", "node", "passthru", "call", node, "third_method_sync", "--arg", "bar=meow")); out != "true" {
		t.Errorf("passthru call third_method_sync = %q, want true", out)
	}
	r.fails(t, "baremetal", "node", "passthru", "call", node, "third_method_sync")
}

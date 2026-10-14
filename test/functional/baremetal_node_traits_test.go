//go:build functional

package functional

import (
	"slices"
	"testing"
)

// Node traits, on a fake-hardware node.

func TestBaremetalNodeTraits(t *testing.T) {
	c := ft(t)
	requireFeature(t, "ironic")
	r := defaultRunner(c)
	node, _ := newBareNode(t, r)

	traits := func() []string {
		return column(r.list(t, "baremetal", "node", "trait", "list", node), "Traits")
	}
	r.ok(t, "baremetal", "node", "trait", "add", node, "CUSTOM_FT_A", "CUSTOM_FT_B")
	r.ok(t, "baremetal", "node", "add", "trait", node, "CUSTOM_FT_C")
	if got := traits(); !slices.Equal(got, []string{"CUSTOM_FT_A", "CUSTOM_FT_B", "CUSTOM_FT_C"}) {
		t.Errorf("traits after add = %v", got)
	}
	r.fails(t, "baremetal", "node", "trait", "add", node, "not a trait")
	r.ok(t, "baremetal", "node", "trait", "remove", node, "CUSTOM_FT_A")
	r.ok(t, "baremetal", "node", "remove", "trait", node, "CUSTOM_FT_B")
	if got := traits(); !slices.Equal(got, []string{"CUSTOM_FT_C"}) {
		t.Errorf("traits after remove = %v", got)
	}
	r.ok(t, "baremetal", "node", "trait", "remove", node, "--all")
	if got := traits(); len(got) != 0 {
		t.Errorf("traits after remove --all = %v", got)
	}
}

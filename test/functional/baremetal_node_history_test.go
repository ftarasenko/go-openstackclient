//go:build functional

package functional

import (
	"strings"
	"testing"
)

// Node history and child nodes, on a fake-hardware node.

func TestBaremetalNodeHistoryAndChildren(t *testing.T) {
	c := ft(t)
	requireFeature(t, "ironic")
	r := defaultRunner(c)
	node, _ := newBareNode(t, r)

	// A failed transition is what ironic records: manage needs a power
	// interface that validates, and an unset fake driver_info is fine, so make
	// the failure explicit with a deploy from enroll.
	r.fails(t, "baremetal", "node", "deploy", node)
	events := r.list(t, "baremetal", "node", "history", "list", node, "--long")
	if len(events) > 0 {
		e := r.show(t, "baremetal", "node", "history", "get", node, field(events[0], "UUID"))
		if field(e, "uuid") != field(events[0], "UUID") || field(e, "event") == "" {
			t.Errorf("history get = %v", e)
		}
	} else {
		r.fails(t, "baremetal", "node", "history", "get", node, "4e1f2d6c-0000-4000-8000-000000000000")
	}

	if microversionAtLeast(ironicAPI(t), "1.83") {
		if rows := r.list(t, "baremetal", "node", "children", "list", node); len(rows) != 0 {
			t.Errorf("children of a fresh node = %v", rows)
		}
	} else if res := r.fails(t, "baremetal", "node", "children", "list", node); !strings.Contains(res, "requires ironic API 1.83") {
		t.Errorf("children list below 1.83: %q", res)
	}
}

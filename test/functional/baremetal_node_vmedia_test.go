//go:build functional

package functional

import (
	"strings"
	"testing"
)

// Node virtual media, on a fake-hardware node.

// fake-hardware cannot attach virtual media; the test proves the request
// reaches ironic (or names the version on a cloud below 1.89).
func TestBaremetalNodeVmedia(t *testing.T) {
	c := ft(t)
	requireFeature(t, "ironic")
	r := systemRunner(c)
	node, _ := newBareNode(t, r)

	attach := r.run(t, "baremetal", "node", "vmedia", "attach", node, "http://192.0.2.1/boot.iso")
	detach := r.run(t, "baremetal", "node", "vmedia", "detach", node, "--device-type", "cdrom")
	if !microversionAtLeast(ironicAPI(t), "1.89") {
		for _, res := range []result{attach, detach} {
			if res.code == 0 || !strings.Contains(res.stderr, "requires ironic API 1.89") {
				t.Errorf("vmedia below 1.89: exit %d, %q", res.code, res.stderr)
			}
		}
		return
	}
	for _, res := range []result{attach, detach} {
		if res.code != 0 && strings.Contains(res.stderr, "requires ironic API") {
			t.Errorf("vmedia on ironic %s blamed the version: %q", ironicAPI(t), res.stderr)
		}
	}
}

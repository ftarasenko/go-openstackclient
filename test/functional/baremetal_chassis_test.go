//go:build functional

package functional

import (
	"fmt"
	"strings"
	"testing"
)

// Chassis, resources-from-file, port unset, boot mode and driver passthru,
// against devstack's API-only fake-hardware ironic.

func TestBaremetalChassis(t *testing.T) {
	c := ft(t)
	requireFeature(t, "ironic")
	r := systemRunner(c)

	ch := r.show(t, "baremetal", "chassis", "create", "--description", "koc functional", "--extra", "rack=r1")
	id := field(ch, "uuid")
	t.Cleanup(func() { r.run(t, "baremetal", "chassis", "delete", id) })
	if field(ch, "description") != "koc functional" || !strings.Contains(field(ch, "extra"), "r1") {
		t.Errorf("chassis create = %v", ch)
	}
	if rows := r.list(t, "baremetal", "chassis", "list", "--long"); !in(rows, id) {
		t.Errorf("chassis list --long has no %s: %v", id, rows)
	}

	r.ok(t, "baremetal", "chassis", "set", id, "--description", "koc functional 2", "--extra", "row=b")
	got := r.show(t, "baremetal", "chassis", "show", id)
	if field(got, "description") != "koc functional 2" || !strings.Contains(field(got, "extra"), "row") {
		t.Errorf("after chassis set: %v", got)
	}
	r.ok(t, "baremetal", "chassis", "unset", id, "--description", "--extra", "rack")
	got = r.show(t, "baremetal", "chassis", "show", id)
	if field(got, "description") != "" || strings.Contains(field(got, "extra"), "rack") {
		t.Errorf("after chassis unset: %v", got)
	}

	r.ok(t, "baremetal", "chassis", "delete", id)
	r.fails(t, "baremetal", "chassis", "show", id)
}

// TestBaremetalCreateFromFile enrolls a chassis holding a node with a port
// group and two ports from one YAML file, then reads every piece back.
func TestBaremetalCreateFromFile(t *testing.T) {
	c := ft(t)
	requireFeature(t, "ironic")
	r := systemRunner(c)

	name := uniq("bmfile")
	doc := fmt.Sprintf(`chassis:
  - description: %[1]s
    nodes:
      - driver: fake-hardware
        name: %[1]s
        resource_class: CUSTOM_FT
        portgroups:
          - name: %[1]s-bond
            address: "52:54:00:13:00:00"
            ports:
              - address: "52:54:00:13:00:01"
        ports:
          - address: "52:54:00:13:00:02"
`, name)
	file := writeFile(t, t.TempDir(), "inventory.yaml", doc)
	out := r.ok(t, "baremetal", "create", file)

	created := map[string][]string{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if rest, ok := strings.CutPrefix(line, "Created "); ok {
			i := strings.LastIndex(rest, " ")
			created[rest[:i]] = append(created[rest[:i]], rest[i+1:])
		}
	}
	t.Cleanup(func() {
		for _, p := range created["port"] {
			r.run(t, "baremetal", "port", "delete", p)
		}
		for _, pg := range created["port group"] {
			r.run(t, "baremetal", "port", "group", "delete", pg)
		}
		for _, n := range created["node"] {
			r.run(t, "baremetal", "node", "delete", n)
		}
		for _, ch := range created["chassis"] {
			r.run(t, "baremetal", "chassis", "delete", ch)
		}
	})
	if len(created["chassis"]) != 1 || len(created["node"]) != 1 || len(created["port group"]) != 1 || len(created["port"]) != 2 {
		t.Fatalf("baremetal create output = %q", out)
	}

	node := r.show(t, "baremetal", "node", "show", name)
	if field(node, "chassis_uuid") != created["chassis"][0] {
		t.Errorf("node chassis_uuid = %q, want %s", field(node, "chassis_uuid"), created["chassis"][0])
	}
	if ports := r.list(t, "baremetal", "port", "list", "--port-group", created["port group"][0]); len(ports) != 1 {
		t.Errorf("port group members = %v, want the nested port", ports)
	}
	if ports := r.list(t, "baremetal", "port", "list", "--node", name); len(ports) != 2 {
		t.Errorf("node ports = %v, want both", ports)
	}

	// A file in neither format is refused before anything is created.
	bad := writeFile(t, t.TempDir(), "inventory.txt", doc)
	if stderr := r.fails(t, "baremetal", "create", bad); !strings.Contains(stderr, ".json or .yaml") {
		t.Errorf("baremetal create of a .txt: %s", stderr)
	}
}

func TestBaremetalPortUnset(t *testing.T) {
	c := ft(t)
	requireFeature(t, "ironic")
	r := systemRunner(c)
	_, nodeUUID := newBareNode(t, r)

	p := r.show(t, "baremetal", "port", "create", "52:54:00:13:00:10", "--node", nodeUUID,
		"--physical-network", "physnet1", "--extra", "rack=r1", "--extra", "row=b")
	id := field(p, "uuid")
	t.Cleanup(func() { r.run(t, "baremetal", "port", "delete", id) })

	r.ok(t, "baremetal", "port", "unset", id, "--physical-network", "--extra", "rack")
	got := r.show(t, "baremetal", "port", "show", id)
	if field(got, "physical_network") != "" || strings.Contains(field(got, "extra"), "rack") ||
		!strings.Contains(field(got, "extra"), "row") {
		t.Errorf("after port unset: %v", got)
	}
	r.fails(t, "baremetal", "port", "unset", id)
}

// fake-hardware's management interface cannot set a boot mode, so the
// conductor records the failure in last_error after ironic accepted the
// request: that it names the requested mode proves koc's request arrived.
func TestBaremetalNodeBootMode(t *testing.T) {
	c := ft(t)
	requireFeature(t, "ironic")
	r := systemRunner(c)
	node, _ := newBareNode(t, r)

	r.fails(t, "baremetal", "node", "boot", "mode", "set", node, "floppy")
	res := r.run(t, "baremetal", "node", "boot", "mode", "set", node, "uefi")
	if res.code != 0 {
		if !strings.Contains(res.stderr, "boot mode uefi") {
			t.Errorf("boot mode set: stderr %q does not name the operation", res.stderr)
		}
		return
	}
	eventually(t, "the boot mode change to be applied or refused", func() bool {
		n := r.show(t, "baremetal", "node", "show", node)
		return field(n, "boot_mode") == "uefi" || strings.Contains(field(n, "last_error"), "uefi")
	})
}

// No fake vendor interface defines a driver-level method, so the listing is
// empty and a call is ironic's refusal — which still proves the route.
func TestBaremetalDriverPassthru(t *testing.T) {
	c := ft(t)
	requireFeature(t, "ironic")
	r := systemRunner(c)

	r.list(t, "baremetal", "driver", "passthru", "list", "fake-hardware")
	if stderr := r.fails(t, "baremetal", "driver", "passthru", "call", "fake-hardware", "koc_no_such_method"); !strings.Contains(stderr, "koc_no_such_method") {
		t.Errorf("driver passthru call: %s", stderr)
	}
}

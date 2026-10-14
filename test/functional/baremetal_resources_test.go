//go:build functional

package functional

import (
	"testing"
	"time"
)

// The standalone ironic nouns: inspection rules, volume connectors and
// targets, shards. Like baremetal_test.go they run against devstack's
// API-only ironic.

func TestBaremetalInspectionRules(t *testing.T) {
	c := ft(t)
	requireIronicAPI(t, "1.96")
	r := defaultRunner(c)

	rule := r.show(t, "baremetal", "inspection", "rule", "create",
		"--description", "koc functional", "--priority", "10",
		"--conditions", `[{"op":"is-true","args":{"value":true}}]`,
		"--actions", `[{"op":"add-trait","args":{"name":"CUSTOM_FT"}}]`)
	id := field(rule, "uuid")
	t.Cleanup(func() { r.run(t, "baremetal", "inspection", "rule", "delete", id) })
	if field(rule, "priority") != "10" || len(items(rule, "actions")) != 1 || len(items(rule, "conditions")) != 1 {
		t.Errorf("inspection rule create = %v", rule)
	}

	rows := r.list(t, "baremetal", "inspection", "rule", "list", "--long")
	if !in(rows, id) {
		t.Errorf("inspection rule list --long has no %s: %v", id, rows)
	}

	r.ok(t, "baremetal", "inspection", "rule", "set", id, "--priority", "20", "--description", "koc functional 2",
		"--actions", `[{"op":"log","args":{"msg":"koc"}},{"op":"add-trait","args":{"name":"CUSTOM_FT"}}]`)
	got := r.show(t, "baremetal", "inspection", "rule", "show", id)
	if field(got, "priority") != "20" || field(got, "description") != "koc functional 2" || len(items(got, "actions")) != 2 {
		t.Errorf("after inspection rule set: %v", got)
	}

	r.ok(t, "baremetal", "inspection", "rule", "unset", id, "--description", "--action", "0", "--condition", "0")
	got = r.show(t, "baremetal", "inspection", "rule", "show", id)
	if field(got, "description") != "" || len(items(got, "actions")) != 1 || len(items(got, "conditions")) != 0 {
		t.Errorf("after inspection rule unset: %v", got)
	}

	r.ok(t, "baremetal", "inspection", "rule", "delete", id)
	r.fails(t, "baremetal", "inspection", "rule", "show", id)
}

// powerOff powers a fake-hardware node off and waits for ironic to record it:
// ironic accepts a volume connector or target update only then.
func powerOff(t *testing.T, r runner, node string) {
	t.Helper()
	r.ok(t, "baremetal", "node", "power", "off", node)
	for deadline := time.Now().Add(90 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Second) {
		if field(r.show(t, "baremetal", "node", "show", node), "power_state") == "power off" {
			return
		}
	}
	t.Fatalf("node %s did not reach power off", node)
}

func TestBaremetalVolumeConnectorsAndTargets(t *testing.T) {
	c := ft(t)
	requireFeature(t, "ironic")
	r := defaultRunner(c)
	node, nodeUUID := newBareNode(t, r)
	powerOff(t, r, nodeUUID)

	iqn := "iqn.2010-10.org.openstack:" + uniq("conn")
	conn := r.show(t, "baremetal", "volume", "connector", "create", "--node", node, "--type", "iqn",
		"--connector-id", iqn, "--extra", "rack=r1")
	connID := field(conn, "uuid")
	t.Cleanup(func() { r.run(t, "baremetal", "volume", "connector", "delete", connID) })
	if field(conn, "node_uuid") != nodeUUID || field(conn, "connector_id") != iqn {
		t.Errorf("volume connector create = %v", conn)
	}
	if rows := r.list(t, "baremetal", "volume", "connector", "list", "--node", node, "--long"); len(rows) != 1 ||
		field(rows[0], "Connector ID") != iqn {
		t.Errorf("volume connector list --node --long = %v", rows)
	}
	r.ok(t, "baremetal", "volume", "connector", "set", connID, "--type", "ip", "--connector-id", "192.0.2.10")
	got := r.show(t, "baremetal", "volume", "connector", "show", connID)
	if field(got, "type") != "ip" || field(got, "connector_id") != "192.0.2.10" {
		t.Errorf("after volume connector set: %v", got)
	}
	r.ok(t, "baremetal", "volume", "connector", "unset", connID, "--extra", "rack")
	if got = r.show(t, "baremetal", "volume", "connector", "show", connID); len(object(got, "extra")) != 0 {
		t.Errorf("after volume connector unset: %v", got)
	}

	volID := "8f4b5c6d-7e8f-4a0b-9c1d-" + time.Now().Format("150405000000")
	tgt := r.show(t, "baremetal", "volume", "target", "create", "--node", nodeUUID, "--type", "iscsi",
		"--boot-index", "0", "--volume-id", volID, "--property", "target_lun=0")
	tgtID := field(tgt, "uuid")
	t.Cleanup(func() { r.run(t, "baremetal", "volume", "target", "delete", tgtID) })
	if field(tgt, "volume_id") != volID || field(tgt, "boot_index") != "0" {
		t.Errorf("volume target create = %v", tgt)
	}
	if rows := r.list(t, "baremetal", "volume", "target", "list", "--node", nodeUUID); len(rows) != 1 ||
		field(rows[0], "Volume ID") != volID {
		t.Errorf("volume target list --node = %v", rows)
	}
	r.ok(t, "baremetal", "volume", "target", "set", tgtID, "--boot-index", "1", "--property", "target_lun=1")
	got = r.show(t, "baremetal", "volume", "target", "show", tgtID)
	if field(got, "boot_index") != "1" || field(object(got, "properties"), "target_lun") != "1" {
		t.Errorf("after volume target set: %v", got)
	}
	r.ok(t, "baremetal", "volume", "target", "unset", tgtID, "--property", "target_lun")
	if got = r.show(t, "baremetal", "volume", "target", "show", tgtID); len(object(got, "properties")) != 0 {
		t.Errorf("after volume target unset: %v", got)
	}

	r.ok(t, "baremetal", "volume", "target", "delete", tgtID)
	r.fails(t, "baremetal", "volume", "target", "show", tgtID)
	r.ok(t, "baremetal", "volume", "connector", "delete", connID)
	r.fails(t, "baremetal", "volume", "connector", "show", connID)
}

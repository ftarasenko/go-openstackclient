package baremetal

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

const (
	testPGUUID   = "99999999-0000-0000-0000-000000000001"
	testNodeUUID = "11111111-1111-1111-1111-111111111111"
)

func testPortGroup() map[string]any {
	return map[string]any{
		"uuid": testPGUUID, "name": "bond0", "address": "52:54:00:00:00:01",
		"node_uuid": testNodeUUID, "mode": "802.3ad", "properties": map[string]any{"miimon": "100"},
		"standalone_ports_supported": false, "physical_network": "physnet1",
		"internal_info": map[string]any{"tenant_vif_port_id": "cccccccc-0000-0000-0000-000000000001"},
		"extra":         map[string]any{}, "links": []any{}, "ports": []any{},
	}
}

func TestRunPortGroupList_LongJoinsMemberPorts(t *testing.T) {
	client, fake := newFakeIronic(t, map[string]fakeRoute{
		"GET /portgroups/detail": {http.StatusOK, map[string]any{"portgroups": []any{testPortGroup()}}},
		"GET /ports": {http.StatusOK, map[string]any{"ports": []any{
			map[string]any{"uuid": "p1", "portgroup_uuid": testPGUUID},
			map[string]any{"uuid": "p2", "portgroup_uuid": testPGUUID},
			map[string]any{"uuid": "p3", "portgroup_uuid": nil},
		}}},
	})
	o := &output.Options{Format: output.FormatJSON}
	var buf bytes.Buffer
	f := &portGroupListFlags{long: true, node: "bm-1", shards: []string{"a", "b"}}
	if err := runPortGroupList(context.Background(), client, o, f, &buf); err != nil {
		t.Fatal(err)
	}
	if q := fake.call("GET /portgroups/detail").Query; q != "node=bm-1&shard=a%2Cb" {
		t.Errorf("port group query = %q", q)
	}
	if q := fake.call("GET /ports").Query; q != "fields=uuid%2Cportgroup_uuid&node=bm-1" {
		t.Errorf("member port query = %q", q)
	}
	rows := decodeJSON[[]map[string]any](t, buf.String())
	jsonEq(t, "Ports", rows[0]["Ports"], `["p1","p2"]`)
	jsonEq(t, "Internal Info", rows[0]["Internal Info"], `{"tenant_vif_port_id":"cccccccc-0000-0000-0000-000000000001"}`)
	for _, col := range []string{"Mode", "Properties", "Standalone Ports Supported", "Physical Network"} {
		if _, ok := rows[0][col]; !ok {
			t.Errorf("--long has no %q column", col)
		}
	}
}

func TestRunPortGroupList_ShortSkipsMemberLookup(t *testing.T) {
	client, _ := newFakeIronic(t, map[string]fakeRoute{
		"GET /portgroups": {http.StatusOK, map[string]any{"portgroups": []any{
			map[string]any{"uuid": testPGUUID, "name": "bond0", "address": "52:54:00:00:00:01"},
		}}},
	})
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	if err := runPortGroupList(context.Background(), client, o, &portGroupListFlags{}, &buf); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != testPGUUID+" 52:54:00:00:00:01 bond0\n" {
		t.Errorf("output = %q", got)
	}
}

func TestRunPortGroupShow_ByAddressWithPorts(t *testing.T) {
	client, fake := newFakeIronic(t, map[string]fakeRoute{
		"GET /portgroups/detail":                   {http.StatusOK, map[string]any{"portgroups": []any{testPortGroup()}}},
		"GET /portgroups/" + testPGUUID + "/ports": {http.StatusOK, map[string]any{"ports": []any{map[string]any{"uuid": "p1"}}}},
	})
	o := &output.Options{Format: output.FormatJSON}
	var buf bytes.Buffer
	if err := runPortGroupShow(context.Background(), client, o, "52:54:00:00:00:01", true, &buf); err != nil {
		t.Fatal(err)
	}
	if q := fake.call("GET /portgroups/detail").Query; q != "address=52%3A54%3A00%3A00%3A00%3A01" {
		t.Errorf("query = %q", q)
	}
	got := decodeJSON[map[string]any](t, buf.String())
	jsonEq(t, "ports", got["ports"], `["p1"]`)
	if got["mode"] != "802.3ad" {
		t.Errorf("mode = %v", got["mode"])
	}
	if _, ok := got["links"]; ok {
		t.Error("show renders links")
	}
}

func TestRunPortGroupCreate_ResolvesNodeAndSendsBody(t *testing.T) {
	client, fake := newFakeIronic(t, map[string]fakeRoute{
		"GET /nodes/bm-1":                          {http.StatusOK, map[string]any{"uuid": testNodeUUID}},
		"POST /portgroups":                         {http.StatusCreated, testPortGroup()},
		"GET /portgroups/" + testPGUUID + "/ports": {http.StatusOK, map[string]any{"ports": []any{}}},
	})
	o := &output.Options{Format: output.FormatValue, Columns: []string{"name"}}
	var buf bytes.Buffer
	f := &portGroupAttrFlags{
		node: "bm-1", name: "bond0", address: "52:54:00:00:00:01", mode: "802.3ad",
		properties: []string{"miimon=100", "xmit_hash_policy=layer3+4"}, extra: []string{"rack=r1"}, noStandalone: true,
	}
	if err := runPortGroupCreate(context.Background(), client, o, f, "", &buf); err != nil {
		t.Fatal(err)
	}
	if q := fake.call("GET /nodes/bm-1").Query; q != "fields=uuid" {
		t.Errorf("node lookup query = %q", q)
	}
	jsonEq(t, "create body", fake.call("POST /portgroups").Body, `{
		"node_uuid": "`+testNodeUUID+`", "name": "bond0", "address": "52:54:00:00:00:01", "mode": "802.3ad",
		"properties": {"miimon": "100", "xmit_hash_policy": "layer3+4"}, "extra": {"rack": "r1"},
		"standalone_ports_supported": false}`)
	if buf.String() != "bond0\n" {
		t.Errorf("output = %q", buf.String())
	}
}

func TestRunPortGroupSetUnset_PatchBodies(t *testing.T) {
	client, fake := newFakeIronic(t, map[string]fakeRoute{
		"PATCH /portgroups/bond0": {http.StatusOK, testPortGroup()},
	})
	o := &output.Options{Format: output.FormatValue, Columns: []string{"uuid"}}
	var buf bytes.Buffer
	f := &portGroupAttrFlags{mode: "active-backup", properties: []string{"miimon=200"}, standalone: true}
	if err := runPortGroupSet(context.Background(), client, o, "bond0", f, &buf); err != nil {
		t.Fatal(err)
	}
	jsonEq(t, "set patch", fake.call("PATCH /portgroups/bond0").Body, `[
		{"op":"replace","path":"/mode","value":"active-backup"},
		{"op":"add","path":"/properties/miimon","value":"200"},
		{"op":"replace","path":"/standalone_ports_supported","value":true}]`)

	fake.calls = nil
	u := &portGroupUnsetFlags{name: true, physicalNetwork: true, extra: []string{"a/b"}}
	if err := runPortGroupUnset(context.Background(), client, o, "bond0", u, &buf); err != nil {
		t.Fatal(err)
	}
	jsonEq(t, "unset patch", fake.call("PATCH /portgroups/bond0").Body, `[
		{"op":"remove","path":"/name"},{"op":"remove","path":"/physical_network"},
		{"op":"remove","path":"/extra/a~1b"}]`)
}

func TestRunPortGroupSet_PhysnetOnOldCloudNamesVersion(t *testing.T) {
	client, _ := newFakeIronic(t, map[string]fakeRoute{
		"PATCH /portgroups/bond0": {http.StatusBadRequest, map[string]any{"error_message": "Cannot patch /physical_network"}},
	})
	o := &output.Options{Format: output.FormatTable}
	err := runPortGroupSet(context.Background(), client, o, "bond0", &portGroupAttrFlags{physicalNetwork: "physnet1"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "requires ironic API 1.102 (OpenStack 2026.1); this cloud supports up to 1.96") {
		t.Errorf("err = %v", err)
	}
}

func TestRunPortGroupDelete_Each(t *testing.T) {
	client, fake := newFakeIronic(t, map[string]fakeRoute{
		"DELETE /portgroups/bond0": {http.StatusNoContent, nil},
		"DELETE /portgroups/bond1": {http.StatusNotFound, map[string]any{"error_message": "not found"}},
	})
	var buf bytes.Buffer
	err := runPortGroupDelete(context.Background(), client, []string{"bond0", "bond1"}, &buf)
	if err == nil || !strings.Contains(err.Error(), "bond1") {
		t.Errorf("err = %v, want the bond1 failure", err)
	}
	if buf.String() != "Deleted port group bond0\n" {
		t.Errorf("output = %q", buf.String())
	}
	fake.call("DELETE /portgroups/bond0")
}

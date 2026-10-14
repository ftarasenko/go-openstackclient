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
	testParentVIF = "aaaaaaaa-0000-0000-0000-00000000000a"
	testTrunk     = "bbbbbbbb-0000-0000-0000-00000000000b"
	testParentMAC = "52:54:00:00:00:01"
)

// wiredNode is a bonded node: port group bond0 with two members (one never
// cabled in ironic), carrying a trunk parent VIF with two subports, one of
// which has its own MAC and is DOWN. A third port has no VIF.
func wiredNode(t *testing.T, parentVIFType string) (ironic, neutron *fakeIronic, run func(neutronUp bool) (string, string)) {
	t.Helper()
	llc := map[string]any{"switch_info": "leaf1", "switch_id": "00:00:5e:00:53:01", "port_id": "Ethernet1/1"}
	ironicClient, ironicFake := newFakeIronic(t, map[string]fakeRoute{
		"GET /nodes/bm-1": {http.StatusOK, map[string]any{"uuid": testNodeUUID, "name": "bm-1"}},
		"GET /ports/detail": {http.StatusOK, map[string]any{"ports": []any{
			map[string]any{"uuid": "p1", "address": testParentMAC, "portgroup_uuid": testPGUUID, "pxe_enabled": true,
				"physical_network": "physnet1", "local_link_connection": llc, "internal_info": map[string]any{}},
			map[string]any{"uuid": "p2", "address": "52:54:00:00:00:02", "portgroup_uuid": testPGUUID, "pxe_enabled": false,
				"physical_network": "physnet1", "local_link_connection": map[string]any{}, "internal_info": map[string]any{}},
			map[string]any{"uuid": "p3", "address": "52:54:00:00:00:03", "pxe_enabled": false,
				"local_link_connection": llc, "internal_info": map[string]any{}},
		}}},
		"GET /portgroups/detail": {http.StatusOK, map[string]any{"portgroups": []any{
			map[string]any{"uuid": testPGUUID, "name": "bond0", "address": testParentMAC,
				"internal_info": map[string]any{"tenant_vif_port_id": testParentVIF}},
		}}},
	})
	neutronClient, neutronFake := newFakeIronic(t, map[string]fakeRoute{
		"GET /ports/" + testParentVIF: {http.StatusOK, map[string]any{"port": map[string]any{
			"id": testParentVIF, "network_id": "net-a", "mac_address": testParentMAC, "status": "ACTIVE",
			"binding:vif_type":    parentVIFType,
			"binding:vif_details": map[string]any{"bound_drivers": map[string]any{"0": "baremetal", "1": "genericswitch"}},
			"trunk_details":       map[string]any{"trunk_id": testTrunk},
		}}},
		"GET /trunks/" + testTrunk: {http.StatusOK, map[string]any{"trunk": map[string]any{
			"id": testTrunk, "status": "ACTIVE", "sub_ports": []any{
				map[string]any{"port_id": "sub-1", "segmentation_id": 101, "segmentation_type": "vlan"},
				map[string]any{"port_id": "sub-2", "segmentation_id": 102, "segmentation_type": "vlan"},
			},
		}}},
		"GET /ports/sub-1": {http.StatusOK, map[string]any{"port": map[string]any{
			"id": "sub-1", "network_id": "net-b", "mac_address": testParentMAC, "status": "ACTIVE",
		}}},
		"GET /ports/sub-2": {http.StatusOK, map[string]any{"port": map[string]any{
			"id": "sub-2", "network_id": "net-c", "mac_address": "fa:16:3e:00:00:02", "status": "DOWN",
		}}},
		"GET /networks/net-a": {http.StatusOK, map[string]any{"network": map[string]any{"id": "net-a", "name": "native", "provider:segmentation_id": 100}}},
		"GET /networks/net-b": {http.StatusOK, map[string]any{"network": map[string]any{"id": "net-b", "name": "vlan101"}}},
		"GET /networks/net-c": {http.StatusOK, map[string]any{"network": map[string]any{"id": "net-c", "name": "vlan102"}}},
	})
	return ironicFake, neutronFake, func(neutronUp bool) (string, string) {
		t.Helper()
		var out, warn bytes.Buffer
		n := neutronClient
		if !neutronUp {
			n = nil
		}
		o := &output.Options{Format: output.FormatCSV}
		if err := runNodeNetworkShow(context.Background(), ironicClient, n, o, "bm-1", &out, &warn); err != nil {
			t.Fatal(err)
		}
		return out.String(), warn.String()
	}
}

func TestNodeNetworkShow_JoinsIronicAndNeutron(t *testing.T) {
	ironic, _, run := wiredNode(t, "other")
	out, warn := run(true)

	if q := ironic.call("GET /ports/detail").Query; q != "node="+testNodeUUID {
		t.Errorf("ports listed with %q, want the node's UUID", q)
	}
	want := `Kind,Ironic,Name,MAC,PXE,Physical Network,Port Group,Switch,Switch Port,VIF,Network,VLAN,Status,VIF Type,Bound Drivers,Trunk
portgroup,` + testPGUUID + `,bond0,` + testParentMAC + `,,,,,,` + testParentVIF + `,native,100,ACTIVE,other,"baremetal,genericswitch",` + testTrunk + `
port,p1,,` + testParentMAC + `,true,physnet1,bond0,leaf1,Ethernet1/1,,,,,,,
port,p2,,52:54:00:00:00:02,false,physnet1,bond0,,,,,,,,,
subport,,,` + testParentMAC + `,,,,,,sub-1,vlan101,101,ACTIVE,,,` + testTrunk + `
subport,,,fa:16:3e:00:00:02,,,,,,sub-2,vlan102,102,DOWN,,,` + testTrunk + `
port,p3,,52:54:00:00:00:03,false,,,leaf1,Ethernet1/1,,,,,,,
`
	if out != want {
		t.Errorf("table =\n%s\nwant\n%s", out, want)
	}
	for _, w := range []string{
		"VIF " + testParentVIF + " is on port group bond0, but ironic port 52:54:00:00:00:02 has no local_link_connection",
		"subport sub-2 (VLAN 102) has MAC fa:16:3e:00:00:02, parent " + testParentVIF + " has " + testParentMAC,
		"trunk " + testTrunk + " is ACTIVE but subport sub-2 (VLAN 102) is DOWN",
	} {
		if !strings.Contains(warn, w) {
			t.Errorf("warnings missing %q:\n%s", w, warn)
		}
	}
	if strings.Count(warn, "warning: ") != 3 {
		t.Errorf("want exactly 3 warnings:\n%s", warn)
	}
}

func TestNodeNetworkShow_FlagsFailedBinding(t *testing.T) {
	_, _, run := wiredNode(t, "binding_failed")
	_, warn := run(true)
	if !strings.Contains(warn, "VIF "+testParentVIF+" on port group bond0: binding:vif_type is binding_failed") {
		t.Errorf("warnings:\n%s", warn)
	}
}

// With no neutron (--creds-from-ns), the ironic side still renders and only
// the ironic-only check runs.
func TestNodeNetworkShow_IronicOnly(t *testing.T) {
	_, neutron, run := wiredNode(t, "other")
	out, warn := run(false)
	if len(neutron.calls) != 0 {
		t.Errorf("neutron was called: %v", neutron.calls)
	}
	if !strings.Contains(out, "portgroup,"+testPGUUID+",bond0") || strings.Contains(out, "subport") {
		t.Errorf("table:\n%s", out)
	}
	if strings.Count(warn, "warning: ") != 1 || !strings.Contains(warn, "no local_link_connection") {
		t.Errorf("warnings:\n%s", warn)
	}
}

package baremetal

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// versionedNode is a node as ironic renders it: fields introduced after the
// requested microversion are left out, as ironic's api/controllers/v1/node.py
// hides them. The minimums are from versions.py.
func versionedNode(r *http.Request) map[string]any {
	n := map[string]any{
		"uuid": "11111111-1111-1111-1111-111111111111", "name": "node-a",
		"instance_uuid": nil, "power_state": "power on", "provision_state": "active",
		"maintenance": false, "driver": "ipmi", "target_provision_state": nil,
		"links": []any{}, "ports": []any{},
	}
	gated := []struct {
		min    string
		fields map[string]any
	}{
		{"1.20", map[string]any{"network_interface": "neutron"}},
		{"1.21", map[string]any{"resource_class": "CUSTOM_BM_2X14C"}},
		{"1.31", map[string]any{
			"boot_interface": "ipxe", "console_interface": "no-console", "deploy_interface": "direct",
			"management_interface": "ipmitool", "power_interface": "ipmitool",
		}},
	}
	v := r.Header.Get("X-OpenStack-Ironic-API-Version")
	for _, g := range gated {
		if v == "latest" || compareMicroversions(v, g.min) >= 0 {
			for k, val := range g.fields {
				n[k] = val
			}
		}
	}
	return n
}

func TestRunNodeShow_RendersFieldsTheNegotiatedVersionReturns(t *testing.T) {
	for _, tc := range []struct {
		microversion string
		want         any // network_interface
	}{
		{"latest", "neutron"},
		{"1.1", nil}, // what ironic answers with no header at all
	} {
		t.Run(tc.microversion, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()
			fakeServer.Mux.HandleFunc("/nodes/node-a", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(versionedNode(r))
			})

			o := &output.Options{Format: output.FormatJSON}
			var buf bytes.Buffer
			if err := runNodeShow(context.Background(), baremetalClient(fakeServer, tc.microversion), o, "node-a", &buf); err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got["network_interface"] != tc.want {
				t.Errorf("network_interface = %v, want %v", got["network_interface"], tc.want)
			}
			// Every detailed field is a key, null when this version omits it, so
			// `jq .boot_interface` distinguishes "absent" from "not rendered".
			for _, k := range []string{"boot_interface", "deploy_interface", "power_interface", "resource_class", "health"} {
				if _, ok := got[k]; !ok {
					t.Errorf("node show -f json has no %q key", k)
				}
			}
			for _, k := range []string{"links", "ports"} {
				if _, ok := got[k]; ok {
					t.Errorf("node show renders navigation key %q", k)
				}
			}
			if tc.microversion == "latest" && got["boot_interface"] != "ipxe" {
				t.Errorf("boot_interface = %v, want ipxe", got["boot_interface"])
			}
		})
	}
}

func TestRunNodeList_LongReadsDetail(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/nodes", func(w http.ResponseWriter, _ *http.Request) {
		t.Error("--long listed /nodes, whose rows carry only the six short fields")
		w.WriteHeader(http.StatusInternalServerError)
	})
	fakeServer.Mux.HandleFunc("/nodes/detail", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"nodes": []any{versionedNode(r)}})
	})

	o := &output.Options{Format: output.FormatJSON}
	var buf bytes.Buffer
	if err := runNodeList(context.Background(), baremetalClient(fakeServer, "latest"), o, &nodeListFlags{long: true}, &buf); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	for k, want := range map[string]any{
		"Driver": "ipmi", "Resource Class": "CUSTOM_BM_2X14C", "Provisioning State": "active",
		"Network Interface": "neutron", "Boot Interface": "ipxe",
	} {
		if rows[0][k] != want {
			t.Errorf("%q = %v, want %v", k, rows[0][k], want)
		}
	}
	if len(rows[0]) != len(nodeDetailFields) {
		t.Errorf("--long has %d columns, want python-ironicclient's %d", len(rows[0]), len(nodeDetailFields))
	}
}

// A -c naming a detail-only column is answered from /nodes/detail without
// --long; one naming only short columns stays on the cheap listing.
func TestRunNodeList_DetailColumnSelectsDetail(t *testing.T) {
	for _, tc := range []struct {
		columns []string
		path    string
	}{
		{[]string{"Name", "Resource Class"}, "/nodes/detail"},
		{[]string{"Name", "Provisioning State"}, "/nodes"},
	} {
		t.Run(strings.Join(tc.columns, ","), func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()
			var got string
			handler := func(w http.ResponseWriter, r *http.Request) {
				got = r.URL.Path
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"nodes": []any{versionedNode(r)}})
			}
			fakeServer.Mux.HandleFunc("/nodes", handler)
			fakeServer.Mux.HandleFunc("/nodes/detail", handler)

			o := &output.Options{Format: output.FormatValue, Columns: tc.columns}
			var buf bytes.Buffer
			if err := runNodeList(context.Background(), baremetalClient(fakeServer, "latest"), o, &nodeListFlags{}, &buf); err != nil {
				t.Fatal(err)
			}
			if got != tc.path {
				t.Errorf("listed %s, want %s", got, tc.path)
			}
			if tc.path == "/nodes/detail" && strings.TrimSpace(buf.String()) != "node-a CUSTOM_BM_2X14C" {
				t.Errorf("output = %q", buf.String())
			}
		})
	}
}

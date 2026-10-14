package baremetal

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// boundPort is a port as ironic 1.101 returns it once a VIF is bound.
func boundPort(uuid, physnet string) map[string]any {
	return map[string]any{
		"uuid": uuid, "address": "52:54:00:00:00:01", "name": "eth0",
		"node_uuid": "11111111-1111-1111-1111-111111111111", "portgroup_uuid": "99999999-0000-0000-0000-000000000001",
		"physical_network": physnet, "pxe_enabled": true, "is_smartnic": false,
		"category": "lom", "vendor": "intel", "description": "uplink A",
		"local_link_connection": map[string]any{"switch_id": "00:00:5e:00:53:01", "port_id": "Ethernet1/1"},
		"internal_info":         map[string]any{"tenant_vif_port_id": "cccccccc-0000-0000-0000-000000000001"},
		"extra":                 map[string]any{}, "links": []any{},
	}
}

func TestRunPortShow_RendersEveryField(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/ports/p1", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(boundPort("p1", "physnet1"))
	})

	o := &output.Options{Format: output.FormatJSON}
	var buf bytes.Buffer
	if err := runPortShow(context.Background(), baremetalClient(fakeServer, "latest"), o, "p1", &buf); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]any{"name": "eth0", "category": "lom", "vendor": "intel", "description": "uplink A"} {
		if got[k] != want {
			t.Errorf("%s = %v, want %v", k, got[k], want)
		}
	}
	if ii, _ := got["internal_info"].(map[string]any); ii["tenant_vif_port_id"] != "cccccccc-0000-0000-0000-000000000001" {
		t.Errorf("internal_info = %v", got["internal_info"])
	}
	if _, ok := got["links"]; ok {
		t.Error("port show renders links")
	}
}

func TestRunPortList_LongFiltersAndColumns(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	var query string
	fakeServer.Mux.HandleFunc("/ports/detail", func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ports": []any{
			boundPort("p1", "physnet1"), boundPort("p2", "physnet2"), boundPort("p3", "physnet1"),
		}})
	})

	o := &output.Options{Format: output.FormatJSON}
	var buf bytes.Buffer
	f := &portListFlags{long: true, node: "bm-1", portGroup: "bond0", physicalNetwork: "physnet1", limit: 1}
	if err := runPortList(context.Background(), baremetalClient(fakeServer, "latest"), o, f, &buf); err != nil {
		t.Fatal(err)
	}
	// --limit is applied after the client-side physical-network filter, so it
	// is not sent as the page size.
	if query != "node=bm-1&portgroup=bond0" {
		t.Errorf("query = %q, want node=bm-1&portgroup=bond0", query)
	}
	var rows []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0]["UUID"] != "p1" {
		t.Fatalf("rows = %v, want only p1", rows)
	}
	for _, col := range []string{"Local Link Connection", "Internal Info", "Name", "Category", "Vendor", "Portgroup UUID"} {
		if rows[0][col] == nil {
			t.Errorf("--long row has no %q", col)
		}
	}
}

func TestRunPortList_ShortStaysOnPlainList(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	var query string
	fakeServer.Mux.HandleFunc("/ports", func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ports":[{"uuid":"p1","address":"52:54:00:00:00:01"}]}`))
	})

	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	if err := runPortList(context.Background(), baremetalClient(fakeServer, "latest"), o, &portListFlags{portGroup: "bond 0"}, &buf); err != nil {
		t.Fatal(err)
	}
	if query != "portgroup=bond+0" {
		t.Errorf("query = %q", query)
	}
	if buf.String() != "p1 52:54:00:00:00:01\n" {
		t.Errorf("output = %q", buf.String())
	}
}

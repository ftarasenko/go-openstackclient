package network

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// A single-segment network carries provider:* itself; a multi-segment one
// carries only "segments" (neutron's multiprovidernet), and its columns join
// the per-segment values.
const providerListBody = `{"networks": [
  {"id": "n1", "name": "vlan100", "subnets": [], "provider:network_type": "vlan",
   "provider:physical_network": "physnet1", "provider:segmentation_id": 100},
  {"id": "n2", "name": "multi", "subnets": [], "segments": [
    {"provider:network_type": "vlan", "provider:physical_network": "physnet1", "provider:segmentation_id": 200},
    {"provider:network_type": "vlan", "provider:physical_network": "physnet2", "provider:segmentation_id": 201}]},
  {"id": "n3", "name": "tenant", "subnets": [], "provider:network_type": "geneve",
   "provider:physical_network": null, "provider:segmentation_id": 7}
]}`

func providerListServer(t *testing.T) th.FakeServer {
	t.Helper()
	fakeServer := th.SetupHTTP()
	t.Cleanup(fakeServer.Teardown)
	fakeServer.Mux.HandleFunc("/networks", func(w http.ResponseWriter, r *http.Request) {
		// The columns come from the listing itself: no fields= narrowing, no
		// per-network show.
		if r.URL.RawQuery != "" {
			t.Errorf("query = %q, want none", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(providerListBody))
	})
	return fakeServer
}

func TestRunNetworkList_ProviderColumns(t *testing.T) {
	for _, tc := range []struct {
		name    string
		f       networkListFlags
		columns []string
		want    string
	}{
		{
			name: "--provider",
			f:    networkListFlags{provider: true},
			want: `[
			  {"ID":"n1","Name":"vlan100","Subnets":[],"Network Type":"vlan","Provider Physical Network":"physnet1","Provider Segment":"100"},
			  {"ID":"n2","Name":"multi","Subnets":[],"Network Type":"vlan,vlan","Provider Physical Network":"physnet1,physnet2","Provider Segment":"200,201"},
			  {"ID":"n3","Name":"tenant","Subnets":[],"Network Type":"geneve","Provider Physical Network":null,"Provider Segment":"7"}]`,
		},
		{
			name:    "-c on the short listing",
			columns: []string{"Name", "Provider Segment"},
			want:    `[{"Name":"vlan100","Provider Segment":"100"},{"Name":"multi","Provider Segment":"200,201"},{"Name":"tenant","Provider Segment":"7"}]`,
		},
		{
			// --long already has Network Type; --provider adds the other two
			// without a duplicated header.
			name:    "--long --provider",
			f:       networkListFlags{long: true, provider: true},
			columns: []string{"Name", "Network Type", "Provider Physical Network"},
			want:    `[{"Name":"vlan100","Network Type":"vlan","Provider Physical Network":"physnet1"},{"Name":"multi","Network Type":"vlan,vlan","Provider Physical Network":"physnet1,physnet2"},{"Name":"tenant","Network Type":"geneve","Provider Physical Network":null}]`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := providerListServer(t)
			o := &output.Options{Format: output.FormatJSON, Columns: tc.columns}
			var buf bytes.Buffer
			if err := runNetworkList(context.Background(), networkClient(fakeServer), o, &tc.f, "", &buf); err != nil {
				t.Fatal(err)
			}
			var got, want any
			if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(tc.want), &want); err != nil {
				t.Fatal(err)
			}
			g, _ := json.Marshal(got)
			wb, _ := json.Marshal(want)
			if string(g) != string(wb) {
				t.Errorf("output =\n  %s\nwant\n  %s", g, wb)
			}
		})
	}
}

// Without --provider or -c the default listing is unchanged.
func TestRunNetworkList_ProviderColumnsAreOptIn(t *testing.T) {
	fakeServer := providerListServer(t)
	o := &output.Options{Format: output.FormatJSON}
	var buf bytes.Buffer
	if err := runNetworkList(context.Background(), networkClient(fakeServer), o, &networkListFlags{long: true}, "", &buf); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	for _, col := range []string{"Provider Physical Network", "Provider Segment"} {
		if _, ok := rows[0][col]; ok {
			t.Errorf("--long without --provider has %q", col)
		}
	}
}

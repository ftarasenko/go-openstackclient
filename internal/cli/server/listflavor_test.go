package server

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Below nova 2.47 the server response carries the flavor's ID and not its name,
// and serverListMicroversion pins the listing at 2.1 by default — so the Flavor
// column that is now in the default listing would be a page of UUIDs without
// the flavor lookup behind it.
const serverListBodyFlavorByID = `{
  "servers": [
    {
      "id": "11111111-1111-1111-1111-111111111111",
      "name": "web-1",
      "status": "ACTIVE",
      "addresses": {},
      "flavor": {"id": "ffffffff-0000-0000-0000-000000000001"},
      "image": {"id": "img-123"}
    }
  ]
}`

const flavorListBody = `{
  "flavors": [
    {"id": "ffffffff-0000-0000-0000-000000000001", "name": "m1.medium", "vcpus": 2, "ram": 4096, "disk": 40}
  ]
}`

// listWithFlavors serves a server listing and counts the flavor lookups behind
// it.
func listWithFlavors(t *testing.T, serverBody string, flavorHits *atomic.Int64) *th.FakeServer {
	t.Helper()
	fakeServer := th.SetupHTTP()
	t.Cleanup(fakeServer.Teardown)
	fakeServer.Mux.HandleFunc("/servers/detail", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(serverBody))
	})
	fakeServer.Mux.HandleFunc("/flavors/detail", func(w http.ResponseWriter, _ *http.Request) {
		flavorHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(flavorListBody))
	})
	return &fakeServer
}

func TestServerListResolvesFlavorNamesBelow247(t *testing.T) {
	var hits atomic.Int64
	fakeServer := listWithFlavors(t, serverListBodyFlavorByID, &hits)

	var buf bytes.Buffer
	o := &output.Options{Format: output.FormatTable}
	if err := runServerList(context.Background(), computeClient(*fakeServer, "2.1"), o,
		&serverListFlags{}, "", "", &buf); err != nil {
		t.Fatalf("runServerList: %v", err)
	}
	if !strings.Contains(buf.String(), "m1.medium") {
		t.Errorf("the Flavor column shows an ID rather than a name:\n%s", buf.String())
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("issued %d flavor listings, want 1 for the whole page", got)
	}
}

func TestServerListSkipsTheFlavorLookupWhenNotShowingFlavor(t *testing.T) {
	// The column is in the default listing now, so a narrowed one must not buy
	// a flavor listing for something it is not rendering — least of all once a
	// refresh a second under --watch.
	var hits atomic.Int64
	fakeServer := listWithFlavors(t, serverListBodyFlavorByID, &hits)

	var buf bytes.Buffer
	o := &output.Options{Format: output.FormatTable, Columns: []string{"Name", "Status"}}
	if err := runServerList(context.Background(), computeClient(*fakeServer, "2.1"), o,
		&serverListFlags{}, "", "", &buf); err != nil {
		t.Fatalf("runServerList: %v", err)
	}
	if got := hits.Load(); got != 0 {
		t.Errorf("issued %d flavor listings for a table without a Flavor column", got)
	}
}

func TestServerListFetchesFlavorNamesWhenFlavorIsSelected(t *testing.T) {
	var hits atomic.Int64
	fakeServer := listWithFlavors(t, serverListBodyFlavorByID, &hits)

	var buf bytes.Buffer
	o := &output.Options{Format: output.FormatTable, Columns: []string{"Name", "Flavor"}}
	if err := runServerList(context.Background(), computeClient(*fakeServer, "2.1"), o,
		&serverListFlags{}, "", "", &buf); err != nil {
		t.Fatalf("runServerList: %v", err)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("issued %d flavor listings, want 1", got)
	}
	if !strings.Contains(buf.String(), "m1.medium") {
		t.Errorf("-c Flavor did not render the name:\n%s", buf.String())
	}
}

func TestWantsFlavorNames(t *testing.T) {
	for _, tc := range []struct {
		name string
		o    *output.Options
		f    *serverListFlags
		want bool
	}{
		{"default listing", &output.Options{}, &serverListFlags{}, true},
		{"--long", &output.Options{}, &serverListFlags{long: true}, true},
		{"-c Flavor", &output.Options{Columns: []string{"Flavor"}}, &serverListFlags{}, true},
		{"--sort-column Flavor", &output.Options{SortColumns: []string{"Flavor"}}, &serverListFlags{}, true},
		{"-c Name", &output.Options{Columns: []string{"Name"}}, &serverListFlags{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := wantsFlavorNames(tc.o, tc.f); got != tc.want {
				t.Errorf("wantsFlavorNames = %v, want %v", got, tc.want)
			}
		})
	}
}

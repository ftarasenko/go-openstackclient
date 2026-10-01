package server

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"
	fakeclient "github.com/gophercloud/gophercloud/v2/testhelper/client"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// upstreamListBody is one image-booted and one volume-booted server, at a
// microversion below 2.47 (flavor by ID) as koc's pinned listing sees them.
const upstreamListBody = `{"servers":[
 {"id":"s1","name":"web-1","status":"ACTIVE","addresses":{"net":[{"addr":"192.0.2.5"}]},
  "image":{"id":"img-1"},"flavor":{"id":"f1"},"OS-EXT-STS:task_state":null,"OS-EXT-STS:power_state":1,
  "OS-EXT-AZ:availability_zone":"nova","OS-EXT-SRV-ATTR:host":"cmp-1",
  "OS-EXT-SRV-ATTR:hypervisor_hostname":"cmp-1.example.com","metadata":{},
  "host_status":"UP","scheduler_hints":{"group":["g1"]}},
 {"id":"s2","name":"db-1","status":"SHUTOFF","addresses":{},
  "image":"","flavor":{"id":"f1"},"OS-EXT-STS:power_state":4,
  "OS-EXT-SRV-ATTR:hypervisor_hostname":"cmp-2.example.com","metadata":{}}
]}`

// upstreamListMock serves upstreamListBody and a one-flavor listing, recording
// the listing's microversion and how often the flavors were listed.
func upstreamListMock(t *testing.T) (th.FakeServer, *string, *int) {
	t.Helper()
	fakeServer := th.SetupHTTP()
	t.Cleanup(fakeServer.Teardown)
	var mv string
	var flavorCalls int
	fakeServer.Mux.HandleFunc("/servers/detail", func(w http.ResponseWriter, r *http.Request) {
		mv = r.Header.Get("OpenStack-API-Version")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(upstreamListBody))
	})
	fakeServer.Mux.HandleFunc("/flavors/detail", func(w http.ResponseWriter, _ *http.Request) {
		flavorCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"flavors":[{"id":"f1","name":"m1.small"}]}`))
	})
	return fakeServer, &mv, &flavorCalls
}

func staticNamer(calls *int) imageNamer {
	return func(_ context.Context, ids []string) map[string]string {
		*calls++
		if len(ids) != 1 || ids[0] != "img-1" {
			return nil
		}
		return map[string]string{"img-1": "cirros"}
	}
}

// The default and --long listings carry upstream's columns in upstream's order
// (python-openstackclient compute/v2/server.py, ListServer).
func TestServerList_UpstreamColumns(t *testing.T) {
	for _, tc := range []struct {
		name string
		long bool
		want string
	}{
		{
			name: "default",
			want: "ID,Name,Status,Networks,Image,Flavor\n" +
				"s1,web-1,ACTIVE,net=192.0.2.5,cirros,m1.small\n" +
				"s2,db-1,SHUTOFF,,N/A (booted from volume),m1.small\n",
		},
		{
			name: "long",
			long: true,
			want: "ID,Name,Status,Task State,Power State,Networks,Image Name,Image ID,Flavor,Availability Zone,Host,Properties\n" +
				"s1,web-1,ACTIVE,,Running,net=192.0.2.5,cirros,img-1,m1.small,nova,cmp-1.example.com,\n" +
				"s2,db-1,SHUTOFF,,Shutdown,,N/A (booted from volume),N/A (booted from volume),m1.small,,cmp-2.example.com,\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer, _, _ := upstreamListMock(t)
			var lookups int
			var buf bytes.Buffer
			err := runServerList(context.Background(), computeClient(fakeServer, "latest"),
				&output.Options{Format: output.FormatCSV},
				&serverListFlags{long: tc.long, imageNames: staticNamer(&lookups)}, "", "", &buf)
			if err != nil {
				t.Fatalf("runServerList: %v", err)
			}
			if buf.String() != tc.want {
				t.Errorf("got:\n%s\nwant:\n%s", buf.String(), tc.want)
			}
			if lookups != 1 {
				t.Errorf("image names looked up %d times, want once for the page", lookups)
			}
		})
	}
}

// --no-name-lookup shows IDs and asks neither glance nor the flavor listing;
// an image the lookup cannot name falls back to its ID.
func TestServerList_NoNameLookup(t *testing.T) {
	fakeServer, _, flavorCalls := upstreamListMock(t)
	var lookups int
	var buf bytes.Buffer
	err := runServerList(context.Background(), computeClient(fakeServer, "latest"),
		&output.Options{Format: output.FormatCSV, Columns: []string{"Name", "Image", "Flavor"}},
		&serverListFlags{noNameLookup: true, imageNames: staticNamer(&lookups)}, "", "", &buf)
	if err != nil {
		t.Fatalf("runServerList: %v", err)
	}
	want := "Name,Image,Flavor\nweb-1,img-1,f1\ndb-1,N/A (booted from volume),f1\n"
	if buf.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", buf.String(), want)
	}
	if lookups != 0 || *flavorCalls != 0 {
		t.Errorf("lookups: images %d, flavors %d; want none", lookups, *flavorCalls)
	}
}

// -c and --sort-column take upstream's attribute spellings as well as headers.
func TestServerList_SnakeCaseColumns(t *testing.T) {
	fakeServer, _, _ := upstreamListMock(t)
	var lookups int
	var buf bytes.Buffer
	o := &output.Options{Format: output.FormatCSV,
		Columns: []string{"name", "host", "power_state", "image_name"}, SortColumns: []string{"name"}}
	err := runServerList(context.Background(), computeClient(fakeServer, "latest"), o,
		&serverListFlags{imageNames: staticNamer(&lookups)}, "", "", &buf)
	if err != nil {
		t.Fatalf("runServerList: %v", err)
	}
	want := "Name,Host,Power State,Image\n" +
		"db-1,cmp-2.example.com,Shutdown,N/A (booted from volume)\n" +
		"web-1,cmp-1.example.com,Running,cirros\n"
	if buf.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", buf.String(), want)
	}
}

// A column nova adds at a later microversion raises the pinned listing to it.
func TestServerList_RawColumnsRaiseMicroversion(t *testing.T) {
	fakeServer, mv, _ := upstreamListMock(t)
	var buf bytes.Buffer
	o := &output.Options{Format: output.FormatCSV, Columns: []string{"Name", "Host Status", "scheduler_hints"}}
	err := runServerList(context.Background(), computeClient(fakeServer, "latest"), o,
		&serverListFlags{pinMicroversion: true}, "", "", &buf)
	if err != nil {
		t.Fatalf("runServerList: %v", err)
	}
	if *mv != "compute 2.100" {
		t.Errorf("microversion = %q, want compute 2.100", *mv)
	}
	want := "Name,Host Status,Scheduler Hints\nweb-1,UP,group='g1'\ndb-1,,\n"
	if buf.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", buf.String(), want)
	}
}

// Names come from one glance id=in: query per chunk of IDs.
func TestLookupImageNames(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	var queries []string
	fakeServer.Mux.HandleFunc("/images", func(w http.ResponseWriter, r *http.Request) {
		ids := strings.TrimPrefix(r.URL.Query().Get("id"), "in:")
		queries = append(queries, ids)
		var body []string
		for _, id := range strings.Split(ids, ",") {
			if id == "gone" {
				continue
			}
			body = append(body, `{"id":"`+id+`","name":"name-`+id+`"}`)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"images":[` + strings.Join(body, ",") + `]}`))
	})
	ids := []string{"gone"}
	for i := range imageNameChunk {
		ids = append(ids, "i"+string(rune('a'+i%26))+string(rune('a'+i/26)))
	}
	names := lookupImageNames(context.Background(), fakeclient.ServiceClient(fakeServer), ids)
	if len(queries) != 2 {
		t.Errorf("%d glance requests, want 2 for %d IDs", len(queries), len(ids))
	}
	if len(names) != imageNameChunk || names["iaa"] != "name-iaa" {
		t.Errorf("names = %v", names)
	}
	if _, ok := names["gone"]; ok {
		t.Error("a deleted image was named")
	}
}

func TestNameServerImage(t *testing.T) {
	named := func(context.Context, []string) map[string]string {
		return map[string]string{"img-1": "cirros"}
	}
	for _, tc := range []struct {
		name   string
		image  any
		lookup imageNamer
		want   any
	}{
		{"named", map[string]any{"id": "img-1"}, named, "cirros (img-1)"},
		{"unknown name", map[string]any{"id": "img-2"}, named, "img-2"},
		{"no lookup", map[string]any{"id": "img-1"}, nil, "img-1"},
		{"volume-booted", "", named, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := map[string]any{"image": tc.image}
			nameServerImage(context.Background(), server, tc.lookup)
			if server["image"] != tc.want {
				t.Errorf("image = %v, want %v", server["image"], tc.want)
			}
		})
	}
}

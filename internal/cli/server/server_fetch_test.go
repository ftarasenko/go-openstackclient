package server

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// The compute microversion decides how much of each server nova ships: at 2.3 it
// starts embedding OS-EXT-SRV-ATTR:user_data, which the table never shows and
// which dominates the response. These tests pin the negotiated version to the
// least one the requested columns and filters need.

func TestServerListMicroversion_LowestThatAnswersTheRequest(t *testing.T) {
	for _, tc := range []struct {
		name string
		f    serverListFlags
		want string
	}{
		{"default table", serverListFlags{}, "2.1"},
		{"long is resolved client-side", serverListFlags{long: true}, "2.1"},
		{"all projects", serverListFlags{all: true}, "2.1"},
		{"deleted filter is in the 2.1 schema", serverListFlags{deleted: true}, "2.1"},
		{"keystack created-since", serverListFlags{createdSince: "2026-01-01T00:00:00Z"}, "2.66"},
		{"keystack deleted-before", serverListFlags{deletedBefore: "2026-01-01T00:00:00Z"}, "2.66"},
		{"changes-since needs nothing above 2.1", serverListFlags{changesSince: "2026-01-01T00:00:00Z"}, "2.1"},
		{"changes-before", serverListFlags{changesBefore: "2026-01-01T00:00:00Z"}, "2.66"},
		{"user filter", serverListFlags{user: "u-1"}, "2.83"},
		{"user filter outranks the time filters", serverListFlags{user: "u-1", createdSince: "x"}, "2.83"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := serverListMicroversion(&tc.f); got != tc.want {
				t.Errorf("serverListMicroversion() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRunServerList_PinnedMicroversionIsSentAndOutputIsUnchanged(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotAPIVersion, gotNovaVersion string
	fakeServer.Mux.HandleFunc("/servers/detail", func(w http.ResponseWriter, r *http.Request) {
		gotAPIVersion = r.Header.Get("OpenStack-API-Version")
		gotNovaVersion = r.Header.Get("X-OpenStack-Nova-API-Version")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(serverListBody))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runServerList(context.Background(), computeClient(fakeServer, "latest"), o,
		&serverListFlags{pinMicroversion: true}, "", "", &buf)
	if err != nil {
		t.Fatalf("runServerList returned error: %v", err)
	}

	if gotAPIVersion != "compute 2.1" {
		t.Errorf("OpenStack-API-Version = %q, want %q", gotAPIVersion, "compute 2.1")
	}
	if gotNovaVersion != "2.1" {
		t.Errorf("X-OpenStack-Nova-API-Version = %q, want 2.1", gotNovaVersion)
	}
	for _, want := range []string{"web-1", "web-2", "ACTIVE", "SHUTOFF", "private=10.0.0.5"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("table output missing %q\n---\n%s", want, buf.String())
		}
	}
}

func TestRunServerList_ExplicitMicroversionIsNotLowered(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotAPIVersion string
	fakeServer.Mux.HandleFunc("/servers/detail", func(w http.ResponseWriter, r *http.Request) {
		gotAPIVersion = r.Header.Get("OpenStack-API-Version")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(serverListBody))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	// pinMicroversion stays false when --os-compute-api-version was supplied.
	err := runServerList(context.Background(), computeClient(fakeServer, "2.79"), o,
		&serverListFlags{}, "", "", &buf)
	if err != nil {
		t.Fatalf("runServerList returned error: %v", err)
	}
	if gotAPIVersion != "compute 2.79" {
		t.Errorf("OpenStack-API-Version = %q, want %q", gotAPIVersion, "compute 2.79")
	}
}

// An ID/Name selection is answered by nova's non-detail listing, which returns
// exactly those two fields.
func TestRunServerList_IDOnlySelectionUsesTheBasicListing(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var basicHits int
	fakeServer.Mux.HandleFunc("/servers", func(w http.ResponseWriter, r *http.Request) {
		basicHits++
		th.TestFormValues(t, r, map[string]string{"all_tenants": "true"})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"servers":[{"id":"srv-1","name":"web-1"},{"id":"srv-2","name":"web-2"}]}`))
	})
	fakeServer.Mux.HandleFunc("/servers/detail", func(w http.ResponseWriter, _ *http.Request) {
		t.Error("an ID-only selection must not request the detail listing")
		w.WriteHeader(http.StatusInternalServerError)
	})

	o := &output.Options{Format: output.FormatValue, Columns: []string{"ID"}}
	var buf bytes.Buffer
	err := runServerList(context.Background(), computeClient(fakeServer, "latest"), o,
		&serverListFlags{all: true, pinMicroversion: true}, "", "", &buf)
	if err != nil {
		t.Fatalf("runServerList returned error: %v", err)
	}
	if basicHits != 1 {
		t.Errorf("GET /servers hits = %d, want 1", basicHits)
	}
	if got := buf.String(); got != "srv-1\nsrv-2\n" {
		t.Errorf("output = %q, want the two IDs", got)
	}
}

func TestServerListBasic_OnlyWhenNothingElseIsNeeded(t *testing.T) {
	for _, tc := range []struct {
		name string
		o    output.Options
		f    serverListFlags
		want bool
	}{
		{"id only", output.Options{Columns: []string{"ID"}}, serverListFlags{}, true},
		{"id and name, any case", output.Options{Columns: []string{"id", "name"}}, serverListFlags{}, true},
		{"no selection means every column", output.Options{}, serverListFlags{}, false},
		{"status needs the detail view", output.Options{Columns: []string{"ID", "Status"}}, serverListFlags{}, false},
		{"long needs the detail view", output.Options{Columns: []string{"ID"}}, serverListFlags{long: true}, false},
		{
			"a sort key still has to be fetched",
			output.Options{Columns: []string{"ID"}, SortColumns: []string{"Status"}},
			serverListFlags{}, false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := serverListBasic(&tc.o, &tc.f); got != tc.want {
				t.Errorf("serverListBasic() = %v, want %v", got, tc.want)
			}
		})
	}
}

// Below microversion 2.47 nova sends the flavor as a bare ID, so --long resolves
// the names with one flavor listing instead of paying for the wider response.
func TestRunServerList_LongResolvesFlavorNamesFromOneListing(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/servers/detail", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"servers":[
			{"id":"srv-1","name":"web-1","status":"ACTIVE","flavor":{"id":"fl-1"}},
			{"id":"srv-2","name":"web-2","status":"ACTIVE","flavor":{"id":"fl-unknown"}}
		]}`))
	})
	var flavorHits int
	var gotIsPublic string
	fakeServer.Mux.HandleFunc("/flavors/detail", func(w http.ResponseWriter, r *http.Request) {
		flavorHits++
		gotIsPublic = r.URL.Query().Get("is_public")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"flavors":[{"id":"fl-1","name":"m1.small"}]}`))
	})

	o := &output.Options{Format: output.FormatValue, Columns: []string{"Flavor"}}
	var buf bytes.Buffer
	err := runServerList(context.Background(), computeClient(fakeServer, "latest"), o,
		&serverListFlags{long: true, pinMicroversion: true}, "", "", &buf)
	if err != nil {
		t.Fatalf("runServerList returned error: %v", err)
	}
	if flavorHits != 1 {
		t.Errorf("flavor listings = %d, want 1 for the whole page", flavorHits)
	}
	if gotIsPublic != "None" {
		t.Errorf("is_public = %q, want None so private flavors resolve too", gotIsPublic)
	}
	// The unresolved flavor keeps its ID rather than rendering empty.
	if got := buf.String(); got != "m1.small\nfl-unknown\n" {
		t.Errorf("Flavor column = %q, want the name then the unresolved ID", got)
	}
}

func TestRunServerList_LongSkipsTheFlavorListingWhenNamesAreEmbedded(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/servers/detail", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(serverListBody))
	})
	fakeServer.Mux.HandleFunc("/flavors/detail", func(w http.ResponseWriter, _ *http.Request) {
		t.Error("embedded original_name must not trigger a flavor listing")
		w.WriteHeader(http.StatusInternalServerError)
	})

	o := &output.Options{Format: output.FormatValue, Columns: []string{"Flavor"}}
	var buf bytes.Buffer
	err := runServerList(context.Background(), computeClient(fakeServer, "2.79"), o,
		&serverListFlags{long: true}, "", "", &buf)
	if err != nil {
		t.Fatalf("runServerList returned error: %v", err)
	}
	if got := buf.String(); got != "m1.small\nm1.large\n" {
		t.Errorf("Flavor column = %q, want the embedded names", got)
	}
}

// A failed flavor listing degrades the column to IDs; it must not fail the list.
func TestRunServerList_LongSurvivesAFlavorListingFailure(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/servers/detail", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"servers":[{"id":"srv-1","name":"web-1","flavor":{"id":"fl-1"}}]}`))
	})
	fakeServer.Mux.HandleFunc("/flavors/detail", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})

	o := &output.Options{Format: output.FormatValue, Columns: []string{"Flavor"}}
	var buf bytes.Buffer
	err := runServerList(context.Background(), computeClient(fakeServer, "latest"), o,
		&serverListFlags{long: true, pinMicroversion: true}, "", "", &buf)
	if err != nil {
		t.Fatalf("runServerList returned error: %v", err)
	}
	if got := buf.String(); got != "fl-1\n" {
		t.Errorf("Flavor column = %q, want the unresolved ID", got)
	}
}

// resolveServerID reads only the ID and the name, so it uses the basic listing.
func TestResolveServerID_UsesTheBasicListing(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/servers", func(w http.ResponseWriter, r *http.Request) {
		th.TestFormValues(t, r, map[string]string{"name": "web-1", "all_tenants": "true"})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"servers":[{"id":"srv-1","name":"web-1"}]}`))
	})
	fakeServer.Mux.HandleFunc("/servers/detail", func(w http.ResponseWriter, _ *http.Request) {
		t.Error("a name lookup must not request the detail listing")
		w.WriteHeader(http.StatusInternalServerError)
	})

	id, err := resolveServerID(context.Background(), computeClient(fakeServer, "latest"), "web-1")
	if err != nil {
		t.Fatalf("resolveServerID: %v", err)
	}
	if id != "srv-1" {
		t.Errorf("resolved id = %q, want srv-1", id)
	}
}

package server

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"
)

const twoHypervisorsBody = `{
  "hypervisors": [
    {"id": "1", "hypervisor_hostname": "cmp-01", "hypervisor_type": "QEMU", "hypervisor_version": 2010000,
     "state": "up", "status": "enabled"},
    {"id": "2", "hypervisor_hostname": "cmp-02", "hypervisor_type": "QEMU", "hypervisor_version": 2010000,
     "state": "up", "status": "enabled"}
  ]
}`

// findHypervisor must query nova at the default microversion (2.1): 2.88 removed
// the usage fields the show view renders, so a negotiated "latest" would report
// them all as zero.
func TestFindHypervisor_PinsTheDefaultMicroversion(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMicroversion string
	var seen bool
	fakeServer.Mux.HandleFunc("/os-hypervisors/detail", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		seen = true
		gotMicroversion = r.Header.Get("OpenStack-API-Version")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(twoHypervisorsBody))
	})

	// The caller's client is on "latest"; the lookup must not inherit it.
	h, err := findHypervisor(context.Background(), computeClient(fakeServer, "latest"), "cmp-02")
	if err != nil {
		t.Fatalf("findHypervisor() error = %v", err)
	}
	if !seen {
		t.Fatal("findHypervisor() never listed hypervisors")
	}
	if gotMicroversion != "" {
		t.Errorf("microversion header = %q, want none (nova's default 2.1)", gotMicroversion)
	}
	if h.HypervisorHostname != "cmp-02" {
		t.Errorf("matched %q, want cmp-02", h.HypervisorHostname)
	}
}

func TestFindHypervisor_Matching(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		ref      string
		wantHost string
		wantErr  string
	}{
		{name: "matches on hostname", body: twoHypervisorsBody, ref: "cmp-01", wantHost: "cmp-01"},
		{name: "matches on id", body: twoHypervisorsBody, ref: "2", wantHost: "cmp-02"},
		{
			name: "no match names the ref", body: twoHypervisorsBody, ref: "cmp-99",
			wantErr: `hypervisor "cmp-99" not found`,
		},
		{
			// Hostnames are not unique across cells, and showing the wrong host's
			// usage is worse than refusing to guess.
			name: "an ambiguous ref is refused, not guessed",
			body: `{"hypervisors": [
			  {"id": "1", "hypervisor_hostname": "cmp-01", "hypervisor_version": 2010000},
			  {"id": "2", "hypervisor_hostname": "cmp-01", "hypervisor_version": 2010000}
			]}`,
			ref:     "cmp-01",
			wantErr: `hypervisor "cmp-01" is ambiguous (2 matches); specify the hypervisor ID instead`,
		},
		{
			name: "an empty fleet is a plain not-found", body: `{"hypervisors": []}`, ref: "cmp-01",
			wantErr: `hypervisor "cmp-01" not found`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()
			fakeServer.Mux.HandleFunc("/os-hypervisors/detail", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.body))
			})

			h, err := findHypervisor(context.Background(), computeClient(fakeServer, "latest"), tt.ref)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("findHypervisor() error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("findHypervisor() error = %v", err)
			}
			if h.HypervisorHostname != tt.wantHost {
				t.Errorf("matched %q, want %q", h.HypervisorHostname, tt.wantHost)
			}
		})
	}
}

// "hypervisor list" prints IDs at the negotiated microversion, UUIDs since 2.53;
// the 2.1 listing show reads names the same host by an integer. A UUID copied
// from the list must still resolve, through the host it names.
func TestFindHypervisor_UUIDFromANegotiatedListing(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	const uuid = "7d6d747d-40a3-4460-a949-abe57e48621f"
	var versions []string
	fakeServer.Mux.HandleFunc("/os-hypervisors/detail", func(w http.ResponseWriter, r *http.Request) {
		mv := r.Header.Get("OpenStack-API-Version")
		versions = append(versions, mv)
		w.Header().Set("Content-Type", "application/json")
		if mv == "" {
			_, _ = w.Write([]byte(`{"hypervisors": [
			  {"id": 1, "hypervisor_hostname": "cmp-01", "hypervisor_version": 2010000, "service": {"host": "cmp-01", "id": 5}, "vcpus": 8},
			  {"id": 2, "hypervisor_hostname": "cmp-02", "hypervisor_version": 2010000, "service": {"host": "cmp-02", "id": 6}, "vcpus": 16}
			]}`))
			return
		}
		_, _ = w.Write([]byte(`{"hypervisors": [
		  {"id": "0c9d7e1a-5b3f-4f60-8a2e-1d4c6b8a9f00", "hypervisor_hostname": "cmp-01", "hypervisor_version": 2010000, "service": {"host": "cmp-01", "id": "a"}},
		  {"id": "` + uuid + `", "hypervisor_hostname": "cmp-02", "hypervisor_version": 2010000, "service": {"host": "cmp-02", "id": "b"}}
		]}`))
	})

	h, err := findHypervisor(context.Background(), computeClient(fakeServer, "2.93"), uuid)
	if err != nil {
		t.Fatalf("findHypervisor(%s) error = %v", uuid, err)
	}
	// The 2.1 record, so the usage fields the show view renders are there.
	if h.HypervisorHostname != "cmp-02" || h.VCPUs != 16 {
		t.Errorf("matched %q with %d vCPUs, want cmp-02's 2.1 record (16)", h.HypervisorHostname, h.VCPUs)
	}
	if want := []string{"", "compute 2.93"}; !reflect.DeepEqual(versions, want) {
		t.Errorf("microversions requested = %q, want %q", versions, want)
	}

	// An ID in neither listing is still not found.
	if _, err := findHypervisor(context.Background(), computeClient(fakeServer, "2.93"),
		"00000000-0000-0000-0000-000000000000"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("unknown UUID: error = %v, want not found", err)
	}
}

// A failed hypervisor list is fatal — unlike the aggregate lookup below.
func TestFindHypervisor_ListFailure(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/os-hypervisors/detail", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	if _, err := findHypervisor(context.Background(), computeClient(fakeServer, "latest"), "cmp-01"); err == nil ||
		!strings.Contains(err.Error(), "listing hypervisors") {
		t.Fatalf("findHypervisor() error = %v, want the listing message", err)
	}
}

func TestHostAggregates(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/os-aggregates", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"aggregates": [
		  {"id": 1, "name": "az-a", "hosts": ["cmp-01", "cmp-02"]},
		  {"id": 2, "name": "gpu",  "hosts": ["cmp-02"]},
		  {"id": 3, "name": "empty", "hosts": []}
		]}`))
	})

	got := hostAggregates(context.Background(), computeClient(fakeServer, "latest"))
	want := map[string][]string{
		"cmp-01": {"az-a"},
		// Order follows nova's aggregate listing, which is what the rendered
		// "az-a,gpu" column depends on.
		"cmp-02": {"az-a", "gpu"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("hostAggregates() = %v, want %v", got, want)
	}
}

// The aggregate column is never the point of a hypervisor view, so a cloud that
// hides os-aggregates — or a non-admin token — must lose the column, not the
// command.
func TestHostAggregates_BestEffortOnFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{name: "a forbidden listing", status: http.StatusForbidden, body: `{}`},
		{name: "an absent extension", status: http.StatusNotFound, body: `{}`},
		{name: "an unparseable body", status: http.StatusOK, body: `{"aggregates": "nope"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()
			fakeServer.Mux.HandleFunc("/os-aggregates", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})

			got := hostAggregates(context.Background(), computeClient(fakeServer, "latest"))
			if len(got) != 0 {
				t.Errorf("hostAggregates() = %v, want an empty map", got)
			}
		})
	}
}

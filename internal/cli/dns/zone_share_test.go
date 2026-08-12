package dns

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	th "github.com/gophercloud/gophercloud/v2/testhelper"
	fakeclient "github.com/gophercloud/gophercloud/v2/testhelper/client"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// dnsShareClient builds a fake designate client. Designate carries no
// microversion header, so Microversion stays empty.
func dnsShareClient(fakeServer th.FakeServer) *gophercloud.ServiceClient {
	sc := fakeclient.ServiceClient(fakeServer)
	sc.Type = "dns"
	return sc
}

// stubZoneList answers resolveZoneID, which lists zones and matches by name or ID.
func stubZoneList(fakeServer th.FakeServer) {
	fakeServer.Mux.HandleFunc("/zones", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"zones": [
          {"id": "z1", "name": "example.com.", "type": "PRIMARY", "status": "ACTIVE"}
        ]}`))
	})
}

func TestRunZoneShareCreate_RequestBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubZoneList(fakeServer)
	var gotMethod string
	fakeServer.Mux.HandleFunc("/zones/z1/shares", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		th.TestJSONRequest(t, r, `{"target_project_id": "p2"}`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{
          "id": "sh1", "zone_id": "z1", "project_id": "p1", "target_project_id": "p2",
          "created_at": "2026-08-06T12:00:00.000000"
        }`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	// The zone is given by name, which resolveZoneID turns into z1.
	if err := runZoneShareCreate(context.Background(), dnsShareClient(fakeServer), o, "example.com", "p2", &buf); err != nil {
		t.Fatalf("runZoneShareCreate error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	for _, want := range []string{"sh1", "z1", "p2"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q\n---\n%s", want, buf.String())
		}
	}
}

// Designate's cross-project reads use the X-Auth-All-Projects header, not a query
// parameter — that is the thing worth pinning down.
func TestRunZoneShareList_AllProjectsUsesHeader(t *testing.T) {
	tests := []struct {
		name        string
		allProjects bool
		wantHeader  string
	}{
		{name: "default", allProjects: false, wantHeader: ""},
		{name: "--all-projects", allProjects: true, wantHeader: "true"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()

			stubZoneList(fakeServer)
			var gotHeader string
			var sawQuery bool
			fakeServer.Mux.HandleFunc("/zones/z1/shares", func(w http.ResponseWriter, r *http.Request) {
				th.TestMethod(t, r, http.MethodGet)
				gotHeader = r.Header.Get("X-Auth-All-Projects")
				sawQuery = r.URL.Query().Has("all_projects")
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"shared_zones": [
                  {"id": "sh1", "zone_id": "z1", "project_id": "p1", "target_project_id": "p2",
                   "created_at": "2026-08-06T12:00:00.000000"}
                ]}`))
			})

			o := &output.Options{Format: output.FormatTable}
			var buf bytes.Buffer
			err := runZoneShareList(context.Background(), dnsShareClient(fakeServer), o, "z1", tc.allProjects, &buf)
			if err != nil {
				t.Fatalf("runZoneShareList error: %v", err)
			}
			if gotHeader != tc.wantHeader {
				t.Errorf("X-Auth-All-Projects = %q, want %q", gotHeader, tc.wantHeader)
			}
			if sawQuery {
				t.Error("all_projects must be a header, not a query parameter")
			}
			for _, want := range []string{"sh1", "p2"} {
				if !strings.Contains(buf.String(), want) {
					t.Errorf("output missing %q\n---\n%s", want, buf.String())
				}
			}
		})
	}
}

func TestRunZoneShareShow_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubZoneList(fakeServer)
	var gotPath string
	fakeServer.Mux.HandleFunc("/zones/z1/shares/sh1", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		th.TestMethod(t, r, http.MethodGet)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
          "id": "sh1", "zone_id": "z1", "project_id": "p1", "target_project_id": "p2",
          "created_at": "2026-08-06T12:00:00.000000"
        }`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runZoneShareShow(context.Background(), dnsShareClient(fakeServer), o, "z1", "sh1", &buf); err != nil {
		t.Fatalf("runZoneShareShow error: %v", err)
	}
	if gotPath != "/zones/z1/shares/sh1" {
		t.Errorf("path = %q, want /zones/z1/shares/sh1", gotPath)
	}
	if !strings.Contains(buf.String(), "target_project_id") {
		t.Errorf("output missing the target project field\n---\n%s", buf.String())
	}
}

func TestRunZoneShareDelete_Request(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubZoneList(fakeServer)
	var deleted []string
	for _, id := range []string{"sh1", "sh2"} {
		fakeServer.Mux.HandleFunc("/zones/z1/shares/"+id, func(w http.ResponseWriter, r *http.Request) {
			th.TestMethod(t, r, http.MethodDelete)
			deleted = append(deleted, id)
			w.WriteHeader(http.StatusNoContent)
		})
	}

	var buf bytes.Buffer
	err := runZoneShareDelete(context.Background(), dnsShareClient(fakeServer), "z1", []string{"sh1", "sh2"}, &buf)
	if err != nil {
		t.Fatalf("runZoneShareDelete error: %v", err)
	}
	if len(deleted) != 2 {
		t.Errorf("deleted %v, want both shares removed", deleted)
	}
	for _, want := range []string{"Removed share sh1", "Removed share sh2"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q\n---\n%s", want, buf.String())
		}
	}
}

// One timestamp convention across the dns package: designate's own spelling,
// and nil — never Go's zero time, never a bare "" from one code path and
// "0001-01-01 00:00:00 +0000 UTC" from another — when the value is null.
func TestDNSTimestampConvention(t *testing.T) {
	stamped := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	if got := dnsTime(stamped); got != "2026-08-06T12:00:00.000000" {
		t.Errorf("dnsTime = %v, want designate's spelling", got)
	}
	if got := dnsTime(time.Time{}); got != nil {
		t.Errorf("a null timestamp must render nil, got %v", got)
	}
	if got := dnsTimeString("2026-08-06T12:00:00.000000"); got != "2026-08-06T12:00:00.000000" {
		t.Errorf("dnsTimeString = %v, want passthrough", got)
	}
	if got := dnsTimeString(""); got != nil {
		t.Errorf("an empty raw timestamp must render nil, got %v", got)
	}
}

// The zero time must never reach the rendered output of a zone, which is where
// an unshared zone's transferred_at showed up as "0001-01-01 00:00:00 +0000 UTC".
func TestRunZoneShow_NullTransferredAtRendersEmpty(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubZoneList(fakeServer)
	fakeServer.Mux.HandleFunc("/zones/z1", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"z1","name":"example.com.","type":"PRIMARY","status":"ACTIVE",
          "created_at":"2026-08-06T12:00:00.000000","updated_at":null,"transferred_at":null}`))
	})

	o := &output.Options{Format: output.FormatJSON}
	var buf bytes.Buffer
	if err := runZoneShow(context.Background(), dnsShareClient(fakeServer), o, "z1", &buf); err != nil {
		t.Fatalf("runZoneShow error: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "0001-01-01") {
		t.Errorf("the Go zero time leaked into the output\n---\n%s", out)
	}
	for _, want := range []string{`"transferred_at": null`, `"updated_at": null`, `"created_at": "2026-08-06T12:00:00.000000"`} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s", want, out)
		}
	}
}

package dns

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

func TestRunDNSLimitList(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/limits", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
          "max_zones": 10, "max_zone_records": 500, "min_ttl": null
        }`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runDNSLimitList(context.Background(), dnsShareClient(fakeServer), o,
		&commonOptions{}, &buf); err != nil {
		t.Fatalf("runDNSLimitList error: %v", err)
	}
	// Keys are sorted so the output is stable, and an unknown future limit still
	// shows up rather than being dropped by a fixed struct.
	out := buf.String()
	for _, want := range []string{"max_zone_records", "max_zones", "min_ttl", "500", "10"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s", want, out)
		}
	}
	if strings.Index(out, "max_zone_records") > strings.Index(out, "max_zones") {
		t.Errorf("rows are not sorted by name\n---\n%s", out)
	}
}

func TestRunZoneNameserversList(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubZoneList(fakeServer)
	fakeServer.Mux.HandleFunc("/zones/z1/nameservers", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"nameservers": [
          {"hostname": "ns1.example.com.", "priority": 1},
          {"hostname": "ns2.example.com.", "priority": 2}
        ]}`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	// The zone is given by name, so resolveZoneID turns it into z1 first.
	if err := runZoneNameserversList(context.Background(), dnsShareClient(fakeServer), o,
		"example.com", &commonOptions{}, &buf); err != nil {
		t.Fatalf("runZoneNameserversList error: %v", err)
	}
	for _, want := range []string{"ns1.example.com.", "ns2.example.com."} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q\n---\n%s", want, buf.String())
		}
	}
}

// The three zone tasks share one seam; what differs is the endpoint segment, the
// body and the confirmation. 204 with an empty body is the common answer, so the
// call must not try to decode one.
func TestRunZoneTask(t *testing.T) {
	for _, tc := range []struct {
		name     string
		task     string
		body     map[string]any
		wantBody string
		message  string
		status   int
	}{
		{"abandon", "abandon", nil, "", "Abandoned zone", http.StatusNoContent},
		{"axfr", "xfr", nil, "", "Scheduled AXFR for zone", http.StatusAccepted},
		{"move", "pool_move", map[string]any{"pool_id": "p2"}, `{"pool_id": "p2"}`,
			"Scheduled move for zone", http.StatusAccepted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()

			stubZoneList(fakeServer)
			var gotMethod string
			fakeServer.Mux.HandleFunc("/zones/z1/tasks/"+tc.task, func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				if tc.wantBody != "" {
					th.TestJSONRequest(t, r, tc.wantBody)
				}
				w.WriteHeader(tc.status)
			})

			var buf bytes.Buffer
			err := runZoneTask(context.Background(), dnsShareClient(fakeServer), "example.com",
				zoneTask{task: tc.task, message: tc.message, body: tc.body, common: &commonOptions{}}, &buf)
			if err != nil {
				t.Fatalf("runZoneTask error: %v", err)
			}
			if gotMethod != http.MethodPost {
				t.Errorf("method = %q, want POST", gotMethod)
			}
			if want := tc.message + " example.com"; !strings.Contains(buf.String(), want) {
				t.Errorf("output = %q, want %q", buf.String(), want)
			}
		})
	}
}

// "zone move" with no --pool-id sends no body at all, letting designate's scheduler
// pick the target.
func TestZoneMove_NoPoolIDSendsNoBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubZoneList(fakeServer)
	var gotLength int64
	fakeServer.Mux.HandleFunc("/zones/z1/tasks/pool_move", func(w http.ResponseWriter, r *http.Request) {
		gotLength = r.ContentLength
		w.WriteHeader(http.StatusAccepted)
	})

	var buf bytes.Buffer
	if err := runZoneTask(context.Background(), dnsShareClient(fakeServer), "example.com",
		zoneTask{task: "pool_move", message: "Scheduled move for zone", common: &commonOptions{}},
		&buf); err != nil {
		t.Fatalf("runZoneTask error: %v", err)
	}
	if gotLength > 0 {
		t.Errorf("request body length = %d, want no body", gotLength)
	}
}

// The zone-task seams resolve the zone through a client carrying the admin headers,
// so --all-projects reaches the resolving list call too, not just the task.
func TestRunZoneTask_ResolvesWithCommonHeaders(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var listHeader, taskHeader string
	fakeServer.Mux.HandleFunc("/zones", func(w http.ResponseWriter, r *http.Request) {
		listHeader = r.Header.Get("X-Auth-All-Projects")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"zones": [{"id": "z1", "name": "example.com."}]}`))
	})
	fakeServer.Mux.HandleFunc("/zones/z1/tasks/abandon", func(w http.ResponseWriter, r *http.Request) {
		taskHeader = r.Header.Get("X-Auth-All-Projects")
		w.WriteHeader(http.StatusNoContent)
	})

	var buf bytes.Buffer
	common := &commonOptions{allProjects: true}
	if err := runZoneTask(context.Background(), dnsShareClient(fakeServer), "example.com",
		zoneTask{task: "abandon", message: "Abandoned zone", common: common}, &buf); err != nil {
		t.Fatalf("runZoneTask error: %v", err)
	}
	if listHeader != "true" {
		t.Errorf("zone list X-Auth-All-Projects = %q, want true", listHeader)
	}
	if taskHeader != "true" {
		t.Errorf("task X-Auth-All-Projects = %q, want true", taskHeader)
	}
}

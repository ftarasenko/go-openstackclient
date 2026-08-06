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

// Listing another project's or user's servers is a cross-project read, which nova
// honors only together with all_tenants — so either filter must imply it rather
// than quietly returning nothing.
func TestRunServerList_ProjectAndUserImplyAllTenants(t *testing.T) {
	tests := []struct {
		name        string
		flags       serverListFlags
		projectID   string
		userID      string
		wantQuery   map[string]string
		absentQuery []string
	}{
		{
			name:        "no scope flags",
			absentQuery: []string{"all_tenants", "tenant_id", "user_id"},
		},
		{
			name:      "project implies all_tenants",
			projectID: "p1",
			wantQuery: map[string]string{"all_tenants": "true", "tenant_id": "p1"},
		},
		{
			name:      "user implies all_tenants",
			userID:    "u1",
			wantQuery: map[string]string{"all_tenants": "true", "user_id": "u1"},
		},
		{
			name:      "both together",
			projectID: "p1",
			userID:    "u1",
			wantQuery: map[string]string{"all_tenants": "true", "tenant_id": "p1", "user_id": "u1"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()

			fakeServer.Mux.HandleFunc("/servers/detail", func(w http.ResponseWriter, r *http.Request) {
				th.TestMethod(t, r, http.MethodGet)
				got := r.URL.Query()
				for key, want := range tc.wantQuery {
					if got.Get(key) != want {
						t.Errorf("query %s = %q, want %q (full %q)", key, got.Get(key), want, r.URL.RawQuery)
					}
				}
				for _, key := range tc.absentQuery {
					if got.Has(key) {
						t.Errorf("query should not carry %s, got %q", key, r.URL.RawQuery)
					}
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"servers": [{"id": "s1", "name": "web-1", "status": "ACTIVE"}]}`))
			})

			o := &output.Options{Format: output.FormatTable}
			var buf bytes.Buffer
			flags := tc.flags
			err := runServerList(context.Background(), computeClient(fakeServer, "latest"), o, &flags,
				tc.projectID, tc.userID, &buf)
			if err != nil {
				t.Fatalf("runServerList error: %v", err)
			}
			if !strings.Contains(buf.String(), "web-1") {
				t.Errorf("output missing the server\n---\n%s", buf.String())
			}
		})
	}
}

// Nova's os-services list has no status query parameter, so --status filters
// after extraction — and must keep the typed and raw-extension slices aligned.
func TestRunComputeServiceList_StatusFiltersAfterExtraction(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/os-services", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		if r.URL.Query().Has("status") {
			t.Errorf("nova has no status filter; it must not be sent, got %q", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"services": [
          {"id": "1", "binary": "nova-compute", "host": "cmp1", "status": "enabled",
           "state": "up", "zone": "nova", "admin_state": "unlocked"},
          {"id": "2", "binary": "nova-compute", "host": "cmp2", "status": "disabled",
           "state": "down", "zone": "nova", "admin_state": "locked", "error_details": "boom"},
          {"id": "3", "binary": "nova-conductor", "host": "ctl1", "status": "enabled",
           "state": "up", "zone": "internal", "admin_state": "unlocked"}
        ]}`))
	})

	o := &output.Options{Format: output.FormatTable}
	client := computeClient(fakeServer, "latest")

	var disabled bytes.Buffer
	err := runComputeServiceList(context.Background(), client, o, &serviceListFlags{status: "disabled", long: true}, &disabled)
	if err != nil {
		t.Fatalf("runComputeServiceList error: %v", err)
	}
	out := disabled.String()
	if !strings.Contains(out, "cmp2") {
		t.Errorf("--status disabled output missing the disabled service\n---\n%s", out)
	}
	for _, absent := range []string{"cmp1", "ctl1"} {
		if strings.Contains(out, absent) {
			t.Errorf("--status disabled output should not contain %q\n---\n%s", absent, out)
		}
	}
	// The KeyStack extension columns are aligned by index with the typed list, so
	// the surviving row must still carry its own error_details, not cmp1's.
	if !strings.Contains(out, "boom") {
		t.Errorf("--status filtering lost the aligned extension fields\n---\n%s", out)
	}

	var enabled bytes.Buffer
	err = runComputeServiceList(context.Background(), client, o, &serviceListFlags{status: "enabled"}, &enabled)
	if err != nil {
		t.Fatalf("runComputeServiceList error: %v", err)
	}
	for _, want := range []string{"cmp1", "ctl1"} {
		if !strings.Contains(enabled.String(), want) {
			t.Errorf("--status enabled output missing %q\n---\n%s", want, enabled.String())
		}
	}
	if strings.Contains(enabled.String(), "cmp2") {
		t.Errorf("--status enabled output should not contain cmp2\n---\n%s", enabled.String())
	}
}

func TestRunComputeServiceList_RejectsUnknownStatus(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runComputeServiceList(context.Background(), computeClient(fakeServer, "latest"), o,
		&serviceListFlags{status: "up"}, &buf)
	if err == nil || !strings.Contains(err.Error(), "enabled or disabled") {
		t.Fatalf("expected an invalid-status error, got %v", err)
	}
}

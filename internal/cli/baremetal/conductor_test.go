package baremetal

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

const conductorListBody = `{
  "conductors": [
    {
      "hostname": "conductor-a",
      "conductor_group": "",
      "alive": true,
      "drivers": ["ipmi", "redfish"]
    },
    {
      "hostname": "conductor-b",
      "conductor_group": "group1",
      "alive": false,
      "drivers": ["ipmi"]
    }
  ]
}`

func TestRunConductorList_RequestAndTableOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotIronicVersion string
	fakeServer.Mux.HandleFunc("/conductors", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotIronicVersion = r.Header.Get("X-OpenStack-Ironic-API-Version")
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(conductorListBody))
	})

	client := baremetalClient(fakeServer, "1.80")
	o := &output.Options{Format: output.FormatTable}

	var buf bytes.Buffer
	if err := runConductorList(context.Background(), client, o, &conductorListFlags{}, &buf); err != nil {
		t.Fatalf("runConductorList returned error: %v", err)
	}

	if gotMethod != http.MethodGet {
		t.Errorf("request method = %q, want GET", gotMethod)
	}
	if gotIronicVersion != "1.80" {
		t.Errorf("X-OpenStack-Ironic-API-Version = %q, want 1.80", gotIronicVersion)
	}

	out := buf.String()
	for _, want := range []string{
		"Hostname", "Conductor Group", "Alive",
		"conductor-a", "conductor-b", "group1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("conductor table output missing %q\n---\n%s", want, out)
		}
	}
	// --long columns should not appear by default.
	if strings.Contains(out, "Drivers") || strings.Contains(out, "Updated At") {
		t.Errorf("default output should not contain --long columns:\n%s", out)
	}
}

func TestRunConductorList_LongAndFilters(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/conductors", func(w http.ResponseWriter, r *http.Request) {
		th.TestFormValues(t, r, map[string]string{
			"detail":   "true",
			"limit":    "10",
			"marker":   "conductor-a",
			"sort_key": "hostname",
			"sort_dir": "asc",
		})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(conductorListBody))
	})

	client := baremetalClient(fakeServer, "latest")
	o := &output.Options{Format: output.FormatTable}
	f := &conductorListFlags{long: true, limit: 10, marker: "conductor-a", sortKey: "hostname", sortDir: "asc"}

	var buf bytes.Buffer
	if err := runConductorList(context.Background(), client, o, f, &buf); err != nil {
		t.Fatalf("runConductorList returned error: %v", err)
	}

	out := buf.String()
	// --long adds the Drivers + Updated At columns.
	for _, want := range []string{"Drivers", "Updated At", "ipmi"} {
		if !strings.Contains(out, want) {
			t.Errorf("--long conductor output missing %q\n---\n%s", want, out)
		}
	}
}

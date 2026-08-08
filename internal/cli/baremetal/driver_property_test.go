package baremetal

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

func TestRunDriverPropertyList_SortedByName(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	fakeServer.Mux.HandleFunc("/drivers/redfish/properties", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		w.Header().Set("Content-Type", "application/json")
		// Deliberately not in alphabetical order: ironic returns a JSON object,
		// whose member order carries no meaning.
		_, _ = w.Write([]byte(`{
		  "redfish_username": "User account with admin privileges. Required.",
		  "redfish_address":  "The URL address to the Redfish controller. Required."
		}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	client := baremetalClient(fakeServer, "latest")
	if err := runDriverPropertyList(context.Background(), client, o, "redfish", &out); err != nil {
		t.Fatalf("runDriverPropertyList returned error: %v", err)
	}

	th.AssertEquals(t, "GET", gotMethod)
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	th.AssertEquals(t, 2, len(lines))
	if !strings.HasPrefix(lines[0], "redfish_address\t") {
		t.Errorf("rows are not sorted by property name: %q", lines[0])
	}
}

func TestRunDriverRAIDPropertyList_UsesRAIDEndpoint(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotPath string
	fakeServer.Mux.HandleFunc("/drivers/redfish/raid/logical_disk_properties", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"raid_level": "RAID level for the logical disk. Required."}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	client := baremetalClient(fakeServer, "latest")
	if err := runDriverRAIDPropertyList(context.Background(), client, o, "redfish", &out); err != nil {
		t.Fatalf("runDriverRAIDPropertyList returned error: %v", err)
	}
	th.AssertEquals(t, "/drivers/redfish/raid/logical_disk_properties", gotPath)
	if !strings.HasPrefix(out.String(), "raid_level\t") {
		t.Errorf("unexpected output: %q", out.String())
	}
}

func TestRunConductorShow_RequestAndFields(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotMicroversion string
	fakeServer.Mux.HandleFunc("/conductors/conductor-1.example.com", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotMicroversion = r.Header.Get("X-OpenStack-Ironic-API-Version")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
		  "hostname": "conductor-1.example.com",
		  "conductor_group": "rack-a",
		  "alive": true,
		  "drivers": ["redfish", "ipmi"],
		  "created_at": "2026-01-02T03:04:05+00:00",
		  "updated_at": "2026-01-02T03:14:05+00:00"
		}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	client := baremetalClient(fakeServer, "latest")
	if err := runConductorShow(context.Background(), client, o, "conductor-1.example.com", &out); err != nil {
		t.Fatalf("runConductorShow returned error: %v", err)
	}

	th.AssertEquals(t, "GET", gotMethod)
	th.AssertEquals(t, "latest", gotMicroversion)
	for _, want := range []string{"conductor-1.example.com", "rack-a", "true", "redfish"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
}

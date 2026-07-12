package baremetal

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	th "github.com/gophercloud/gophercloud/v2/testhelper"
	fakeclient "github.com/gophercloud/gophercloud/v2/testhelper/client"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

const nodeListBody = `{
  "nodes": [
    {
      "uuid": "11111111-1111-1111-1111-111111111111",
      "name": "node-a",
      "instance_uuid": "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
      "power_state": "power on",
      "provision_state": "active",
      "maintenance": false,
      "driver": "ipmi"
    },
    {
      "uuid": "22222222-2222-2222-2222-222222222222",
      "name": "node-b",
      "instance_uuid": null,
      "power_state": "power off",
      "provision_state": "available",
      "maintenance": true,
      "driver": "redfish"
    }
  ]
}`

// baremetalClient returns a service client wired to the mock server with the
// ironic service type + microversion, mirroring how auth.Client.Baremetal does.
func baremetalClient(fakeServer th.FakeServer, microversion string) *gophercloud.ServiceClient {
	sc := fakeclient.ServiceClient(fakeServer)
	sc.Type = "baremetal"
	sc.Microversion = microversion
	return sc
}

func TestRunNodeList_RequestAndTableOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotIronicVersion, gotAPIVersion string
	fakeServer.Mux.HandleFunc("/nodes", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotIronicVersion = r.Header.Get("X-OpenStack-Ironic-API-Version")
		gotAPIVersion = r.Header.Get("OpenStack-API-Version")
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(nodeListBody))
	})

	client := baremetalClient(fakeServer, "1.80")
	o := &output.Options{Format: output.FormatTable}

	var buf bytes.Buffer
	if err := runNodeList(context.Background(), client, o, &nodeListFlags{}, &buf); err != nil {
		t.Fatalf("runNodeList returned error: %v", err)
	}

	// Request assertions: method + microversion headers.
	if gotMethod != http.MethodGet {
		t.Errorf("request method = %q, want GET", gotMethod)
	}
	if gotIronicVersion != "1.80" {
		t.Errorf("X-OpenStack-Ironic-API-Version = %q, want 1.80", gotIronicVersion)
	}
	if gotAPIVersion != "baremetal 1.80" {
		t.Errorf("OpenStack-API-Version = %q, want %q", gotAPIVersion, "baremetal 1.80")
	}

	// Output assertions: both nodes present, default columns rendered.
	out := buf.String()
	for _, want := range []string{
		"UUID", "Name", "Instance UUID", "Power State", "Provisioning State", "Maintenance",
		"node-a", "node-b",
		"11111111-1111-1111-1111-111111111111",
		"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		"power on", "power off", "active", "available",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("table output missing %q\n---\n%s", want, out)
		}
	}
	// --long columns should NOT appear by default.
	if strings.Contains(out, "Resource Class") {
		t.Errorf("default output should not contain --long columns:\n%s", out)
	}
}

func TestRunNodeList_ValueFormatIsTabSeparatedNoHeader(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/nodes", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(nodeListBody))
	})

	client := baremetalClient(fakeServer, "latest")
	o := &output.Options{Format: output.FormatValue}

	var buf bytes.Buffer
	if err := runNodeList(context.Background(), client, o, &nodeListFlags{}, &buf); err != nil {
		t.Fatalf("runNodeList returned error: %v", err)
	}

	out := buf.String()
	if strings.Contains(out, "UUID") {
		t.Errorf("value format must not include headers:\n%s", out)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("value format: got %d rows, want 2:\n%s", len(lines), out)
	}
	first := strings.Split(lines[0], "\t")
	if len(first) != 6 {
		t.Fatalf("value row should have 6 tab-separated fields, got %d: %q", len(first), lines[0])
	}
	if first[0] != "11111111-1111-1111-1111-111111111111" || first[1] != "node-a" {
		t.Errorf("unexpected value row fields: %#v", first)
	}
}

func TestRunNodeList_ColumnSelection(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/nodes", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(nodeListBody))
	})

	client := baremetalClient(fakeServer, "latest")
	o := &output.Options{Format: output.FormatCSV, Columns: []string{"Name", "Provisioning State"}}

	var buf bytes.Buffer
	if err := runNodeList(context.Background(), client, o, &nodeListFlags{}, &buf); err != nil {
		t.Fatalf("runNodeList returned error: %v", err)
	}

	out := strings.TrimSpace(buf.String())
	lines := strings.Split(out, "\n")
	if lines[0] != "Name,Provisioning State" {
		t.Errorf("CSV header = %q, want %q", lines[0], "Name,Provisioning State")
	}
	if strings.Contains(out, "UUID") {
		t.Errorf("column selection should exclude UUID:\n%s", out)
	}
	if lines[1] != "node-a,active" {
		t.Errorf("CSV first data row = %q, want %q", lines[1], "node-a,active")
	}
}

func TestRunNodeList_LimitCapsResults(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	// Server returns 2 nodes even though the client asked for a page size of 1
	// (mirrors ironic returning a full page); --limit must cap the rendered rows.
	fakeServer.Mux.HandleFunc("/nodes", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(nodeListBody))
	})

	client := baremetalClient(fakeServer, "latest")
	o := &output.Options{Format: output.FormatValue}
	f := &nodeListFlags{limit: 1}

	var buf bytes.Buffer
	if err := runNodeList(context.Background(), client, o, f, &buf); err != nil {
		t.Fatalf("runNodeList returned error: %v", err)
	}

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("--limit 1 should render exactly 1 row, got %d:\n%s", len(lines), buf.String())
	}
	if !strings.Contains(lines[0], "node-a") {
		t.Errorf("first row should be node-a, got %q", lines[0])
	}
}

func TestNodeListFilters_MaintenanceAndAssociatedFalseAreSent(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotRawQuery string
	fakeServer.Mux.HandleFunc("/nodes", func(w http.ResponseWriter, r *http.Request) {
		gotRawQuery = r.URL.RawQuery
		th.TestFormValues(t, r, map[string]string{
			"maintenance": "false",
			"associated":  "false",
		})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"nodes": []}`))
	})

	client := baremetalClient(fakeServer, "latest")
	o := &output.Options{Format: output.FormatValue}
	// Both flags explicitly set to their false (inverting) value.
	f := &nodeListFlags{maintenanceSet: true, maintenance: false, associatedSet: true, associated: false}

	var buf bytes.Buffer
	if err := runNodeList(context.Background(), client, o, f, &buf); err != nil {
		t.Fatalf("runNodeList returned error: %v", err)
	}
	if !strings.Contains(gotRawQuery, "maintenance=false") {
		t.Errorf("request query %q must contain maintenance=false", gotRawQuery)
	}
	if !strings.Contains(gotRawQuery, "associated=false") {
		t.Errorf("request query %q must contain associated=false", gotRawQuery)
	}
}

func TestNodeListFilters_MaintenanceTrueStillSent(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/nodes", func(w http.ResponseWriter, r *http.Request) {
		th.TestFormValues(t, r, map[string]string{"maintenance": "true"})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"nodes": []}`))
	})

	client := baremetalClient(fakeServer, "latest")
	o := &output.Options{Format: output.FormatValue}
	f := &nodeListFlags{maintenanceSet: true, maintenance: true}

	var buf bytes.Buffer
	if err := runNodeList(context.Background(), client, o, f, &buf); err != nil {
		t.Fatalf("runNodeList returned error: %v", err)
	}
}

func TestEscapeJSONPointer(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"plain", "plain"},
		{"a/b", "a~1b"},
		{"a~b", "a~0b"},
		{"a~/b", "a~0~1b"},
		{"~/", "~0~1"},
	} {
		if got := escapeJSONPointer(tc.in); got != tc.want {
			t.Errorf("escapeJSONPointer(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRunNodeDelete_ContinuesAfterFailure(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	// good-1 deletes fine; bad fails with 404; good-2 must still be attempted.
	fakeServer.Mux.HandleFunc("/nodes/good-1", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	fakeServer.Mux.HandleFunc("/nodes/bad", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	var good2Called bool
	fakeServer.Mux.HandleFunc("/nodes/good-2", func(w http.ResponseWriter, _ *http.Request) {
		good2Called = true
		w.WriteHeader(http.StatusNoContent)
	})

	client := baremetalClient(fakeServer, "latest")
	var buf bytes.Buffer
	err := runNodeDelete(context.Background(), client, []string{"good-1", "bad", "good-2"}, &buf)
	if err == nil {
		t.Fatal("expected error from failed delete, got nil")
	}
	if !strings.Contains(err.Error(), "bad") {
		t.Errorf("error should name the failed id, got: %v", err)
	}
	if !good2Called {
		t.Error("delete should continue to good-2 after bad failed")
	}
	out := buf.String()
	if !strings.Contains(out, "Deleted node good-1") || !strings.Contains(out, "Deleted node good-2") {
		t.Errorf("both successful deletes should be reported, got:\n%s", out)
	}
}

func TestNodeListFilters_MarkerAndProvisionState(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/nodes", func(w http.ResponseWriter, r *http.Request) {
		th.TestFormValues(t, r, map[string]string{
			"limit":           "50",
			"marker":          "33333333-3333-3333-3333-333333333333",
			"provision_state": "available",
		})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"nodes": []}`))
	})

	client := baremetalClient(fakeServer, "latest")
	o := &output.Options{Format: output.FormatValue}
	f := &nodeListFlags{limit: 50, marker: "33333333-3333-3333-3333-333333333333", provisionState: "available"}

	var buf bytes.Buffer
	if err := runNodeList(context.Background(), client, o, f, &buf); err != nil {
		t.Fatalf("runNodeList returned error: %v", err)
	}
}

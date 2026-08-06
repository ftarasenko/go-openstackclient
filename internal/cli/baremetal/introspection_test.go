package baremetal

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	th "github.com/gophercloud/gophercloud/v2/testhelper"
	fakeclient "github.com/gophercloud/gophercloud/v2/testhelper/client"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// introspectionClient builds a fake ironic-inspector client. The inspector has no
// microversion header, so Type is set but Microversion is deliberately empty.
func introspectionClient(fakeServer th.FakeServer) *gophercloud.ServiceClient {
	sc := fakeclient.ServiceClient(fakeServer)
	sc.Type = "baremetal-introspection"
	return sc
}

func TestRunIntrospectionStart_SendsManageBootQuery(t *testing.T) {
	tests := []struct {
		name      string
		flags     introspectionStartFlags
		wantQuery string
	}{
		{"default", introspectionStartFlags{}, ""},
		{"manage-boot", introspectionStartFlags{manageBoot: true}, "true"},
		{"no-manage-boot", introspectionStartFlags{noManageBoot: true}, "false"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()

			var gotMethod, gotManageBoot string
			fakeServer.Mux.HandleFunc("/introspection/node-1", func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				gotManageBoot = r.URL.Query().Get("manage_boot")
				th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
				w.WriteHeader(http.StatusAccepted)
			})

			var buf bytes.Buffer
			err := runIntrospectionStart(context.Background(), introspectionClient(fakeServer), "node-1", &tc.flags, &buf)
			if err != nil {
				t.Fatalf("runIntrospectionStart returned error: %v", err)
			}
			if gotMethod != http.MethodPost {
				t.Errorf("request method = %q, want POST", gotMethod)
			}
			if gotManageBoot != tc.wantQuery {
				t.Errorf("manage_boot query = %q, want %q", gotManageBoot, tc.wantQuery)
			}
			if out := buf.String(); !strings.Contains(out, "Started introspection of node node-1") {
				t.Errorf("unexpected output %q", out)
			}
		})
	}
}

func TestRunIntrospectionStart_RejectsConflictingFlags(t *testing.T) {
	cmd := newIntrospectionStartCommand(nil, &output.Options{Format: output.FormatTable})
	cmd.SetArgs([]string{"node-1", "--manage-boot", "--no-manage-boot"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected a mutual-exclusion error, got %v", err)
	}
}

func TestRunIntrospectionStatus_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/introspection/node-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
          "uuid": "node-1", "state": "finished", "finished": true, "error": null,
          "started_at": "2026-08-01T10:00:00Z", "finished_at": "2026-08-01T10:06:00Z"
        }`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runIntrospectionStatus(context.Background(), introspectionClient(fakeServer), o, "node-1", &buf); err != nil {
		t.Fatalf("runIntrospectionStatus returned error: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"uuid", "node-1", "state", "finished", "true", "2026-08-01"} {
		if !strings.Contains(out, want) {
			t.Errorf("status output missing %q\n---\n%s", want, out)
		}
	}
}

func TestRunIntrospectionList_LimitIsAHardCap(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/introspection", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		th.TestFormValues(t, r, map[string]string{"limit": "1"})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// The inspector treats limit as a page size, so it may return more than
		// asked for across pages; --limit must still cap the rendered rows.
		_, _ = w.Write([]byte(`{"introspection": [
          {"uuid": "node-1", "state": "finished", "finished": true, "started_at": "2026-08-01T10:00:00Z"},
          {"uuid": "node-2", "state": "error", "finished": true, "error": "boom", "started_at": "2026-08-01T11:00:00Z"}
        ]}`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	f := &introspectionListFlags{limit: 1}
	if err := runIntrospectionList(context.Background(), introspectionClient(fakeServer), o, f, &buf); err != nil {
		t.Fatalf("runIntrospectionList returned error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "node-1") {
		t.Errorf("list output missing node-1:\n%s", out)
	}
	if strings.Contains(out, "node-2") {
		t.Errorf("--limit 1 should have capped the list at one row:\n%s", out)
	}
}

func TestRunIntrospectionAbort_Request(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	fakeServer.Mux.HandleFunc("/introspection/node-1/abort", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		w.WriteHeader(http.StatusAccepted)
	})

	var buf bytes.Buffer
	if err := runIntrospectionAbort(context.Background(), introspectionClient(fakeServer), "node-1", &buf); err != nil {
		t.Fatalf("runIntrospectionAbort returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("request method = %q, want POST", gotMethod)
	}
	if out := buf.String(); !strings.Contains(out, "Aborted introspection of node node-1") {
		t.Errorf("unexpected output %q", out)
	}
}

const introspectionDataBody = `{
  "cpu_arch": "x86_64",
  "cpus": 64,
  "memory_mb": 262144,
  "macs": ["52:54:00:aa:bb:cc"],
  "interfaces": {
    "eno1": {"mac": "52:54:00:aa:bb:cc", "ip": "10.0.0.21", "pxe": true}
  },
  "all_interfaces": {
    "eno1": {
      "mac": "52:54:00:aa:bb:cc", "ip": "10.0.0.21", "pxe": true,
      "lldp_processed": {
        "switch_chassis_id": "00:1b:0d:aa:bb:cc",
        "switch_port_id": "Ethernet1/7",
        "switch_port_vlans": [{"id": 100, "name": "provisioning"}],
        "switch_port_mau_type": "10GigBaseSR"
      }
    },
    "eno2": {"mac": "52:54:00:aa:bb:cd", "pxe": false}
  },
  "vendor_specific_extra": {"kept": true}
}`

func TestRunIntrospectionInterfaceList_ProjectsLLDPFromData(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/introspection/node-1/data", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(introspectionDataBody))
	})

	o := &output.Options{Format: output.FormatTable}
	client := introspectionClient(fakeServer)

	var buf bytes.Buffer
	if err := runIntrospectionInterfaceList(context.Background(), client, o, "node-1", &introspectionInterfaceListFlags{}, &buf); err != nil {
		t.Fatalf("runIntrospectionInterfaceList returned error: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"Interface", "MAC Address", "Switch Chassis ID", "Switch Port ID",
		"eno1", "52:54:00:aa:bb:cc", "00:1b:0d:aa:bb:cc", "Ethernet1/7",
		// eno2 comes from all_interfaces only — it must still be listed.
		"eno2", "52:54:00:aa:bb:cd",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("interface list output missing %q\n---\n%s", want, out)
		}
	}
	if strings.Contains(out, "Switch Port MAU Type") {
		t.Errorf("default output should not carry --long columns:\n%s", out)
	}

	// --long adds the MAU type and marks which NICs ironic kept a port for.
	var long bytes.Buffer
	f := &introspectionInterfaceListFlags{long: true}
	if err := runIntrospectionInterfaceList(context.Background(), client, o, "node-1", f, &long); err != nil {
		t.Fatalf("runIntrospectionInterfaceList --long returned error: %v", err)
	}
	for _, want := range []string{"Switch Port MAU Type", "10GigBaseSR", "Node Port Created", "PXE Enabled"} {
		if !strings.Contains(long.String(), want) {
			t.Errorf("--long output missing %q\n---\n%s", want, long.String())
		}
	}
}

func TestRunIntrospectionDataSave_PreservesUnknownKeys(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/introspection/node-1/data", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(introspectionDataBody))
	})

	var buf bytes.Buffer
	err := runIntrospectionDataSave(context.Background(), introspectionClient(fakeServer), "node-1", &introspectionDataSaveFlags{}, &buf)
	if err != nil {
		t.Fatalf("runIntrospectionDataSave returned error: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("saved data is not valid JSON: %v\n%s", err, buf.String())
	}
	// The dump is untyped so plugin keys with no gophercloud struct survive.
	if _, ok := got["vendor_specific_extra"]; !ok {
		t.Errorf("saved introspection data dropped vendor_specific_extra:\n%s", buf.String())
	}
}

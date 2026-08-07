package baremetal

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

func TestRunNodeAbort_RequestBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotIronicVersion string
	fakeServer.Mux.HandleFunc("/nodes/node-1/states/provision", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotIronicVersion = r.Header.Get("X-OpenStack-Ironic-API-Version")
		th.TestJSONRequest(t, r, `{"target": "abort"}`)
		w.WriteHeader(http.StatusAccepted)
	})

	client := baremetalClient(fakeServer, "latest")

	var buf bytes.Buffer
	if err := runNodeAbort(context.Background(), client, "node-1", false, 0, &buf); err != nil {
		t.Fatalf("runNodeAbort returned error: %v", err)
	}

	if gotMethod != http.MethodPut {
		t.Errorf("request method = %q, want PUT", gotMethod)
	}
	if gotIronicVersion != "latest" {
		t.Errorf("X-OpenStack-Ironic-API-Version = %q, want latest", gotIronicVersion)
	}
	if out := buf.String(); !strings.Contains(out, "Requested abort for node node-1") {
		t.Errorf("unexpected output %q", out)
	}
}

// TestRunNodeAbort_WaitSettlesInFailureState covers the difference between abort
// and the other provision verbs: "clean failed" is the expected destination of a
// successful abort, so --wait must report it rather than treat it as an error.
func TestRunNodeAbort_WaitSettlesInFailureState(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/nodes/node-1/states/provision", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	var gets int
	fakeServer.Mux.HandleFunc("/nodes/node-1", func(w http.ResponseWriter, _ *http.Request) {
		gets++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if gets == 1 {
			// Still transitioning: target_provision_state is set.
			_, _ = w.Write([]byte(`{"uuid":"node-1","provision_state":"clean wait","target_provision_state":"available"}`))
			return
		}
		// Real ironic leaves target_provision_state populated after a successful
		// abort — only provision_state marks the transition as terminal.
		_, _ = w.Write([]byte(`{"uuid":"node-1","provision_state":"clean failed","target_provision_state":"available","last_error":"aborted by user"}`))
	})

	client := baremetalClient(fakeServer, "latest")

	// Keep the test fast.
	oldInterval := provisionPollInterval
	provisionPollInterval = time.Millisecond
	defer func() { provisionPollInterval = oldInterval }()

	var buf bytes.Buffer
	if err := runNodeAbort(context.Background(), client, "node-1", true, 10*time.Second, &buf); err != nil {
		t.Fatalf("runNodeAbort --wait returned error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `settled in provision state "clean failed"`) {
		t.Errorf("unexpected --wait output %q", out)
	}
	if !strings.Contains(out, "Last error: aborted by user") {
		t.Errorf("--wait output should report last_error, got %q", out)
	}
	if gets < 2 {
		t.Errorf("expected --wait to keep polling while the abort was in flight, got %d GETs", gets)
	}
}

const driverShowBody = `{
  "name": "redfish",
  "hosts": ["conductor-a", "conductor-b"],
  "type": "dynamic",
  "default_boot_interface": "redfish-virtual-media",
  "enabled_boot_interfaces": ["redfish-virtual-media", "pxe"],
  "default_power_interface": "redfish",
  "enabled_power_interfaces": ["redfish"],
  "default_management_interface": "redfish",
  "enabled_management_interfaces": ["redfish", "noop"],
  "default_deploy_interface": "direct",
  "enabled_deploy_interfaces": ["direct", "ramdisk"]
}`

func TestRunDriverShow_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	fakeServer.Mux.HandleFunc("/drivers/redfish", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(driverShowBody))
	})

	client := baremetalClient(fakeServer, "latest")
	o := &output.Options{Format: output.FormatTable}

	var buf bytes.Buffer
	if err := runDriverShow(context.Background(), client, o, "redfish", &buf); err != nil {
		t.Fatalf("runDriverShow returned error: %v", err)
	}

	if gotMethod != http.MethodGet {
		t.Errorf("request method = %q, want GET", gotMethod)
	}
	out := buf.String()
	for _, want := range []string{
		"name", "redfish", "hosts", "conductor-a", "type", "dynamic",
		"default_boot_interface", "redfish-virtual-media",
		"enabled_management_interfaces", "noop",
		"default_deploy_interface", "direct",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("driver show output missing %q\n---\n%s", want, out)
		}
	}
}

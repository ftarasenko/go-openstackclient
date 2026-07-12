package baremetal

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"
)

func TestRunNodeMaintenanceSet_RequestBodyAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotIronicVersion string
	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/nodes/node-a/maintenance", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotIronicVersion = r.Header.Get("X-OpenStack-Ironic-API-Version")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusAccepted)
	})

	client := baremetalClient(fakeServer, "1.80")

	var buf bytes.Buffer
	if err := runNodeMaintenanceSet(context.Background(), client, "node-a", "hw swap", &buf); err != nil {
		t.Fatalf("runNodeMaintenanceSet returned error: %v", err)
	}

	if gotMethod != http.MethodPut {
		t.Errorf("request method = %q, want PUT", gotMethod)
	}
	if gotIronicVersion != "1.80" {
		t.Errorf("X-OpenStack-Ironic-API-Version = %q, want 1.80", gotIronicVersion)
	}
	if gotBody["reason"] != "hw swap" {
		t.Errorf("maintenance body reason = %v, want %q", gotBody["reason"], "hw swap")
	}
	if !strings.Contains(buf.String(), "Set node node-a into maintenance mode") {
		t.Errorf("unexpected output: %q", buf.String())
	}
}

func TestRunNodeMaintenanceUnset_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	fakeServer.Mux.HandleFunc("/nodes/node-a/maintenance", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		w.WriteHeader(http.StatusAccepted)
	})

	client := baremetalClient(fakeServer, "1.80")

	var buf bytes.Buffer
	if err := runNodeMaintenanceUnset(context.Background(), client, "node-a", &buf); err != nil {
		t.Fatalf("runNodeMaintenanceUnset returned error: %v", err)
	}

	if gotMethod != http.MethodDelete {
		t.Errorf("request method = %q, want DELETE", gotMethod)
	}
	if !strings.Contains(buf.String(), "Took node node-a out of maintenance mode") {
		t.Errorf("unexpected output: %q", buf.String())
	}
}

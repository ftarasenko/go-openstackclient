package server

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"
)

// TestRunComputeServiceSet_AdminState exercises the KeyStack os-services
// admin_state extension (KCP-1886 / KCP-7988): a single PUT carrying
// admin_state (incl. the "Unstable" value) plus error_details/status/reason.
func TestRunComputeServiceSet_AdminState(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/os-services", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"services":[{"id":"svc-1","binary":"nova-compute","host":"cmp1"}]}`))
	})
	var gotMethod string
	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/os-services/svc-1", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotBody = decodeBody(t, r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"service":{"id":"svc-1","binary":"nova-compute","host":"cmp1","admin_state":"Unstable"}}`))
	})

	client := computeClient(fakeServer, "2.53")
	f := &serviceSetFlags{adminState: "Unstable", errorDetails: "flapping", status: "disable", reason: "hw fault"}
	var buf bytes.Buffer
	if err := runComputeServiceSet(context.Background(), client, "cmp1", "nova-compute", f, &buf); err != nil {
		t.Fatalf("runComputeServiceSet: %v", err)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
	if gotBody["admin_state"] != "Unstable" {
		t.Errorf("body.admin_state = %v, want Unstable", gotBody["admin_state"])
	}
	if gotBody["error_details"] != "flapping" {
		t.Errorf("body.error_details = %v, want flapping", gotBody["error_details"])
	}
	if gotBody["status"] != "disable" {
		t.Errorf("body.status = %v, want disable", gotBody["status"])
	}
	if gotBody["disabled_reason"] != "hw fault" {
		t.Errorf("body.disabled_reason = %v, want hw fault", gotBody["disabled_reason"])
	}
	if !strings.Contains(buf.String(), "Set admin state Unstable on compute service nova-compute on host cmp1") {
		t.Errorf("output = %q, want admin-state confirmation", buf.String())
	}
}

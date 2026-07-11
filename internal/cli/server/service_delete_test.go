package server

import (
	"bytes"
	"context"
	"net/http"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"
)

func TestRunComputeServiceDelete(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	// resolveServiceID lists services filtered by host+binary...
	fakeServer.Mux.HandleFunc("/os-services", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"services":[{"id":"svc-1","binary":"nova-compute","host":"cmp1"}]}`))
	})
	// ...then DELETEs by ID.
	var gotMethod string
	fakeServer.Mux.HandleFunc("/os-services/svc-1", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		w.WriteHeader(http.StatusNoContent)
	})

	client := computeClient(fakeServer, "2.53")
	var buf bytes.Buffer
	if err := runComputeServiceDelete(context.Background(), client, "cmp1", "nova-compute", &buf); err != nil {
		t.Fatalf("runComputeServiceDelete: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", gotMethod)
	}
}

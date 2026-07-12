package identity

import (
	"context"
	"net/http"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"
)

// TestResolveServiceID_MatchesClientSide confirms that service name resolution
// works even when the server ignores the ?name= filter and returns the whole
// catalog (keystone /v3/services filters only by type). The name is matched
// client-side, so a unique name resolves rather than reporting ambiguity.
func TestResolveServiceID_MatchesClientSide(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/services", func(w http.ResponseWriter, _ *http.Request) {
		// Return every service regardless of query, as keystone does.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"services": [
			{"id": "svc-nova", "name": "nova", "type": "compute"},
			{"id": "svc-glance", "name": "glance", "type": "image"},
			{"id": "svc-keystone", "name": "keystone", "type": "identity"}
		]}`))
	})

	client := identityClient(fakeServer)
	id, err := resolveServiceID(context.Background(), client, "glance")
	if err != nil {
		t.Fatalf("resolveServiceID returned error: %v", err)
	}
	if id != "svc-glance" {
		t.Errorf("resolved id = %q, want %q", id, "svc-glance")
	}
}

// TestResolveServiceID_FallsBackToLiteral confirms a name with no match returns
// the literal ref (OSC name-or-ID semantics), rather than erroring.
func TestResolveServiceID_FallsBackToLiteral(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/services", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"services": [{"id": "svc-nova", "name": "nova", "type": "compute"}]}`))
	})

	client := identityClient(fakeServer)
	id, err := resolveServiceID(context.Background(), client, "svc-nova")
	if err != nil {
		t.Fatalf("resolveServiceID returned error: %v", err)
	}
	if id != "svc-nova" {
		t.Errorf("resolved id = %q, want literal %q", id, "svc-nova")
	}
}

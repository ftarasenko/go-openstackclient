package identity

import (
	"context"
	"net/http"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"
)

// TestRunAppCredDelete_ResolvesNameToID confirms that deleting an application
// credential by name first lists the owning user's credentials filtered by name
// and then issues the DELETE against the matched ID (not the raw name).
func TestRunAppCredDelete_ResolvesNameToID(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const userID = "user-1"
	var listQuery, deletePath, deleteMethod string

	fakeServer.Mux.HandleFunc("/users/"+userID+"/application_credentials", func(w http.ResponseWriter, r *http.Request) {
		listQuery = r.URL.Query().Get("name")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"application_credentials":[{"id":"cred-id-9","name":"mycred"}]}`))
	})
	fakeServer.Mux.HandleFunc("/users/"+userID+"/application_credentials/cred-id-9", func(w http.ResponseWriter, r *http.Request) {
		deleteMethod = r.Method
		deletePath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})

	client := identityClient(fakeServer)
	if err := runAppCredDelete(context.Background(), client, userID, "mycred"); err != nil {
		t.Fatalf("runAppCredDelete returned error: %v", err)
	}
	if listQuery != "mycred" {
		t.Errorf("list name filter = %q, want %q", listQuery, "mycred")
	}
	if deleteMethod != http.MethodDelete {
		t.Errorf("delete method = %q, want DELETE", deleteMethod)
	}
	if deletePath != "/users/"+userID+"/application_credentials/cred-id-9" {
		t.Errorf("delete path = %q, want the resolved ID path", deletePath)
	}
}

// TestRunAppCredDelete_FallsBackToLiteralRef confirms that when no credential
// matches by name, the raw ref is used as the ID (OSC name-or-ID semantics).
func TestRunAppCredDelete_FallsBackToLiteralRef(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const userID = "user-1"
	var deletePath string

	fakeServer.Mux.HandleFunc("/users/"+userID+"/application_credentials", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"application_credentials":[]}`))
	})
	fakeServer.Mux.HandleFunc("/users/"+userID+"/application_credentials/raw-id", func(w http.ResponseWriter, r *http.Request) {
		deletePath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})

	client := identityClient(fakeServer)
	if err := runAppCredDelete(context.Background(), client, userID, "raw-id"); err != nil {
		t.Fatalf("runAppCredDelete returned error: %v", err)
	}
	if deletePath != "/users/"+userID+"/application_credentials/raw-id" {
		t.Errorf("delete path = %q, want literal ref path", deletePath)
	}
}

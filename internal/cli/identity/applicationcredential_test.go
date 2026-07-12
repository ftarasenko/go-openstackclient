package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
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

// TestRunAppCredCreate_UnrestrictedAndDescription confirms that --unrestricted
// and --description are sent in the create request body.
func TestRunAppCredCreate_UnrestrictedAndDescription(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const userID = "user-1"
	var body map[string]any

	fakeServer.Mux.HandleFunc("/users/"+userID+"/application_credentials", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"application_credential":{"id":"ac-1","name":"cred","unrestricted":true,"description":"my cred","secret":"s3cr3t"}}`))
	})

	client := identityClient(fakeServer)
	f := &appCredCreateFlags{description: "my cred", unrestricted: true}
	var buf bytes.Buffer
	o := &output.Options{Format: "value"}
	if err := runAppCredCreate(context.Background(), client, o, userID, "cred", f, &buf); err != nil {
		t.Fatalf("runAppCredCreate returned error: %v", err)
	}

	ac, ok := body["application_credential"].(map[string]any)
	if !ok {
		t.Fatalf("request body missing application_credential: %v", body)
	}
	if ac["unrestricted"] != true {
		t.Errorf("unrestricted = %v, want true", ac["unrestricted"])
	}
	if ac["description"] != "my cred" {
		t.Errorf("description = %v, want %q", ac["description"], "my cred")
	}
}

// TestRunAppCredShow_ResolvesNameAndOmitsSecret confirms that "show" resolves a
// name to its ID, GETs the credential, and renders without the secret.
func TestRunAppCredShow_ResolvesNameAndOmitsSecret(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const userID = "user-1"
	var getMethod string

	fakeServer.Mux.HandleFunc("/users/"+userID+"/application_credentials", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"application_credentials":[{"id":"ac-9","name":"mycred"}]}`))
	})
	fakeServer.Mux.HandleFunc("/users/"+userID+"/application_credentials/ac-9", func(w http.ResponseWriter, r *http.Request) {
		getMethod = r.Method
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"application_credential":{"id":"ac-9","name":"mycred","project_id":"p1","description":"d","unrestricted":false,"secret":"should-not-appear"}}`))
	})

	client := identityClient(fakeServer)
	var buf bytes.Buffer
	o := &output.Options{Format: "value"}
	if err := runAppCredShow(context.Background(), client, o, userID, "mycred", &buf); err != nil {
		t.Fatalf("runAppCredShow returned error: %v", err)
	}
	if getMethod != http.MethodGet {
		t.Errorf("get method = %q, want GET", getMethod)
	}
	if bytes.Contains(buf.Bytes(), []byte("should-not-appear")) {
		t.Errorf("show output leaked the secret: %s", buf.String())
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

package identity

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

func TestRunAppCredList_RequestAndTableOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const userID = "user-1"
	var gotMethod, gotPath string
	fakeServer.Mux.HandleFunc("/users/"+userID+"/application_credentials", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"application_credentials":[
			{"id":"c1","name":"mycred","project_id":"p1","description":"first"}
		]}`))
	})

	client := identityClient(fakeServer)
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runAppCredList(context.Background(), client, o, userID, &buf); err != nil {
		t.Fatalf("runAppCredList error: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if gotPath != "/users/"+userID+"/application_credentials" {
		t.Errorf("path = %q, want the user's credential collection", gotPath)
	}
	for _, want := range []string{"c1", "mycred", "p1", "first"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q\n---\n%s", want, buf.String())
		}
	}
}

func TestRunAppCredCreate_RequestBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const userID = "user-1"
	var gotMethod string
	fakeServer.Mux.HandleFunc("/users/"+userID+"/application_credentials", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		th.TestJSONRequest(t, r, `{"application_credential":{"name":"mycred","unrestricted":false}}`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"application_credential":{"id":"c-new","name":"mycred","project_id":"p1","secret":"topsecret"}}`))
	})

	client := identityClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}
	// No --role so no role resolution; no --expiration.
	f := &appCredCreateFlags{}
	var buf bytes.Buffer
	if err := runAppCredCreate(context.Background(), client, o, userID, "mycred", f, &buf); err != nil {
		t.Fatalf("runAppCredCreate error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	for _, want := range []string{"c-new", "mycred", "topsecret"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q\n---\n%s", want, buf.String())
		}
	}
}

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

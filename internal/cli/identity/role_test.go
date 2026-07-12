package identity

import (
	"bytes"
	"context"
	"net/http"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// TestRunRoleAdd_AssignmentPutURL exercises the "role add --project demo --user
// admin admin" flow: names are resolved to IDs, then the grant is a PUT to the
// role-assignment URL projects/{pid}/users/{uid}/roles/{rid}.
func TestRunRoleAdd_AssignmentPutURL(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/projects", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"projects": [{"id": "demo-id", "name": "demo"}]}`))
	})
	fakeServer.Mux.HandleFunc("/users", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"users": [{"id": "admin-id", "name": "admin"}]}`))
	})
	fakeServer.Mux.HandleFunc("/roles", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"roles": [{"id": "role-admin-id", "name": "admin"}]}`))
	})

	var putMethod, putPath string
	// The assignment PUT lands under the /projects/ subtree, distinct from the
	// exact "/projects" list handler above.
	fakeServer.Mux.HandleFunc("/projects/", func(w http.ResponseWriter, r *http.Request) {
		putMethod = r.Method
		putPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})

	client := identityClient(fakeServer)
	f := &grantFlags{user: "admin", project: "demo"}

	if err := runRoleAdd(context.Background(), client, "admin", f); err != nil {
		t.Fatalf("runRoleAdd returned error: %v", err)
	}

	if putMethod != http.MethodPut {
		t.Errorf("assignment method = %q, want PUT", putMethod)
	}
	if want := "/projects/demo-id/users/admin-id/roles/role-admin-id"; putPath != want {
		t.Errorf("assignment path = %q, want %q", putPath, want)
	}
}

// TestRunRoleAdd_UserDomainQualifiesLookup verifies that --user-domain (not the
// scope --domain) qualifies the --user name lookup: the /users list must carry
// the resolved user-domain ID, independent of the project scope.
func TestRunRoleAdd_UserDomainQualifiesLookup(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/domains", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"domains": [{"id": "userdom-id", "name": "userdom"}]}`))
	})
	fakeServer.Mux.HandleFunc("/projects", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"projects": [{"id": "demo-id", "name": "demo"}]}`))
	})
	var userDomainQuery string
	fakeServer.Mux.HandleFunc("/users", func(w http.ResponseWriter, r *http.Request) {
		userDomainQuery = r.URL.Query().Get("domain_id")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"users": [{"id": "admin-id", "name": "admin"}]}`))
	})
	fakeServer.Mux.HandleFunc("/roles", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"roles": [{"id": "role-admin-id", "name": "admin"}]}`))
	})
	fakeServer.Mux.HandleFunc("/projects/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	client := identityClient(fakeServer)
	f := &grantFlags{user: "admin", project: "demo", userDomain: "userdom"}

	if err := runRoleAdd(context.Background(), client, "admin", f); err != nil {
		t.Fatalf("runRoleAdd returned error: %v", err)
	}
	if userDomainQuery != "userdom-id" {
		t.Errorf("user lookup domain_id = %q, want %q", userDomainQuery, "userdom-id")
	}
}

// TestRunRoleAssignmentList_ProjectDomainMutuallyExclusive checks that supplying
// both --project and --domain scopes is rejected before any request is made.
func TestRunRoleAssignmentList_ProjectDomainMutuallyExclusive(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	client := identityClient(fakeServer)
	f := &assignmentListFlags{project: "demo", domain: "d1"}
	var buf bytes.Buffer
	err := runRoleAssignmentList(context.Background(), client, &output.Options{Format: "table"}, f, &buf)
	if err == nil {
		t.Fatal("expected error for --project + --domain, got nil")
	}
	if want := "mutually exclusive"; !bytes.Contains([]byte(err.Error()), []byte(want)) {
		t.Errorf("error = %q, want it to contain %q", err.Error(), want)
	}
}

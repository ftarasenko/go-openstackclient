package identity

import (
	"context"
	"net/http"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"
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

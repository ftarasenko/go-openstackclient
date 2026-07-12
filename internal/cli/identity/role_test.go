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

// TestRunRoleRemove_AssignmentDeleteURL mirrors role add but revokes: names are
// resolved and the grant is DELETEd from the assignment URL.
func TestRunRoleRemove_AssignmentDeleteURL(t *testing.T) {
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

	var delMethod, delPath string
	fakeServer.Mux.HandleFunc("/projects/", func(w http.ResponseWriter, r *http.Request) {
		delMethod = r.Method
		delPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})

	client := identityClient(fakeServer)
	f := &grantFlags{user: "admin", project: "demo"}
	if err := runRoleRemove(context.Background(), client, "admin", f); err != nil {
		t.Fatalf("runRoleRemove returned error: %v", err)
	}
	if delMethod != http.MethodDelete {
		t.Errorf("assignment method = %q, want DELETE", delMethod)
	}
	if want := "/projects/demo-id/users/admin-id/roles/role-admin-id"; delPath != want {
		t.Errorf("assignment path = %q, want %q", delPath, want)
	}
}

func TestRunRoleList_RequestAndTableOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotPath string
	fakeServer.Mux.HandleFunc("/roles", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"roles":[
			{"id":"r1","name":"admin","domain_id":""},
			{"id":"r2","name":"member","domain_id":"d1"}
		]}`))
	})

	client := identityClient(fakeServer)
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	// Empty domain avoids the resolve lookup.
	if err := runRoleList(context.Background(), client, o, "", &buf); err != nil {
		t.Fatalf("runRoleList error: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if gotPath != "/roles" {
		t.Errorf("path = %q, want /roles", gotPath)
	}
	for _, want := range []string{"r1", "admin", "r2", "member"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q\n---\n%s", want, buf.String())
		}
	}
}

func TestRunRoleShow_ResolvesNameAndGets(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var listName, getPath string
	fakeServer.Mux.HandleFunc("/roles", func(w http.ResponseWriter, r *http.Request) {
		listName = r.URL.Query().Get("name")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"roles":[{"id":"r1","name":"admin"}]}`))
	})
	fakeServer.Mux.HandleFunc("/roles/r1", func(w http.ResponseWriter, r *http.Request) {
		getPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"role":{"id":"r1","name":"admin","domain_id":"","description":"admin role"}}`))
	})

	client := identityClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	if err := runRoleShow(context.Background(), client, o, "admin", &buf); err != nil {
		t.Fatalf("runRoleShow error: %v", err)
	}
	if listName != "admin" {
		t.Errorf("resolve name = %q, want admin", listName)
	}
	if getPath != "/roles/r1" {
		t.Errorf("get path = %q, want /roles/r1", getPath)
	}
	for _, want := range []string{"r1", "admin", "admin role"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q\n---\n%s", want, buf.String())
		}
	}
}

func TestRunRoleAssignmentList_ResolvesFiltersAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/projects", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"projects":[{"id":"demo-id","name":"demo"}]}`))
	})
	fakeServer.Mux.HandleFunc("/users", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"users":[{"id":"admin-id","name":"admin"}]}`))
	})

	var gotMethod, gotPath, userQ, projQ string
	fakeServer.Mux.HandleFunc("/role_assignments", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		userQ = r.URL.Query().Get("user.id")
		projQ = r.URL.Query().Get("scope.project.id")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"role_assignments":[
			{"role":{"id":"r1"},"user":{"id":"admin-id"},"scope":{"project":{"id":"demo-id"}}}
		]}`))
	})

	client := identityClient(fakeServer)
	o := &output.Options{Format: output.FormatTable}
	f := &assignmentListFlags{user: "admin", project: "demo"}
	var buf bytes.Buffer
	if err := runRoleAssignmentList(context.Background(), client, o, f, &buf); err != nil {
		t.Fatalf("runRoleAssignmentList error: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if gotPath != "/role_assignments" {
		t.Errorf("path = %q, want /role_assignments", gotPath)
	}
	if userQ != "admin-id" {
		t.Errorf("user.id query = %q, want admin-id", userQ)
	}
	if projQ != "demo-id" {
		t.Errorf("scope.project.id query = %q, want demo-id", projQ)
	}
	for _, want := range []string{"r1", "admin-id", "demo-id"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q\n---\n%s", want, buf.String())
		}
	}
}

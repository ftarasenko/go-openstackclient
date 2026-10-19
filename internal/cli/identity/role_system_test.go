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

// "role add --system all" grants on keystone's system target:
// PUT /system/users/{uid}/roles/{rid}; "role remove --system all" is the DELETE.
func TestRunRoleAddRemove_SystemScope(t *testing.T) {
	for _, tc := range []struct{ name, method string }{
		{"add", http.MethodPut},
		{"remove", http.MethodDelete},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()
			fakeServer.Mux.HandleFunc("/users", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"users":[{"id":"admin-id","name":"admin"}]}`))
			})
			fakeServer.Mux.HandleFunc("/roles", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"roles":[{"id":"reader-id","name":"reader"}]}`))
			})
			var gotMethod, gotPath string
			fakeServer.Mux.HandleFunc("/system/", func(w http.ResponseWriter, r *http.Request) {
				gotMethod, gotPath = r.Method, r.URL.Path
				w.WriteHeader(http.StatusNoContent)
			})

			client := identityClient(fakeServer)
			f := &grantFlags{user: "admin", system: "all"}
			var err error
			if tc.name == "add" {
				err = runRoleAdd(context.Background(), client, "reader", f)
			} else {
				err = runRoleRemove(context.Background(), client, "reader", f)
			}
			if err != nil {
				t.Fatalf("role %s: %v", tc.name, err)
			}
			if gotMethod != tc.method {
				t.Errorf("method = %q, want %s", gotMethod, tc.method)
			}
			if want := "/system/users/admin-id/roles/reader-id"; gotPath != want {
				t.Errorf("path = %q, want %q", gotPath, want)
			}
		})
	}
}

// A grant takes exactly one scope, and "all" is the only system there is; both
// are refused before any request leaves.
func TestResolveGrant_ScopeValidation(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	client := identityClient(fakeServer)

	for _, tc := range []struct {
		name string
		f    grantFlags
		want string
	}{
		{"no scope", grantFlags{user: "u"}, "exactly one of --project, --domain or --system"},
		{"project and system", grantFlags{user: "u", project: "p", system: "all"}, "exactly one of --project, --domain or --system"},
		{"domain and system", grantFlags{user: "u", domain: "d", system: "all"}, "exactly one of --project, --domain or --system"},
		{"unknown system", grantFlags{user: "u", system: "nova"}, `the only system scope is "all"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolveGrant(context.Background(), client, "r", &tc.f)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

// --role filters by the resolved role ID (role.id) and --system by
// scope.system, the query keys keystone reads.
func TestRunRoleAssignmentList_RoleAndSystemFilters(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var domainQ string
	fakeServer.Mux.HandleFunc("/domains", func(w http.ResponseWriter, r *http.Request) {
		domainQ = r.URL.Query().Get("name")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"domains":[{"id":"rdom-id","name":"rdom"}]}`))
	})
	var roleDomainQ string
	fakeServer.Mux.HandleFunc("/roles", func(w http.ResponseWriter, r *http.Request) {
		roleDomainQ = r.URL.Query().Get("domain_id")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"roles":[{"id":"reader-id","name":"reader"}]}`))
	})
	var query map[string][]string
	fakeServer.Mux.HandleFunc("/role_assignments", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		query = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"role_assignments":[
			{"role":{"id":"reader-id"},"user":{"id":"u1"},"scope":{"system":{"all":true}}}
		]}`))
	})

	client := identityClient(fakeServer)
	f := &assignmentListFlags{role: "reader", roleDomain: "rdom", system: "all"}
	var buf bytes.Buffer
	if err := runRoleAssignmentList(context.Background(), client, &output.Options{Format: output.FormatCSV}, f, &buf); err != nil {
		t.Fatalf("runRoleAssignmentList: %v", err)
	}
	if domainQ != "rdom" || roleDomainQ != "rdom-id" {
		t.Errorf("role lookup: domain name %q, role domain_id %q; want rdom, rdom-id", domainQ, roleDomainQ)
	}
	if got := query["role.id"]; len(got) != 1 || got[0] != "reader-id" {
		t.Errorf("role.id = %v, want [reader-id]", got)
	}
	if got := query["scope.system"]; len(got) != 1 || got[0] != "all" {
		t.Errorf("scope.system = %v, want [all]", got)
	}
	for _, k := range []string{"scope.project.id", "scope.domain.id"} {
		if _, ok := query[k]; ok {
			t.Errorf("unexpected %s in %v", k, query)
		}
	}
	if want := "reader-id,u1,,,,all,false"; !strings.Contains(buf.String(), want) {
		t.Errorf("output = %q, want a row %s", buf.String(), want)
	}
}

func TestRunRoleAssignmentList_SystemScopeExclusive(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	client := identityClient(fakeServer)

	for _, f := range []assignmentListFlags{
		{project: "p", system: "all"},
		{domain: "d", system: "all"},
	} {
		err := runRoleAssignmentList(context.Background(), client, &output.Options{Format: output.FormatTable}, &f, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
			t.Errorf("%+v: err = %v, want mutually exclusive", f, err)
		}
	}
	f := assignmentListFlags{system: "everything"}
	if err := runRoleAssignmentList(context.Background(), client, &output.Options{Format: output.FormatTable}, &f, &bytes.Buffer{}); err == nil ||
		!strings.Contains(err.Error(), `the only system scope is "all"`) {
		t.Errorf("--system everything: err = %v", err)
	}
}

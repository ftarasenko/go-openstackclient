package identity_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/applicationcredentials"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/roles"

	"github.com/ftarasenko/koc/pkg/openstack/identity"
)

func TestListRoleAssignments_KeepsWhatGophercloudDrops(t *testing.T) {
	k, sc := newKeystone(t, map[string]func(*http.Request) (int, string){
		"GET /role_assignments": ok(`{"role_assignments":[
		  {"role":{"id":"r1","name":"reader","domain":{"id":"d1","name":"corp"}},
		   "user":{"id":"u1","name":"alice","domain":{"id":"default","name":"Default"}},
		   "scope":{"project":{"id":"p1","name":"web"},"OS-INHERIT:inherited_to":"projects"}},
		  {"role":{"id":"r2","name":"admin"},"group":{"id":"g1"},"scope":{"system":{"all":true}}}
		],"links":{"next":null}}`),
	})
	names := true
	all, err := identity.ListRoleAssignments(context.Background(), sc,
		roles.ListAssignmentsOpts{ScopeProjectID: "p1", UserID: "u1", IncludeNames: &names})
	if err != nil || len(all) != 2 {
		t.Fatalf("ListRoleAssignments = %+v, %v", all, err)
	}
	if got := k.only(t); got.Query != "include_names=true&scope.project.id=p1&user.id=u1" {
		t.Errorf("query = %q", got.Query)
	}
	a, b := all[0], all[1]
	if a.Role.ID != "r1" || a.User.Name != "alice" || a.Scope.Project.ID != "p1" ||
		a.RoleDomain.Name != "corp" || a.InheritedTo != "projects" {
		t.Errorf("first = %+v", a)
	}
	if b.Group.ID != "g1" || b.Scope.System == nil || !b.Scope.System.All || b.InheritedTo != "" || b.RoleDomain.ID != "" {
		t.Errorf("second = %+v", b)
	}
}

func TestRoleInferenceRules(t *testing.T) {
	k, sc := newKeystone(t, map[string]func(*http.Request) (int, string){
		"GET /role_inferences": ok(`{"role_inferences":[{"prior_role":{"id":"r2","name":"admin"},
		  "implies":[{"id":"r3","name":"member"},{"id":"r1","name":"reader"}]}]}`),
		"PUT /roles/r2/implies/r3": status(http.StatusCreated, `{"role_inference":{"prior_role":{"id":"r2","name":"admin"},
		  "implies":{"id":"r3","name":"member"}}}`),
		"DELETE /roles/r2/implies/r3": status(http.StatusNoContent, ""),
	})
	ctx := context.Background()
	list, err := identity.ListRoleInferenceRules(ctx, sc)
	if err != nil || len(list) != 1 || list[0].PriorRole.ID != "r2" || len(list[0].ImpliedRoles) != 2 {
		t.Fatalf("ListRoleInferenceRules = %+v, %v", list, err)
	}
	k.take(t)
	rule, err := identity.CreateRoleInferenceRule(ctx, sc, "r2", "r3")
	if err != nil || rule.RoleInference.ImpliedRole.Name != "member" {
		t.Fatalf("CreateRoleInferenceRule = %+v, %v", rule, err)
	}
	if got := k.only(t); got.Method != http.MethodPut || got.Path != "/roles/r2/implies/r3" {
		t.Errorf("create sent %+v", got)
	}
	if err := identity.DeleteRoleInferenceRule(ctx, sc, "r2", "r3"); err != nil {
		t.Fatal(err)
	}
	if got := k.only(t); got.Method != http.MethodDelete || got.Path != "/roles/r2/implies/r3" {
		t.Errorf("delete sent %+v", got)
	}
}

func TestRoleAndAppCredWrites(t *testing.T) {
	k, sc := newKeystone(t, map[string]func(*http.Request) (int, string){
		"GET /roles/r1":                         ok(`{"role":{"id":"r1","name":"reader"}}`),
		"POST /roles":                           status(http.StatusCreated, `{"role":{"id":"r9","name":"auditor"}}`),
		"PATCH /roles/r1":                       ok(`{"role":{"id":"r1"}}`),
		"DELETE /roles/r1":                      status(http.StatusNoContent, ""),
		"PUT /projects/p1/users/u1/roles/r1":    status(http.StatusNoContent, ""),
		"DELETE /projects/p1/users/u1/roles/r1": status(http.StatusNoContent, ""),
		"PUT /system/groups/g1/roles/r1":        status(http.StatusNoContent, ""),
		"PUT /domains/d1/users/u1/roles/r1":     status(http.StatusNoContent, ""),
		"GET /users/u1/application_credentials": ok(`{"application_credentials":[{"id":"ac1","name":"ci"}],"links":{"next":null}}`),
		"GET /users/u1/application_credentials/ac1": ok(`{"application_credential":{"id":"ac1","name":"ci",
		  "access_rules":[{"service":"compute","method":"GET","path":"/v2.1/servers"}]}}`),
		"POST /users/u1/application_credentials":       status(http.StatusCreated, `{"application_credential":{"id":"ac2","name":"ci","secret":"s3cr3t-example"}}`),
		"DELETE /users/u1/application_credentials/ac1": status(http.StatusNoContent, ""),
	})
	ctx := context.Background()
	desc := "read only"
	exp := time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)
	ignore := func(_ any, err error) error { return err }
	for _, tc := range []struct {
		do                 func() error
		method, path, body string
	}{
		{func() error { return ignore(identity.GetRole(ctx, sc, "r1")) }, "GET", "/roles/r1", ""},
		{func() error {
			return ignore(identity.CreateRole(ctx, sc, roles.CreateOpts{Name: "auditor", DomainID: "d1"}))
		}, "POST", "/roles", `{"role":{"domain_id":"d1","name":"auditor"}}`},
		{func() error {
			return ignore(identity.UpdateRole(ctx, sc, "r1", roles.UpdateOpts{Description: &desc}))
		}, "PATCH", "/roles/r1", `{"role":{"description":"read only"}}`},
		{func() error { return identity.DeleteRole(ctx, sc, "r1") }, "DELETE", "/roles/r1", ""},
		{func() error {
			return identity.AssignRole(ctx, sc, "r1", roles.AssignOpts{UserID: "u1", ProjectID: "p1"})
		}, "PUT", "/projects/p1/users/u1/roles/r1", ""},
		{func() error {
			return identity.AssignRole(ctx, sc, "r1", roles.AssignOpts{GroupID: "g1", System: true})
		}, "PUT", "/system/groups/g1/roles/r1", ""},
		{func() error {
			return identity.AssignRole(ctx, sc, "r1", roles.AssignOpts{UserID: "u1", DomainID: "d1"})
		}, "PUT", "/domains/d1/users/u1/roles/r1", ""},
		{func() error {
			return identity.UnassignRole(ctx, sc, "r1", roles.UnassignOpts{UserID: "u1", ProjectID: "p1"})
		}, "DELETE", "/projects/p1/users/u1/roles/r1", ""},
		{func() error {
			all, err := identity.ListApplicationCredentials(ctx, sc, "u1", applicationcredentials.ListOpts{Name: "ci"})
			if err == nil && (len(all) != 1 || all[0].ID != "ac1") {
				t.Errorf("ListApplicationCredentials = %+v", all)
			}
			return err
		}, "GET", "/users/u1/application_credentials", ""},
		{func() error {
			c, err := identity.GetApplicationCredential(ctx, sc, "u1", "ac1")
			if err == nil && (len(c.AccessRules) != 1 || c.AccessRules[0].Service != "compute") {
				t.Errorf("GetApplicationCredential = %+v", c)
			}
			return err
		}, "GET", "/users/u1/application_credentials/ac1", ""},
		{func() error {
			c, err := identity.CreateApplicationCredential(ctx, sc, "u1", applicationcredentials.CreateOpts{
				Name: "ci", Roles: []applicationcredentials.Role{{ID: "r1"}}, ExpiresAt: &exp})
			if err == nil && c.Secret != "s3cr3t-example" {
				t.Errorf("CreateApplicationCredential = %+v", c)
			}
			return err
		}, "POST", "/users/u1/application_credentials",
			`{"application_credential":{"expires_at":"2027-01-02T03:04:05","name":"ci","roles":[{"id":"r1"}],"unrestricted":false}}`},
		{func() error { return identity.DeleteApplicationCredential(ctx, sc, "u1", "ac1") }, "DELETE", "/users/u1/application_credentials/ac1", ""},
	} {
		if err := tc.do(); err != nil {
			t.Errorf("%s %s: %v", tc.method, tc.path, err)
			k.take(t)
			continue
		}
		if got := k.only(t); got.Method != tc.method || got.Path != tc.path || got.Body != tc.body {
			t.Errorf("sent %+v, want %s %s %s", got, tc.method, tc.path, tc.body)
		}
	}
	if got := k.take(t); len(got) != 0 {
		t.Errorf("stray requests %+v", got)
	}
}

func TestRoleErrors_NameWhatWasAsked(t *testing.T) {
	_, sc := newKeystone(t, nil)
	ctx := context.Background()
	ignore := func(_ any, err error) error { return err }
	for want, err := range map[string]error{
		"showing role r1":                            ignore(identity.GetRole(ctx, sc, "r1")),
		`creating role "auditor"`:                    ignore(identity.CreateRole(ctx, sc, roles.CreateOpts{Name: "auditor"})),
		"updating role r1":                           ignore(identity.UpdateRole(ctx, sc, "r1", roles.UpdateOpts{Name: "x"})),
		"deleting role r1":                           identity.DeleteRole(ctx, sc, "r1"),
		"granting role r1":                           identity.AssignRole(ctx, sc, "r1", roles.AssignOpts{UserID: "u1", ProjectID: "p1"}),
		"revoking role r1":                           identity.UnassignRole(ctx, sc, "r1", roles.UnassignOpts{UserID: "u1", ProjectID: "p1"}),
		"listing role assignments":                   ignore(identity.ListRoleAssignments(ctx, sc, roles.ListAssignmentsOpts{})),
		"listing role-inference rules":               ignore(identity.ListRoleInferenceRules(ctx, sc)),
		"creating role-inference rule r2 implies r3": ignore(identity.CreateRoleInferenceRule(ctx, sc, "r2", "r3")),
		"deleting role-inference rule r2 implies r3": identity.DeleteRoleInferenceRule(ctx, sc, "r2", "r3"),
		"listing application credentials of user u1": ignore(identity.ListApplicationCredentials(ctx, sc, "u1", applicationcredentials.ListOpts{})),
		"showing application credential ac1":         ignore(identity.GetApplicationCredential(ctx, sc, "u1", "ac1")),
		`creating application credential "ci"`:       ignore(identity.CreateApplicationCredential(ctx, sc, "u1", applicationcredentials.CreateOpts{Name: "ci"})),
		"deleting application credential ac1":        identity.DeleteApplicationCredential(ctx, sc, "u1", "ac1"),
	} {
		if err == nil || !strings.HasPrefix(err.Error(), want+": ") || !gophercloud.ResponseCodeIs(err, http.StatusNotFound) {
			t.Errorf("err = %v, want %q wrapping the 404", err, want)
		}
	}
	// A grant with no actor fails in gophercloud before any request; it is
	// still labelled.
	if err := identity.AssignRole(ctx, sc, "r1", roles.AssignOpts{ProjectID: "p1"}); err == nil ||
		!strings.HasPrefix(err.Error(), "granting role r1: ") {
		t.Errorf("actorless grant = %v", err)
	}
}

func TestListRoleAssignments_Malformed(t *testing.T) {
	_, sc := newKeystone(t, map[string]func(*http.Request) (int, string){
		"GET /role_assignments": ok(`{"role_assignments":"not a list","links":{"next":null}}`),
	})
	if _, err := identity.ListRoleAssignments(context.Background(), sc, roles.ListAssignmentsOpts{}); err == nil ||
		!strings.HasPrefix(err.Error(), "listing role assignments: ") {
		t.Errorf("err = %v", err)
	}
}

func TestCreateRole_DuplicateIsDetectable(t *testing.T) {
	_, sc := newKeystone(t, map[string]func(*http.Request) (int, string){
		"POST /roles": status(http.StatusConflict, `{"error":{"code":409,"message":"Conflict occurred attempting to store role."}}`),
	})
	_, err := identity.CreateRole(context.Background(), sc, roles.CreateOpts{Name: "reader"})
	if !gophercloud.ResponseCodeIs(err, http.StatusConflict) {
		t.Errorf("err = %v, want a detectable 409", err)
	}
}

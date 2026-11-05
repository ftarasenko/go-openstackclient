package identity_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/projects"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/roles"

	"github.com/ftarasenko/go-openstackclient/pkg/openstack"
	"github.com/ftarasenko/go-openstackclient/pkg/openstack/identity"
)

// List a domain's projects, then add a user to a group.
func ExampleListProjects() {
	keystoneAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Println(strings.TrimSpace(r.Method + " " + r.URL.Path + " " + r.URL.RawQuery))
		if r.Method == http.MethodPut {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"projects":[{"id":"9f1e2d3c4b5a4c6d8e7f0a1b2c3d4e5f","name":"web","domain_id":"default"}],"links":{"next":null}}`)
	}))
	defer keystoneAPI.Close()

	pc := &gophercloud.ProviderClient{
		EndpointLocator: func(gophercloud.EndpointOpts) (string, error) { return keystoneAPI.URL + "/v3/", nil },
	}
	client, err := openstack.FromProvider(pc, gophercloud.EndpointOpts{Region: "RegionOne"}, openstack.Options{}).Identity()
	if err != nil {
		fmt.Println(err)
		return
	}
	ctx := context.Background()
	list, err := identity.ListProjects(ctx, client, projects.ListOpts{DomainID: "default"})
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, p := range list {
		fmt.Println(p.Name, p.ID)
	}
	if err := identity.AddUserToGroup(ctx, client, "ops", "alice"); err != nil {
		fmt.Println(err)
	}
	// Output:
	// GET /v3/projects domain_id=default
	// web 9f1e2d3c4b5a4c6d8e7f0a1b2c3d4e5f
	// PUT /v3/groups/ops/users/alice
}

// Grant a role on a project, then list the project's assignments with names,
// including the inheritance marker gophercloud's own type drops.
func ExampleListRoleAssignments() {
	keystoneAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Println(strings.TrimSpace(r.Method + " " + r.URL.Path + " " + r.URL.RawQuery))
		if r.Method == http.MethodPut {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"role_assignments":[
		  {"role":{"id":"r1","name":"reader"},"user":{"id":"u1","name":"alice"},"scope":{"project":{"id":"p1","name":"web"}}},
		  {"role":{"id":"r2","name":"member"},"group":{"id":"g1","name":"ops"},
		   "scope":{"project":{"id":"p1","name":"web"},"OS-INHERIT:inherited_to":"projects"}}],"links":{"next":null}}`)
	}))
	defer keystoneAPI.Close()

	pc := &gophercloud.ProviderClient{
		EndpointLocator: func(gophercloud.EndpointOpts) (string, error) { return keystoneAPI.URL + "/v3/", nil },
	}
	client, err := openstack.FromProvider(pc, gophercloud.EndpointOpts{Region: "RegionOne"}, openstack.Options{}).Identity()
	if err != nil {
		fmt.Println(err)
		return
	}
	ctx := context.Background()
	if err := identity.AssignRole(ctx, client, "r1", roles.AssignOpts{UserID: "u1", ProjectID: "p1"}); err != nil {
		fmt.Println(err)
		return
	}
	names := true
	list, err := identity.ListRoleAssignments(ctx, client, roles.ListAssignmentsOpts{ScopeProjectID: "p1", IncludeNames: &names})
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, a := range list {
		fmt.Println(a.Role.Name, a.User.Name+a.Group.Name, a.InheritedTo != "")
	}
	// Output:
	// PUT /v3/projects/p1/users/u1/roles/r1
	// GET /v3/role_assignments include_names=true&scope.project.id=p1
	// reader alice false
	// member ops true
}

package identity_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/domains"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/endpoints"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/groups"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/projects"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/regions"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/roles"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/services"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/users"
	th "github.com/gophercloud/gophercloud/v2/testhelper"
	fakeclient "github.com/gophercloud/gophercloud/v2/testhelper/client"

	"github.com/ftarasenko/go-openstackclient/pkg/openstack/identity"
)

const subject = "gAAAAA-example-subject-token"

type call struct{ Method, Path, Query, Subject, Body string }

// keystone is a fake identity endpoint that records every request and answers
// it from routes, keyed "METHOD /path".
type keystone struct {
	mu   sync.Mutex
	seen []call
}

func newKeystone(t *testing.T, routes map[string]func(r *http.Request) (int, string)) (*keystone, *gophercloud.ServiceClient) {
	t.Helper()
	fs := th.SetupHTTP()
	t.Cleanup(fs.Teardown)
	k := &keystone{}
	fs.Mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body := string(raw)
		var v any
		if json.Unmarshal(raw, &v) == nil {
			b, _ := json.Marshal(v)
			body = string(b)
		}
		k.mu.Lock()
		k.seen = append(k.seen, call{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("X-Subject-Token"), body})
		k.mu.Unlock()
		reply, ok := routes[r.Method+" "+r.URL.Path]
		if !ok {
			reply = func(*http.Request) (int, string) {
				return http.StatusNotFound, `{"error":{"code":404,"message":"not found"}}`
			}
		}
		status, out := reply(r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, out)
	})
	sc := fakeclient.ServiceClient(fs)
	sc.Type = "identity"
	return k, sc
}

func (k *keystone) take(t *testing.T) []call {
	t.Helper()
	k.mu.Lock()
	defer k.mu.Unlock()
	got := k.seen
	k.seen = nil
	return got
}

func (k *keystone) only(t *testing.T) call {
	t.Helper()
	got := k.take(t)
	if len(got) != 1 {
		t.Fatalf("want one request, got %+v", got)
	}
	return got[0]
}

func ok(body string) func(*http.Request) (int, string) {
	return func(*http.Request) (int, string) { return http.StatusOK, body }
}

func status(code int, body string) func(*http.Request) (int, string) {
	return func(*http.Request) (int, string) { return code, body }
}

func TestListings_FiltersAndPages(t *testing.T) {
	var base string
	k, sc := newKeystone(t, map[string]func(*http.Request) (int, string){
		"GET /domains":         ok(`{"domains":[{"id":"d1","name":"Default"}],"links":{"next":null}}`),
		"GET /projects":        ok(`{"projects":[{"id":"p1","name":"web"}],"links":{"next":null}}`),
		"GET /groups":          ok(`{"groups":[{"id":"g1","name":"ops"}],"links":{"next":null}}`),
		"GET /regions":         ok(`{"regions":[{"id":"RegionOne"}],"links":{"next":null}}`),
		"GET /services":        ok(`{"services":[{"id":"s1","type":"compute","name":"nova"}],"links":{"next":null}}`),
		"GET /endpoints":       ok(`{"endpoints":[{"id":"e1","interface":"public","url":"https://compute.example.com"}],"links":{"next":null}}`),
		"GET /roles":           ok(`{"roles":[{"id":"r1","name":"reader"}],"links":{"next":null}}`),
		"GET /users/u1/groups": ok(`{"groups":[{"id":"g1","name":"ops","domain_id":"d1"}],"links":{"next":null}}`),
		"GET /users": func(r *http.Request) (int, string) {
			if r.URL.Query().Get("page") == "2" {
				return http.StatusOK, `{"users":[{"id":"u2","name":"bob"}],"links":{"next":null}}`
			}
			return http.StatusOK, `{"users":[{"id":"u1","name":"alice"}],"links":{"next":"` + base + `users?page=2"}}`
		},
	})
	base = sc.Endpoint
	ctx := context.Background()

	for _, tc := range []struct {
		name  string
		list  func() (int, error)
		query []string
	}{
		{"domains", func() (int, error) {
			all, err := identity.ListDomains(ctx, sc, domains.ListOpts{Name: "Default"})
			return len(all), err
		}, []string{"name=Default"}},
		{"projects", func() (int, error) {
			all, err := identity.ListProjects(ctx, sc, projects.ListOpts{Name: "web", DomainID: "d1"})
			return len(all), err
		}, []string{"domain_id=d1&name=web"}},
		{"users, two pages", func() (int, error) {
			all, err := identity.ListUsers(ctx, sc, users.ListOpts{DomainID: "d1"})
			return len(all), err
		}, []string{"domain_id=d1", "page=2"}},
		{"groups", func() (int, error) {
			all, err := identity.ListGroups(ctx, sc, groups.ListOpts{DomainID: "d1"})
			return len(all), err
		}, []string{"domain_id=d1"}},
		{"groups of a user", func() (int, error) {
			all, err := identity.ListUserGroups(ctx, sc, "u1")
			return len(all), err
		}, []string{""}},
		{"regions", func() (int, error) {
			all, err := identity.ListRegions(ctx, sc, regions.ListOpts{})
			return len(all), err
		}, []string{""}},
		{"services", func() (int, error) {
			all, err := identity.ListServices(ctx, sc, services.ListOpts{ServiceType: "compute"})
			return len(all), err
		}, []string{"type=compute"}},
		{"endpoints", func() (int, error) {
			all, err := identity.ListEndpoints(ctx, sc, endpoints.ListOpts{Availability: gophercloud.AvailabilityPublic, ServiceID: "s1"})
			return len(all), err
		}, []string{"interface=public&service_id=s1"}},
		{"roles", func() (int, error) {
			all, err := identity.ListRoles(ctx, sc, roles.ListOpts{Name: "reader"})
			return len(all), err
		}, []string{"name=reader"}},
	} {
		n, err := tc.list()
		if err != nil || n != len(tc.query) {
			t.Errorf("%s: %d items, %v; want %d", tc.name, n, err, len(tc.query))
		}
		got := k.take(t)
		if len(got) != len(tc.query) {
			t.Fatalf("%s: requests %+v", tc.name, got)
		}
		for i, q := range tc.query {
			if got[i].Method != http.MethodGet || got[i].Query != q {
				t.Errorf("%s: request %d = %+v, want query %q", tc.name, i, got[i], q)
			}
		}
	}
}

func TestWrites(t *testing.T) {
	k, sc := newKeystone(t, map[string]func(*http.Request) (int, string){
		"GET /domains/d1":            ok(`{"domain":{"id":"d1","name":"Default"}}`),
		"POST /domains":              status(http.StatusCreated, `{"domain":{"id":"d2","name":"new"}}`),
		"PATCH /domains/d1":          ok(`{"domain":{"id":"d1","name":"Default"}}`),
		"DELETE /domains/d1":         status(http.StatusNoContent, ""),
		"GET /projects/p1":           ok(`{"project":{"id":"p1","name":"web"}}`),
		"POST /projects":             status(http.StatusCreated, `{"project":{"id":"p2","name":"new"}}`),
		"PATCH /projects/p1":         ok(`{"project":{"id":"p1"}}`),
		"DELETE /projects/p1":        status(http.StatusNoContent, ""),
		"GET /users/u1":              ok(`{"user":{"id":"u1","name":"alice"}}`),
		"POST /users":                status(http.StatusCreated, `{"user":{"id":"u2","name":"bob"}}`),
		"PATCH /users/u1":            ok(`{"user":{"id":"u1"}}`),
		"DELETE /users/u1":           status(http.StatusNoContent, ""),
		"POST /users/u1/password":    status(http.StatusNoContent, ""),
		"GET /groups/g1":             ok(`{"group":{"id":"g1","name":"ops"}}`),
		"POST /groups":               status(http.StatusCreated, `{"group":{"id":"g2","name":"new"}}`),
		"PATCH /groups/g1":           ok(`{"group":{"id":"g1"}}`),
		"DELETE /groups/g1":          status(http.StatusNoContent, ""),
		"PUT /groups/g1/users/u1":    status(http.StatusNoContent, ""),
		"DELETE /groups/g1/users/u1": status(http.StatusNoContent, ""),
		"GET /regions/RegionOne":     ok(`{"region":{"id":"RegionOne"}}`),
		"POST /regions":              status(http.StatusCreated, `{"region":{"id":"RegionTwo"}}`),
		"PATCH /regions/RegionOne":   ok(`{"region":{"id":"RegionOne"}}`),
		"DELETE /regions/RegionOne":  status(http.StatusNoContent, ""),
		"GET /services/s1":           ok(`{"service":{"id":"s1","type":"compute"}}`),
		"POST /services":             status(http.StatusCreated, `{"service":{"id":"s2","type":"dns"}}`),
		"PATCH /services/s1":         ok(`{"service":{"id":"s1"}}`),
		"DELETE /services/s1":        status(http.StatusNoContent, ""),
		"GET /endpoints/e1":          ok(`{"endpoint":{"id":"e1"}}`),
		"POST /endpoints":            status(http.StatusCreated, `{"endpoint":{"id":"e2"}}`),
		"PATCH /endpoints/e1":        ok(`{"endpoint":{"id":"e1"}}`),
		"DELETE /endpoints/e1":       status(http.StatusNoContent, ""),
	})
	ctx := context.Background()
	off := false
	desc := "web tier"
	ignore := func(_ any, err error) error { return err }
	for _, tc := range []struct {
		do         func() error
		method     string
		path, body string
	}{
		{func() error { return ignore(identity.GetDomain(ctx, sc, "d1")) }, "GET", "/domains/d1", ""},
		{func() error {
			return ignore(identity.CreateDomain(ctx, sc, domains.CreateOpts{Name: "new", Enabled: &off}))
		}, "POST", "/domains", `{"domain":{"enabled":false,"name":"new"}}`},
		{func() error {
			return ignore(identity.UpdateDomain(ctx, sc, "d1", domains.UpdateOpts{Description: &desc}))
		}, "PATCH", "/domains/d1", `{"domain":{"description":"web tier"}}`},
		{func() error { return identity.DeleteDomain(ctx, sc, "d1") }, "DELETE", "/domains/d1", ""},
		{func() error { return ignore(identity.GetProject(ctx, sc, "p1")) }, "GET", "/projects/p1", ""},
		{func() error {
			return ignore(identity.CreateProject(ctx, sc, projects.CreateOpts{Name: "new", DomainID: "d1", Extra: map[string]any{"tier": "web"}}))
		}, "POST", "/projects", `{"project":{"domain_id":"d1","name":"new","tier":"web"}}`},
		{func() error {
			return ignore(identity.UpdateProject(ctx, sc, "p1", projects.UpdateOpts{Name: "renamed"}))
		}, "PATCH", "/projects/p1", `{"project":{"name":"renamed"}}`},
		{func() error { return identity.DeleteProject(ctx, sc, "p1") }, "DELETE", "/projects/p1", ""},
		{func() error { return ignore(identity.GetUser(ctx, sc, "u1")) }, "GET", "/users/u1", ""},
		{func() error {
			return ignore(identity.CreateUser(ctx, sc, users.CreateOpts{Name: "bob", DomainID: "d1"}))
		}, "POST", "/users", `{"user":{"domain_id":"d1","name":"bob"}}`},
		{func() error {
			return ignore(identity.UpdateUser(ctx, sc, "u1", users.UpdateOpts{Extra: map[string]any{"email": "alice@example.com"}}))
		}, "PATCH", "/users/u1", `{"user":{"email":"alice@example.com"}}`},
		{func() error { return identity.DeleteUser(ctx, sc, "u1") }, "DELETE", "/users/u1", ""},
		{func() error {
			return identity.ChangeUserPassword(ctx, sc, "u1", users.ChangePasswordOpts{OriginalPassword: "old-example", Password: "new-example"})
		}, "POST", "/users/u1/password", `{"user":{"original_password":"old-example","password":"new-example"}}`},
		{func() error { return ignore(identity.GetGroup(ctx, sc, "g1")) }, "GET", "/groups/g1", ""},
		{func() error {
			return ignore(identity.CreateGroup(ctx, sc, groups.CreateOpts{Name: "new", DomainID: "d1"}))
		}, "POST", "/groups", `{"group":{"domain_id":"d1","name":"new"}}`},
		{func() error { return ignore(identity.UpdateGroup(ctx, sc, "g1", groups.UpdateOpts{Name: "renamed"})) }, "PATCH", "/groups/g1", `{"group":{"name":"renamed"}}`},
		{func() error { return identity.DeleteGroup(ctx, sc, "g1") }, "DELETE", "/groups/g1", ""},
		{func() error { return identity.AddUserToGroup(ctx, sc, "g1", "u1") }, "PUT", "/groups/g1/users/u1", ""},
		{func() error { return identity.RemoveUserFromGroup(ctx, sc, "g1", "u1") }, "DELETE", "/groups/g1/users/u1", ""},
		{func() error { return ignore(identity.GetRegion(ctx, sc, "RegionOne")) }, "GET", "/regions/RegionOne", ""},
		{func() error {
			return ignore(identity.CreateRegion(ctx, sc, regions.CreateOpts{ID: "RegionTwo", ParentRegionID: "RegionOne"}))
		}, "POST", "/regions", `{"region":{"id":"RegionTwo","parent_region_id":"RegionOne"}}`},
		{func() error {
			return ignore(identity.UpdateRegion(ctx, sc, "RegionOne", regions.UpdateOpts{Description: &desc}))
		}, "PATCH", "/regions/RegionOne", `{"region":{"description":"web tier"}}`},
		{func() error { return identity.DeleteRegion(ctx, sc, "RegionOne") }, "DELETE", "/regions/RegionOne", ""},
		{func() error { return ignore(identity.GetService(ctx, sc, "s1")) }, "GET", "/services/s1", ""},
		{func() error {
			return ignore(identity.CreateService(ctx, sc, services.CreateOpts{Type: "dns", Enabled: &off}))
		}, "POST", "/services", `{"service":{"enabled":false,"type":"dns"}}`},
		{func() error {
			return ignore(identity.UpdateService(ctx, sc, "s1", services.UpdateOpts{Type: "compute"}))
		}, "PATCH", "/services/s1", `{"service":{"type":"compute"}}`},
		{func() error { return identity.DeleteService(ctx, sc, "s1") }, "DELETE", "/services/s1", ""},
		{func() error { return ignore(identity.GetEndpoint(ctx, sc, "e1")) }, "GET", "/endpoints/e1", ""},
		{func() error {
			return ignore(identity.CreateEndpoint(ctx, sc, endpoints.CreateOpts{Availability: gophercloud.AvailabilityInternal,
				Region: "RegionOne", ServiceID: "s1", URL: "https://compute.example.com"}))
		}, "POST", "/endpoints", `{"endpoint":{"interface":"internal","region":"RegionOne","service_id":"s1","url":"https://compute.example.com"}}`},
		{func() error {
			return ignore(identity.UpdateEndpoint(ctx, sc, "e1", endpoints.UpdateOpts{URL: "https://c.example.com"}))
		}, "PATCH", "/endpoints/e1", `{"endpoint":{"url":"https://c.example.com"}}`},
		{func() error { return identity.DeleteEndpoint(ctx, sc, "e1") }, "DELETE", "/endpoints/e1", ""},
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
}

func TestIsUserInGroup(t *testing.T) {
	_, sc := newKeystone(t, map[string]func(*http.Request) (int, string){
		"HEAD /groups/g1/users/u1": status(http.StatusNoContent, ""),
		"HEAD /groups/g1/users/u2": status(http.StatusNotFound, ""),
		"HEAD /groups/g1/users/u3": status(http.StatusForbidden, ""),
	})
	ctx := context.Background()
	if in, err := identity.IsUserInGroup(ctx, sc, "g1", "u1"); err != nil || !in {
		t.Errorf("member = %v, %v", in, err)
	}
	if in, err := identity.IsUserInGroup(ctx, sc, "g1", "u2"); err != nil || in {
		t.Errorf("non-member = %v, %v", in, err)
	}
	if _, err := identity.IsUserInGroup(ctx, sc, "g1", "u3"); err == nil ||
		!strings.HasPrefix(err.Error(), "checking whether user u3 is in group g1: ") {
		t.Errorf("forbidden = %v", err)
	}
}

const tokenBody = `{"token":{"expires_at":"2026-12-01T00:00:00.000000Z","methods":["password"],
  "user":{"id":"u1","name":"alice","domain":{"id":"default","name":"Default"}}%s}}`

func TestTokens(t *testing.T) {
	scoped := true
	k, sc := newKeystone(t, map[string]func(*http.Request) (int, string){
		"GET /auth/tokens": func(*http.Request) (int, string) {
			if scoped {
				return http.StatusOK, strings.Replace(tokenBody, "%s", `,"project":{"id":"p1","name":"web","domain":{"id":"default"}}`, 1)
			}
			return http.StatusOK, strings.Replace(tokenBody, "%s", "", 1)
		},
		"DELETE /auth/tokens": status(http.StatusNoContent, ""),
		"GET /auth/catalog":   ok(`{"catalog":[{"id":"s1","name":"nova","type":"compute","endpoints":[{"id":"e1","interface":"public","region":"RegionOne","url":"https://compute.example.com"}]}]}`),
	})
	ctx := context.Background()

	info, err := identity.IntrospectToken(ctx, sc, subject)
	if err != nil || info.Token.ExpiresAt.Year() != 2026 || info.Project == nil || info.Project.ID != "p1" || info.User == nil || info.User.ID != "u1" {
		t.Fatalf("IntrospectToken = %+v, %v", info, err)
	}
	if got := k.only(t); got.Subject != subject {
		t.Errorf("sent %+v", got)
	}
	scoped = false
	if info, err = identity.IntrospectToken(ctx, sc, subject); err != nil || info.Project != nil || info.User == nil {
		t.Errorf("domain-scoped IntrospectToken = %+v, %v", info, err)
	}
	k.take(t)

	if u, err := identity.TokenUser(ctx, sc, subject); err != nil || u.ID != "u1" {
		t.Errorf("TokenUser = %+v, %v", u, err)
	}
	k.take(t)

	if err := identity.RevokeToken(ctx, sc, subject); err != nil {
		t.Fatal(err)
	}
	if got := k.only(t); got.Method != http.MethodDelete || got.Subject != subject {
		t.Errorf("revoke sent %+v", got)
	}

	cat, err := identity.ListCatalog(ctx, sc)
	if err != nil || len(cat) != 1 || cat[0].Type != "compute" || len(cat[0].Endpoints) != 1 {
		t.Errorf("ListCatalog = %+v, %v", cat, err)
	}
}

func TestErrors_NameWhatWasAsked(t *testing.T) {
	_, sc := newKeystone(t, nil)
	ctx := context.Background()
	ignore := func(_ any, err error) error { return err }
	for want, err := range map[string]error{
		"listing domains":                ignore(identity.ListDomains(ctx, sc, domains.ListOpts{})),
		"listing projects":               ignore(identity.ListProjects(ctx, sc, projects.ListOpts{})),
		"listing users":                  ignore(identity.ListUsers(ctx, sc, users.ListOpts{})),
		"listing groups":                 ignore(identity.ListGroups(ctx, sc, groups.ListOpts{})),
		"listing groups of user u1":      ignore(identity.ListUserGroups(ctx, sc, "u1")),
		"listing regions":                ignore(identity.ListRegions(ctx, sc, regions.ListOpts{})),
		"listing services":               ignore(identity.ListServices(ctx, sc, services.ListOpts{})),
		"listing endpoints":              ignore(identity.ListEndpoints(ctx, sc, endpoints.ListOpts{})),
		"listing roles":                  ignore(identity.ListRoles(ctx, sc, roles.ListOpts{})),
		"listing catalog":                ignore(identity.ListCatalog(ctx, sc)),
		"showing domain d1":              ignore(identity.GetDomain(ctx, sc, "d1")),
		`creating domain "new"`:          ignore(identity.CreateDomain(ctx, sc, domains.CreateOpts{Name: "new"})),
		"updating domain d1":             ignore(identity.UpdateDomain(ctx, sc, "d1", domains.UpdateOpts{Name: "x"})),
		"deleting domain d1":             identity.DeleteDomain(ctx, sc, "d1"),
		"showing project p1":             ignore(identity.GetProject(ctx, sc, "p1")),
		`creating project "new"`:         ignore(identity.CreateProject(ctx, sc, projects.CreateOpts{Name: "new"})),
		"updating project p1":            ignore(identity.UpdateProject(ctx, sc, "p1", projects.UpdateOpts{Name: "x"})),
		"deleting project p1":            identity.DeleteProject(ctx, sc, "p1"),
		"showing user u1":                ignore(identity.GetUser(ctx, sc, "u1")),
		`creating user "bob"`:            ignore(identity.CreateUser(ctx, sc, users.CreateOpts{Name: "bob"})),
		"updating user u1":               ignore(identity.UpdateUser(ctx, sc, "u1", users.UpdateOpts{Name: "x"})),
		"deleting user u1":               identity.DeleteUser(ctx, sc, "u1"),
		"changing password of user u1":   identity.ChangeUserPassword(ctx, sc, "u1", users.ChangePasswordOpts{Password: "x", OriginalPassword: "y"}),
		"showing group g1":               ignore(identity.GetGroup(ctx, sc, "g1")),
		`creating group "new"`:           ignore(identity.CreateGroup(ctx, sc, groups.CreateOpts{Name: "new"})),
		"updating group g1":              ignore(identity.UpdateGroup(ctx, sc, "g1", groups.UpdateOpts{Name: "x"})),
		"deleting group g1":              identity.DeleteGroup(ctx, sc, "g1"),
		"adding user u1 to group g1":     identity.AddUserToGroup(ctx, sc, "g1", "u1"),
		"removing user u1 from group g1": identity.RemoveUserFromGroup(ctx, sc, "g1", "u1"),
		"showing region RegionOne":       ignore(identity.GetRegion(ctx, sc, "RegionOne")),
		`creating region "RegionTwo"`:    ignore(identity.CreateRegion(ctx, sc, regions.CreateOpts{ID: "RegionTwo"})),
		"updating region RegionOne":      ignore(identity.UpdateRegion(ctx, sc, "RegionOne", regions.UpdateOpts{ParentRegionID: "x"})),
		"deleting region RegionOne":      identity.DeleteRegion(ctx, sc, "RegionOne"),
		"showing service s1":             ignore(identity.GetService(ctx, sc, "s1")),
		`creating service of type "dns"`: ignore(identity.CreateService(ctx, sc, services.CreateOpts{Type: "dns"})),
		"updating service s1":            ignore(identity.UpdateService(ctx, sc, "s1", services.UpdateOpts{Type: "x"})),
		"deleting service s1":            identity.DeleteService(ctx, sc, "s1"),
		"showing endpoint e1":            ignore(identity.GetEndpoint(ctx, sc, "e1")),
		"creating endpoint":              ignore(identity.CreateEndpoint(ctx, sc, endpoints.CreateOpts{Availability: gophercloud.AvailabilityPublic, ServiceID: "s1", URL: "https://example.com"})),
		"updating endpoint e1":           ignore(identity.UpdateEndpoint(ctx, sc, "e1", endpoints.UpdateOpts{URL: "https://example.com"})),
		"deleting endpoint e1":           identity.DeleteEndpoint(ctx, sc, "e1"),
		"introspecting token":            ignore(identity.IntrospectToken(ctx, sc, subject)),
		"revoking token":                 identity.RevokeToken(ctx, sc, subject),
	} {
		if err == nil || !strings.HasPrefix(err.Error(), want+": ") || !gophercloud.ResponseCodeIs(err, http.StatusNotFound) {
			t.Errorf("err = %v, want %q wrapping the 404", err, want)
		}
		if err != nil && strings.Contains(err.Error(), subject) {
			t.Errorf("err %q carries the subject token", err)
		}
	}
	if _, err := identity.TokenUser(ctx, sc, subject); err == nil || !strings.HasPrefix(err.Error(), "introspecting token: ") {
		t.Errorf("TokenUser err = %v", err)
	}
}

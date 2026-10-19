//go:build functional

package functional

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// The keystone suite: every identity noun driven through its lifecycle on a
// real Keystone, each write checked by reading the object back.

// in reports whether any row of a listing has want in any column.
func in(rows []map[string]any, want string) bool {
	for _, r := range rows {
		for k := range r {
			if field(r, k) == want {
				return true
			}
		}
	}
	return false
}

func TestIdentityDomainProjectUser(t *testing.T) {
	c := ft(t)
	r := defaultRunner(c)

	dom := uniq("dom")
	d := r.show(t, "domain", "create", dom, "--description", "functional")
	t.Cleanup(func() {
		r.run(t, "domain", "set", dom, "--disable") // keystone deletes only a disabled domain
		r.run(t, "domain", "delete", dom)
	})
	if field(d, "name") != dom || field(d, "enabled") != "true" {
		t.Fatalf("domain create = %v", d)
	}
	r.ok(t, "domain", "set", dom, "--description", "changed", "--disable")
	if d = r.show(t, "domain", "show", dom); field(d, "description") != "changed" || field(d, "enabled") != "false" {
		t.Errorf("after domain set: %v", d)
	}
	r.ok(t, "domain", "set", dom, "--enable")
	if rows := r.list(t, "domain", "list", "--name", dom); len(rows) != 1 {
		t.Errorf("domain list --name %s = %v", dom, rows)
	}

	proj := uniq("proj")
	p := r.show(t, "project", "create", proj, "--domain", dom, "--description", "functional")
	t.Cleanup(func() { r.run(t, "project", "delete", proj, "--domain", dom) })
	if field(p, "domain id") != field(d, "id") {
		t.Errorf("project domain = %v, want %s", p, field(d, "id"))
	}
	r.ok(t, "project", "set", proj, "--domain", dom, "--description", "changed", "--disable")
	if p = r.show(t, "project", "show", proj, "--domain", dom); field(p, "description") != "changed" || field(p, "enabled") != "false" {
		t.Errorf("after project set: %v", p)
	}
	r.ok(t, "project", "set", proj, "--domain", dom, "--enable")

	user, pw := uniq("user"), "Ft-"+uniq("pw")
	u := r.show(t, "user", "create", user, "--domain", dom, "--password", pw, "--project", proj, "--project-domain", dom)
	t.Cleanup(func() { r.run(t, "user", "delete", user, "--domain", dom) })
	if field(u, "default project id") != field(p, "id") {
		t.Errorf("user default project = %v, want %s", u, field(p, "id"))
	}
	r.ok(t, "user", "set", user, "--domain", dom, "--description", "changed")
	if u = r.show(t, "user", "show", user, "--domain", dom); field(u, "description") != "changed" {
		t.Errorf("after user set: %v", u)
	}
	if !slices.Contains(column(r.list(t, "user", "list", "--domain", dom), "name"), user) {
		t.Errorf("user list --domain %s does not list %s", dom, user)
	}

	// The user changes its own password, then authenticates with the new one.
	r.ok(t, "role", "add", "member", "--user", user, "--user-domain", dom, "--project", proj, "--project-domain", dom)
	as := func(password string) runner {
		return runner{env: append(baseEnv(),
			"OS_AUTH_URL="+openrcValue(c, "OS_AUTH_URL"), "OS_REGION_NAME="+openrcValue(c, "OS_REGION_NAME"),
			"OS_USERNAME="+user, "OS_PASSWORD="+password, "OS_USER_DOMAIN_NAME="+dom,
			"OS_PROJECT_NAME="+proj, "OS_PROJECT_DOMAIN_NAME="+dom)}
	}
	newPW := "Ft-" + uniq("pw2")
	as(pw).ok(t, "user", "password", "set", "--original-password", pw, "--password", newPW)
	// The change revokes the user's tokens by a timestamp with one-second
	// granularity, so a token issued in the same second is revoked with them.
	waitFor(t, "a token with the new password", 15*time.Second, func() bool {
		return as(newPW).run(t, "token", "issue").code == 0
	})
	if got := issue(t, as(newPW)); got.user != field(u, "id") {
		t.Errorf("token after password change = %+v, want user %s", got, field(u, "id"))
	}
	as(pw).fails(t, "token", "issue")
}

func TestIdentityRolesAndGroups(t *testing.T) {
	c := ft(t)
	r := defaultRunner(c)

	role := uniq("role")
	ro := r.show(t, "role", "create", role, "--description", "functional")
	t.Cleanup(func() { r.run(t, "role", "delete", role) })
	if field(ro, "name") != role {
		t.Fatalf("role create = %v", ro)
	}
	r.ok(t, "role", "set", role, "--description", "changed")
	if ro = r.show(t, "role", "show", role); field(ro, "description") != "changed" {
		t.Errorf("after role set: %v", ro)
	}
	if !slices.Contains(column(r.list(t, "role", "list"), "name"), role) {
		t.Errorf("role list does not list %s", role)
	}

	// An implied role: holding ours implies keystone's built-in "reader".
	r.ok(t, "implied", "role", "create", role, "--implied-role", "reader")
	if rows := r.list(t, "implied", "role", "list"); !in(rows, role) {
		t.Errorf("implied role list = %v, want %s → reader", rows, role)
	}
	r.ok(t, "implied", "role", "delete", role, "--implied-role", "reader")

	user := uniq("guser")
	userID := field(r.show(t, "user", "create", user, "--password", "Ft-"+uniq("pw")), "id")
	t.Cleanup(func() { r.run(t, "user", "delete", user) })

	grp := uniq("grp")
	g := r.show(t, "group", "create", grp, "--description", "functional")
	t.Cleanup(func() { r.run(t, "group", "delete", grp) })
	if field(g, "name") != grp {
		t.Fatalf("group create = %v", g)
	}
	r.ok(t, "group", "set", grp, "--description", "changed")
	if g = r.show(t, "group", "show", grp); field(g, "description") != "changed" {
		t.Errorf("after group set: %v", g)
	}
	r.fails(t, "group", "contains", "user", grp, user)
	r.ok(t, "group", "add", "user", grp, user)
	r.ok(t, "group", "contains", "user", grp, user)
	if !slices.Contains(column(r.list(t, "group", "list", "--user", user), "name"), grp) {
		t.Errorf("group list --user %s does not list %s", user, grp)
	}
	if !slices.Contains(column(r.list(t, "group", "list"), "name"), grp) {
		t.Errorf("group list does not list %s", grp)
	}

	// A role on a group reaches its members: assignment list shows it.
	r.ok(t, "role", "add", role, "--group", grp, "--project", "admin")
	rows := r.list(t, "role", "assignment", "list", "--group", grp, "--names")
	if !in(rows, role) {
		t.Errorf("role assignment list --group %s --names = %v, want %s", grp, rows, role)
	}
	if rows = r.list(t, "role", "assignment", "list", "--role", role); !in(rows, field(g, "id")) {
		t.Errorf("role assignment list --role %s = %v, want group %s", role, rows, field(g, "id"))
	}
	r.ok(t, "role", "remove", role, "--group", grp, "--project", "admin")
	if rows = r.list(t, "role", "assignment", "list", "--group", grp, "--names"); in(rows, role) {
		t.Errorf("after role remove, assignments = %v", rows)
	}

	r.ok(t, "group", "remove", "user", grp, user)
	r.fails(t, "group", "contains", "user", grp, user)

	// A system-scope grant, made and read back as the system admin keystone's
	// scoped policies ask for.
	sys := systemRunner(c)
	sys.ok(t, "role", "add", "reader", "--user", user, "--system", "all")
	t.Cleanup(func() { sys.run(t, "role", "remove", "reader", "--user", user, "--system", "all") })
	rows = sys.list(t, "role", "assignment", "list", "--user", user, "--system", "all", "--names")
	if len(rows) != 1 || field(rows[0], "system") != "all" || !strings.HasPrefix(field(rows[0], "role"), "reader") {
		t.Errorf("role assignment list --user %s --system all = %v, want reader on the system", user, rows)
	}
	if rows = sys.list(t, "role", "assignment", "list", "--role", "reader", "--system", "all"); !in(rows, userID) {
		t.Errorf("role assignment list --role reader --system all = %v, want user %s", rows, userID)
	}
	sys.ok(t, "role", "remove", "reader", "--user", user, "--system", "all")
	if rows = sys.list(t, "role", "assignment", "list", "--user", user, "--system", "all"); len(rows) != 0 {
		t.Errorf("after role remove --system all, assignments = %v", rows)
	}
}

// --password-prompt reads from a terminal only; from a script it refuses
// before anything reaches keystone.
func TestIdentityUserPasswordPrompt(t *testing.T) {
	c := ft(t)
	r := defaultRunner(c)
	user := uniq("puser")
	if stderr := r.withStdin("Ft-pw\nFt-pw\n").fails(t, "user", "create", user, "--password-prompt"); !strings.Contains(stderr, "needs an interactive terminal") {
		t.Errorf("user create --password-prompt without a terminal: %s", stderr)
	}
	if slices.Contains(column(r.list(t, "user", "list"), "name"), user) {
		r.run(t, "user", "delete", user)
		t.Errorf("a refused --password-prompt still created %s", user)
	}
}

// An application credential restricted by --access-rules carries the rules
// keystone stored, on create and on show.
func TestIdentityApplicationCredentialAccessRules(t *testing.T) {
	c := ft(t)
	r := defaultRunner(c)
	name := uniq("rules")
	rule := "GET compute /v2.1/servers"
	cr := r.show(t, "application", "credential", "create", name,
		"--access-rules", `[{"service":"compute","method":"GET","path":"/v2.1/servers"}]`)
	t.Cleanup(func() { r.run(t, "application", "credential", "delete", name) })
	if !strings.Contains(field(cr, "access rules"), rule) || field(cr, "secret") == "" {
		t.Fatalf("application credential create --access-rules = %v, want %q", cr, rule)
	}
	if got := r.show(t, "application", "credential", "show", name); !strings.Contains(field(got, "access rules"), rule) {
		t.Errorf("application credential show = %v, want access rule %q", got, rule)
	}
	r.fails(t, "application", "credential", "create", uniq("bad"), "--access-rules", `[{"service":"compute"}]`)
}

func TestIdentityCatalogObjects(t *testing.T) {
	c := ft(t)
	r := defaultRunner(c)

	region := uniq("region")
	rg := r.show(t, "region", "create", region, "--description", "functional")
	t.Cleanup(func() { r.run(t, "region", "delete", region) })
	if field(rg, "id") != region {
		t.Fatalf("region create = %v", rg)
	}
	r.ok(t, "region", "set", region, "--description", "changed")
	if rg = r.show(t, "region", "show", region); field(rg, "description") != "changed" {
		t.Errorf("after region set: %v", rg)
	}
	if !slices.Contains(column(r.list(t, "region", "list"), "id"), region) {
		t.Errorf("region list does not list %s", region)
	}

	typ, name := uniq("type"), uniq("svc")
	sv := r.show(t, "service", "create", typ, "--name", name, "--description", "functional")
	svcID := field(sv, "id")
	t.Cleanup(func() { r.run(t, "service", "delete", svcID) })
	r.ok(t, "service", "set", svcID, "--description", "changed", "--disable")
	if sv = r.show(t, "service", "show", svcID); field(sv, "description") != "changed" || field(sv, "enabled") != "false" {
		t.Errorf("after service set: %v", sv)
	}
	if !slices.Contains(column(r.list(t, "service", "list"), "id"), svcID) {
		t.Errorf("service list does not list %s", svcID)
	}

	ep := r.show(t, "endpoint", "create", svcID, "public", "http://192.0.2.10/ft", "--region", region)
	epID := field(ep, "id")
	t.Cleanup(func() { r.run(t, "endpoint", "delete", epID) })
	r.ok(t, "endpoint", "set", epID, "--url", "http://192.0.2.11/ft")
	if ep = r.show(t, "endpoint", "show", epID); field(ep, "url") != "http://192.0.2.11/ft" || field(ep, "region") != region {
		t.Errorf("after endpoint set: %v", ep)
	}
	r.ok(t, "endpoint", "delete", epID)
	r.ok(t, "service", "delete", svcID)
	r.ok(t, "region", "delete", region)

	// catalog show names the service's endpoints.
	cat := r.show(t, "catalog", "show", "identity")
	if field(cat, "type") != "identity" || !strings.Contains(field(cat, "endpoints"), "identity") {
		t.Errorf("catalog show identity = %v", cat)
	}
}

func TestIdentityTokenRevoke(t *testing.T) {
	c := ft(t)
	r := defaultRunner(c)
	var tok map[string]any
	r.json(t, &tok, "token", "issue")
	r.ok(t, "token", "revoke", field(tok, "id"))
	// The revoked token no longer validates; a fresh one still does.
	issue(t, r)
}

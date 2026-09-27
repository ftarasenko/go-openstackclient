//go:build functional

package functional

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

// The designate suite (bind9 backend): zones and recordsets through to ACTIVE
// on the backend, exports and imports, transfers to the demo project, shares,
// blacklists, TLDs, TSIG keys, PTR records on a floating IP, and the admin
// views (pools, services, limits, quotas).
//
// These run serially: a TLD, while it exists, restricts every zone create to
// it, so the TLD test must not overlap one that creates a zone.

const dnsTimeout = 3 * time.Minute

// zoneName is a unique zone under example.com. (RFC 2606), dot-terminated.
func zoneName(prefix string) string { return uniq(prefix) + ".example.com." }

// newZone creates a primary zone, waits for ACTIVE, and deletes it at the end.
func newZone(t *testing.T, r runner) (name, id string) {
	t.Helper()
	name = zoneName("zone")
	z := r.show(t, "zone", "create", name, "--email", "hostmaster@example.com", "--ttl", "3600")
	id = field(z, "id")
	t.Cleanup(func() { r.run(t, "zone", "delete", id) })
	zoneActive(t, r, id)
	return name, id
}

func zoneActive(t *testing.T, r runner, id string) {
	t.Helper()
	waitFor(t, "zone "+id+" to go ACTIVE", dnsTimeout, func() bool {
		st := field(r.show(t, "zone", "show", id), "status")
		if st == "ERROR" {
			t.Fatalf("zone %s went to ERROR", id)
		}
		return st == "ACTIVE"
	})
}

func TestDNSZonesAndRecordsets(t *testing.T) {
	c := ft(t)
	requireFeature(t, "dns")
	r := defaultRunner(c)
	name, id := newZone(t, r)

	r.ok(t, "zone", "set", id, "--description", "functional", "--ttl", "7200", "--email", "dns@example.com")
	zoneActive(t, r, id)
	if z := r.show(t, "zone", "show", id); field(z, "description") != "functional" || field(z, "ttl") != "7200" {
		t.Errorf("after zone set: %v", z)
	}
	if rows := r.list(t, "zone", "list", "--name", name); len(rows) != 1 {
		t.Errorf("zone list --name = %v", rows)
	}
	if ns := r.list(t, "zone", "nameservers", "list", id); len(ns) == 0 {
		t.Error("zone nameservers list is empty")
	}

	www := "www." + name
	rs := r.show(t, "recordset", "create", id, www, "--type", "A", "--record", "192.0.2.10", "--ttl", "300")
	rsID := field(rs, "id")
	r.ok(t, "recordset", "set", id, rsID, "--record", "192.0.2.11", "--record", "192.0.2.12",
		"--ttl", "600", "--description", "functional")
	waitFor(t, "the recordset to go ACTIVE", dnsTimeout, func() bool {
		return field(r.show(t, "recordset", "show", id, rsID), "status") == "ACTIVE"
	})
	rs = r.show(t, "recordset", "show", id, rsID)
	if recs := field(rs, "records"); !strings.Contains(recs, "192.0.2.12") || strings.Contains(recs, "192.0.2.10") || field(rs, "ttl") != "600" {
		t.Errorf("after recordset set: %v", rs)
	}
	if rows := r.list(t, "recordset", "list", id, "--type", "A"); !in(rows, rsID) {
		t.Errorf("recordset list --type A = %v", rows)
	}
	r.ok(t, "recordset", "delete", id, rsID)
	waitFor(t, "the recordset to go", dnsTimeout, func() bool { return r.run(t, "recordset", "show", id, rsID).code != 0 })
}

// A secondary zone is what AXFR is for; its master is never reachable, so the
// transfer fails asynchronously and only the request is asserted.
func TestDNSSecondaryAndAbandon(t *testing.T) {
	c := ft(t)
	requireFeature(t, "dns")
	r := defaultRunner(c)

	sec := r.show(t, "zone", "create", zoneName("secondary"), "--type", "SECONDARY", "--master", "192.0.2.1")
	secID := field(sec, "id")
	t.Cleanup(func() { r.run(t, "zone", "delete", secID) })
	if field(sec, "type") != "SECONDARY" {
		t.Errorf("zone create --type SECONDARY = %v", sec)
	}
	r.ok(t, "zone", "axfr", secID)

	// Abandon forgets a zone in designate and leaves the backend alone.
	_, id := newZone(t, r)
	r.ok(t, "zone", "abandon", id)
	r.fails(t, "zone", "show", id)
}

func TestDNSExportAndImport(t *testing.T) {
	c := ft(t)
	requireFeature(t, "dns")
	r := defaultRunner(c)
	name, id := newZone(t, r)

	ex := r.show(t, "zone", "export", "create", id)
	exID := field(ex, "id")
	t.Cleanup(func() { r.run(t, "zone", "export", "delete", exID) })
	waitFor(t, "the export to complete", dnsTimeout, func() bool {
		return field(r.show(t, "zone", "export", "show", exID), "status") == "COMPLETE"
	})
	if file := r.ok(t, "zone", "export", "showfile", exID); !strings.Contains(file, "SOA") || !strings.Contains(file, name) {
		t.Errorf("zone export showfile = %s", file)
	}
	if !in(r.list(t, "zone", "export", "list", "--zone-id", id), exID) {
		t.Errorf("zone export list does not list %s", exID)
	}
	r.ok(t, "zone", "export", "delete", exID)

	// Import a zone file of our own writing.
	imported := zoneName("imported")
	file := writeFile(t, t.TempDir(), "zone.txt", "$ORIGIN "+imported+"\n$TTL 300\n"+
		imported+" IN SOA ns1.example.com. hostmaster.example.com. 1 3600 600 86400 300\n"+
		imported+" IN NS ns1.example.com.\n"+
		"www."+imported+" IN A 192.0.2.20\n")
	im := r.show(t, "zone", "import", "create", file)
	imID := field(im, "id")
	t.Cleanup(func() { r.run(t, "zone", "import", "delete", imID) })
	var zoneID string
	waitFor(t, "the import to complete", dnsTimeout, func() bool {
		got := r.show(t, "zone", "import", "show", imID)
		if field(got, "status") == "ERROR" {
			t.Fatalf("zone import failed: %v", got)
		}
		zoneID = field(got, "zone_id")
		return field(got, "status") == "COMPLETE"
	})
	t.Cleanup(func() { r.run(t, "zone", "delete", zoneID) })
	if !in(r.list(t, "zone", "import", "list"), imID) {
		t.Errorf("zone import list does not list %s", imID)
	}
	if rows := r.list(t, "recordset", "list", zoneID, "--name", "www."+imported); len(rows) != 1 {
		t.Errorf("imported zone's www recordset = %v", rows)
	}
	r.ok(t, "zone", "import", "delete", imID)
}

// admin offers a zone to demo, demo accepts it with the key.
func TestDNSTransfer(t *testing.T) {
	c := ft(t)
	requireFeature(t, "dns")
	r, demo := defaultRunner(c), demoRunner(c)
	demoID := issue(t, demo).project

	// One request withdrawn.
	_, other := newZone(t, r)
	w := r.show(t, "zone", "transfer", "request", "create", other, "--target-project", demoID)
	r.ok(t, "zone", "transfer", "request", "delete", field(w, "id"))

	_, id := newZone(t, r)
	req := r.show(t, "zone", "transfer", "request", "create", id, "--target-project", demoID, "--description", "functional")
	reqID, key := field(req, "id"), field(req, "key")
	t.Cleanup(func() { r.run(t, "zone", "transfer", "request", "delete", reqID) })
	r.ok(t, "zone", "transfer", "request", "set", reqID, "--description", "changed")
	if got := r.show(t, "zone", "transfer", "request", "show", reqID); field(got, "description") != "changed" {
		t.Errorf("transfer request show after set = %v", got)
	}
	if !in(r.list(t, "zone", "transfer", "request", "list"), reqID) {
		t.Errorf("transfer request list does not list %s", reqID)
	}

	acc := demo.show(t, "zone", "transfer", "accept", "request", "--transfer-id", reqID, "--key", key)
	accID := field(acc, "id")
	t.Cleanup(func() { demo.run(t, "zone", "delete", id) })
	// An accept belongs to the project that made it; the offering admin's
	// project-scoped token does not see it.
	waitFor(t, "the transfer to complete", dnsTimeout, func() bool {
		return field(demo.show(t, "zone", "transfer", "accept", "show", accID), "status") == "COMPLETE"
	})
	// Listing accepts is an administrator's call (find_zone_transfer_accepts):
	// the accepting project itself is refused, and the admin sees demo's accept
	// only across projects.
	if msg := demo.fails(t, "zone", "transfer", "accept", "list"); !strings.Contains(msg, "403") {
		t.Errorf("zone transfer accept list as demo: %q, want designate's 403", msg)
	}
	if !in(r.list(t, "zone", "transfer", "accept", "list", "--all-projects"), accID) {
		t.Errorf("transfer accept list --all-projects does not list %s", accID)
	}
	if z := demo.show(t, "zone", "show", id); field(z, "project_id") != demoID {
		t.Errorf("zone after the transfer = %v, want project %s", z, demoID)
	}
}

func TestDNSSharesAndMoves(t *testing.T) {
	c := ft(t)
	requireFeature(t, "dns")
	// Shared zones and pool moves are 2023.2 designate; it advertises neither.
	requireSeries(t, "2023.2")
	r := defaultRunner(c)
	demoID := issue(t, demoRunner(c)).project
	_, id := newZone(t, r)

	sh := r.show(t, "zone", "share", "create", id, demoID)
	shID := field(sh, "id")
	t.Cleanup(func() { r.run(t, "zone", "share", "delete", id, shID) })
	if got := r.show(t, "zone", "share", "show", id, shID); field(got, "target_project_id") != demoID {
		t.Errorf("zone share show = %v", got)
	}
	if !in(r.list(t, "zone", "share", "list", id), shID) {
		t.Errorf("zone share list does not list %s", shID)
	}
	r.ok(t, "zone", "share", "delete", id, shID)

	// One pool: designate either moves the zone onto the pool it is in, or
	// refuses; both are its answer to a request koc made.
	res := r.run(t, "zone", "move", id)
	if res.code != 0 && strings.TrimSpace(res.stderr) == "" {
		t.Errorf("zone move failed with no message (exit %d)", res.code)
	}
	t.Logf("zone move: exit %d %s", res.code, strings.TrimSpace(res.stderr))
}

func TestDNSBlacklistsAndTLDs(t *testing.T) {
	c := ft(t)
	requireFeature(t, "dns")
	r := defaultRunner(c)

	bl := r.show(t, "zone", "blacklist", "create", "--pattern", `^ft-blocked\..*`, "--description", "functional")
	blID := field(bl, "id")
	t.Cleanup(func() { r.run(t, "zone", "blacklist", "delete", blID) })
	r.ok(t, "zone", "blacklist", "set", blID, "--pattern", `^ft-blocked-too\..*`, "--no-description")
	if got := r.show(t, "zone", "blacklist", "show", blID); field(got, "pattern") != `^ft-blocked-too\..*` || field(got, "description") != "" {
		t.Errorf("after blacklist set: %v", got)
	}
	if !in(r.list(t, "zone", "blacklist", "list"), blID) {
		t.Errorf("zone blacklist list does not list %s", blID)
	}
	r.ok(t, "zone", "blacklist", "delete", blID)

	// While any TLD exists every zone must sit under one; this one lives for
	// the few calls below.
	tld := strings.ReplaceAll(uniq("tld"), "-", "")
	tl := r.show(t, "tld", "create", "--name", tld, "--description", "functional")
	tlID := field(tl, "id")
	t.Cleanup(func() { r.run(t, "tld", "delete", tlID) })
	r.ok(t, "tld", "set", tlID, "--description", "changed")
	if got := r.show(t, "tld", "show", tlID); field(got, "description") != "changed" {
		t.Errorf("after tld set: %v", got)
	}
	if !in(r.list(t, "tld", "list", "--name", tld), tlID) {
		t.Errorf("tld list --name does not list %s", tlID)
	}
	r.ok(t, "tld", "delete", tlID)
	r.fails(t, "tld", "show", tlID)
}

func TestDNSTsigkeys(t *testing.T) {
	c := ft(t)
	requireFeature(t, "dns")
	r := defaultRunner(c)
	pools := r.list(t, "dns", "pool", "list")
	if len(pools) == 0 {
		t.Fatal("dns pool list is empty")
	}
	pool := field(pools[0], "id")
	if p := r.show(t, "dns", "pool", "show", pool); field(p, "name") == "" {
		t.Errorf("dns pool show = %v", p)
	}

	name, secret := uniq("tsig"), base64.StdEncoding.EncodeToString([]byte("koc-functional-secret"))
	k := r.show(t, "tsigkey", "create", "--name", name, "--algorithm", "hmac-sha256", "--secret", secret,
		"--scope", "POOL", "--resource-id", pool)
	id := field(k, "id")
	t.Cleanup(func() { r.run(t, "tsigkey", "delete", id) })
	r.ok(t, "tsigkey", "set", id, "--name", name+"-renamed", "--algorithm", "hmac-sha512")
	got := r.show(t, "tsigkey", "show", id, "--show-secret")
	if field(got, "name") != name+"-renamed" || field(got, "algorithm") != "hmac-sha512" || field(got, "secret") != secret {
		t.Errorf("tsigkey show --show-secret after set = %v", got)
	}
	if field(r.show(t, "tsigkey", "show", id), "secret") != "" {
		t.Error("tsigkey show prints the secret without --show-secret")
	}
	if !in(r.list(t, "tsigkey", "list", "--scope", "POOL"), id) {
		t.Errorf("tsigkey list does not list %s", id)
	}
	r.ok(t, "tsigkey", "delete", id)
}

// PTR records live on floating IPs, addressed as <region>:<floating-ip-id>.
func TestDNSPtrRecords(t *testing.T) {
	c := ft(t)
	requireFeature(t, "dns")
	requireFeature(t, "net")
	r := defaultRunner(c)

	fip := r.show(t, "floating", "ip", "create", "public")
	fipID := field(fip, "id")
	t.Cleanup(func() { r.run(t, "floating", "ip", "delete", fipID) })
	ref := openrcValue(c, "OS_REGION_NAME") + ":" + fipID
	ptr := "ft-" + strings.ReplaceAll(fipID[:8], "-", "") + ".example.com."

	r.ok(t, "ptr", "record", "set", ref, ptr, "--ttl", "600", "--description", "functional")
	t.Cleanup(func() { r.run(t, "ptr", "record", "unset", ref) })
	waitFor(t, "the PTR record", dnsTimeout, func() bool {
		got := r.show(t, "ptr", "record", "show", ref)
		return field(got, "ptrdname") == ptr && field(got, "status") == "ACTIVE"
	})
	if !in(r.list(t, "ptr", "record", "list"), ptr) {
		t.Errorf("ptr record list does not list %s", ptr)
	}
	r.ok(t, "ptr", "record", "unset", ref)
	waitFor(t, "the PTR record to go", dnsTimeout, func() bool {
		return field(r.show(t, "ptr", "record", "show", ref), "ptrdname") == ""
	})
}

func TestDNSAdminViews(t *testing.T) {
	c := ft(t)
	requireFeature(t, "dns")
	r := defaultRunner(c)

	svcs := r.list(t, "dns", "service", "list")
	if !in(svcs, "central") {
		t.Errorf("dns service list = %v, want central", svcs)
	}
	if s := r.show(t, "dns", "service", "show", field(svcs[0], "id")); field(s, "service_name") == "" {
		t.Errorf("dns service show = %v", s)
	}
	if l := r.list(t, "dns", "limit", "list"); !in(l, "max_zones") {
		t.Errorf("dns limit list = %v, want max_zones", l)
	}

	proj := r.show(t, "project", "create", uniq("dnsquota"))
	pid := field(proj, "id")
	t.Cleanup(func() { r.run(t, "project", "delete", pid) })
	// Who may write another project's quotas is designate policy, and it has
	// moved: any admin on zed (the deprecated rules still apply), a
	// system-scoped admin once 2025.1 enforces the new defaults, and a project
	// admin again once designate dropped system scope (2026.2), which refuses
	// a system token. Upstream's client is refused alike, so the test takes the
	// token the cloud accepts: project scope first, then system scope, which
	// designate serves only with --all-projects.
	system := false
	if res := r.run(t, "dns", "quota", "set", pid, "--zones", "3", "--zone-recordsets", "40"); res.code != 0 {
		t.Logf("project-scoped dns quota set refused; using a system-scoped token: %s", strings.TrimSpace(res.stderr))
		system = true
		r.ok(t, "--os-system-scope", "all", "dns", "quota", "set", pid, "--zones", "3", "--zone-recordsets", "40", "--all-projects")
	}
	quotas := func() map[string]any {
		t.Helper()
		if system {
			return r.show(t, "--os-system-scope", "all", "dns", "quota", "list", pid, "--all-projects")
		}
		return r.show(t, "dns", "quota", "list", pid)
	}
	if q := quotas(); field(q, "zones") != "3" || field(q, "zone_recordsets") != "40" {
		t.Errorf("dns quota list after set = %v", q)
	}
	if system {
		r.ok(t, "--os-system-scope", "all", "dns", "quota", "reset", pid, "--all-projects")
	} else {
		r.ok(t, "dns", "quota", "reset", pid)
	}
	if q := quotas(); field(q, "zones") == "3" {
		t.Errorf("dns quota list after reset = %v, want the default", q)
	}
}

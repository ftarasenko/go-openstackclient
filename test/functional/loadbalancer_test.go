//go:build functional

package functional

import (
	"strings"
	"testing"
	"time"
)

// The octavia suite, against up.sh's "lb" feature: octavia with its no-op
// amphora, compute, network and image drivers, as upstream's
// octavia-v2-dsvm-noop-api job runs it. Every object walks the real API and
// the controller worker's flows to ACTIVE, amphora records included, but there
// is no amphora VM — so these tests assert the API, never traffic.
//
// Octavia makes a load balancer immutable (PENDING_UPDATE) while it applies a
// change to it or any child, and answers a second write with 409 until it is
// ACTIVE again. lbActive is the wait between writes.

const lbTimeout = 3 * time.Minute

func lbActive(t *testing.T, r runner, id string) {
	t.Helper()
	waitFor(t, "load balancer "+id+" to go ACTIVE", lbTimeout, func() bool {
		st := field(r.show(t, "loadbalancer", "show", id), "provisioning_status")
		if st == "ERROR" {
			t.Fatalf("load balancer %s went to ERROR", id)
		}
		return st == "ACTIVE"
	})
}

// newLoadBalancer creates a load balancer on a network of the test's own,
// waits for ACTIVE and deletes it, children and all, at the end.
func newLoadBalancer(t *testing.T, r runner) (name, id, subnetID string) {
	t.Helper()
	f := newNetwork(t, r, "203.0.113.0/24")
	name = uniq("lb")
	lb := r.show(t, "loadbalancer", "create", "--name", name, "--vip-subnet-id", f.subnetID,
		"--description", "koc functional", "--tag", "ft", "--wait")
	id = field(lb, "id")
	// Registered after newNetwork's cleanups, so it runs before them: the
	// VIP port has to go before its subnet can.
	t.Cleanup(func() { r.run(t, "loadbalancer", "delete", "--cascade", "--wait", id) })
	if field(lb, "provisioning_status") != "ACTIVE" || field(lb, "vip_subnet_id") != f.subnetID {
		t.Errorf("loadbalancer create --wait = %v", lb)
	}
	return name, id, f.subnetID
}

func TestLoadBalancer(t *testing.T) {
	c := ft(t)
	requireFeature(t, "lb")
	r := defaultRunner(c)
	name, id, subnetID := newLoadBalancer(t, r)

	if rows := r.list(t, "loadbalancer", "list", "--name", name, "--long"); !in(rows, id) {
		t.Errorf("loadbalancer list --name = %v", rows)
	}
	if rows := r.list(t, "loadbalancer", "list", "--vip-subnet-id", subnetID, "--provisioning-status", "ACTIVE"); len(rows) != 1 {
		t.Errorf("loadbalancer list --vip-subnet-id = %v", rows)
	}

	renamed := name + "-2"
	r.ok(t, "loadbalancer", "set", id, "--name", renamed, "--description", "functional 2", "--tag", "ft-b")
	lbActive(t, r, id)
	got := r.show(t, "loadbalancer", "show", renamed)
	if field(got, "description") != "functional 2" || !strings.Contains(field(got, "tags"), "ft-b") {
		t.Errorf("after loadbalancer set: %v", got)
	}

	r.show(t, "loadbalancer", "stats", "show", id)
	if tree := r.list(t, "loadbalancer", "status", "show", id); !in(tree, id) {
		t.Errorf("loadbalancer status show = %v", tree)
	}

	r.ok(t, "loadbalancer", "failover", id, "--wait")
	lbActive(t, r, id)

	r.ok(t, "loadbalancer", "delete", "--cascade", "--wait", id)
	r.fails(t, "loadbalancer", "show", id)
}

// TestLoadBalancerChildren walks a listener, pool, member, health monitor,
// L7 policy and L7 rule through create, list, set, show and delete.
func TestLoadBalancerChildren(t *testing.T) {
	c := ft(t)
	requireFeature(t, "lb")
	r := defaultRunner(c)
	_, lbID, subnetID := newLoadBalancer(t, r)

	// listener
	lsnName := uniq("lsn")
	lsn := r.show(t, "loadbalancer", "listener", "create", "--name", lsnName, "--loadbalancer", lbID,
		"--protocol", "HTTP", "--protocol-port", "80", "--connection-limit", "100", "--tag", "ft")
	lsnID := field(lsn, "id")
	lbActive(t, r, lbID)
	if rows := r.list(t, "loadbalancer", "listener", "list", "--loadbalancer", lbID, "--long"); !in(rows, lsnID) {
		t.Errorf("listener list --loadbalancer = %v", rows)
	}
	r.ok(t, "loadbalancer", "listener", "set", lsnID, "--description", "functional",
		"--connection-limit", "200", "--timeout-client-data", "30000")
	lbActive(t, r, lbID)
	if got := r.show(t, "loadbalancer", "listener", "show", lsnName); field(got, "connection_limit") != "200" ||
		field(got, "description") != "functional" || field(got, "timeout_client_data") != "30000" {
		t.Errorf("after listener set: %v", got)
	}

	// pool
	poolName := uniq("pool")
	pool := r.show(t, "loadbalancer", "pool", "create", "--name", poolName, "--listener", lsnID,
		"--protocol", "HTTP", "--lb-algorithm", "ROUND_ROBIN", "--session-persistence", "type=SOURCE_IP")
	poolID := field(pool, "id")
	lbActive(t, r, lbID)
	if rows := r.list(t, "loadbalancer", "pool", "list", "--loadbalancer", lbID, "--long"); !in(rows, poolID) {
		t.Errorf("pool list --loadbalancer = %v", rows)
	}
	r.ok(t, "loadbalancer", "pool", "set", poolID, "--lb-algorithm", "LEAST_CONNECTIONS", "--description", "functional")
	lbActive(t, r, lbID)
	if got := r.show(t, "loadbalancer", "pool", "show", poolName); field(got, "lb_algorithm") != "LEAST_CONNECTIONS" ||
		field(got, "description") != "functional" {
		t.Errorf("after pool set: %v", got)
	}

	// member
	mem := r.show(t, "loadbalancer", "member", "create", poolID, "--name", uniq("mem"),
		"--address", "203.0.113.10", "--protocol-port", "8080", "--subnet-id", subnetID, "--weight", "5")
	memID := field(mem, "id")
	lbActive(t, r, lbID)
	if rows := r.list(t, "loadbalancer", "member", "list", poolID, "--long"); !in(rows, memID) {
		t.Errorf("member list = %v", rows)
	}
	r.ok(t, "loadbalancer", "member", "set", poolID, memID, "--weight", "7", "--monitor-port", "9090")
	lbActive(t, r, lbID)
	if got := r.show(t, "loadbalancer", "member", "show", poolID, memID); field(got, "weight") != "7" ||
		field(got, "monitor_port") != "9090" {
		t.Errorf("after member set: %v", got)
	}

	// health monitor
	hm := r.show(t, "loadbalancer", "healthmonitor", "create", poolID, "--name", uniq("hm"), "--type", "HTTP",
		"--delay", "5", "--timeout", "3", "--max-retries", "2", "--url-path", "/healthz")
	hmID := field(hm, "id")
	lbActive(t, r, lbID)
	if rows := r.list(t, "loadbalancer", "healthmonitor", "list", "--pool", poolID, "--long"); !in(rows, hmID) {
		t.Errorf("healthmonitor list --pool = %v", rows)
	}
	r.ok(t, "loadbalancer", "healthmonitor", "set", hmID, "--delay", "10", "--expected-codes", "200-204")
	lbActive(t, r, lbID)
	if got := r.show(t, "loadbalancer", "healthmonitor", "show", hmID); field(got, "delay") != "10" ||
		field(got, "expected_codes") != "200-204" {
		t.Errorf("after healthmonitor set: %v", got)
	}

	// L7 policy and rule
	pol := r.show(t, "loadbalancer", "l7policy", "create", "--name", uniq("l7p"), "--listener", lsnID,
		"--action", "REDIRECT_TO_URL", "--redirect-url", "http://www.example.com/", "--position", "1")
	polID := field(pol, "id")
	lbActive(t, r, lbID)
	if rows := r.list(t, "loadbalancer", "l7policy", "list", "--listener", lsnID, "--long"); !in(rows, polID) {
		t.Errorf("l7policy list --listener = %v", rows)
	}
	r.ok(t, "loadbalancer", "l7policy", "set", polID, "--action", "REDIRECT_TO_POOL", "--redirect-pool", poolID)
	lbActive(t, r, lbID)
	if got := r.show(t, "loadbalancer", "l7policy", "show", polID); field(got, "action") != "REDIRECT_TO_POOL" ||
		field(got, "redirect_pool_id") != poolID {
		t.Errorf("after l7policy set: %v", got)
	}

	rule := r.show(t, "loadbalancer", "l7rule", "create", polID, "--type", "PATH",
		"--compare-type", "STARTS_WITH", "--value", "/api")
	ruleID := field(rule, "id")
	lbActive(t, r, lbID)
	if rows := r.list(t, "loadbalancer", "l7rule", "list", polID, "--long"); !in(rows, ruleID) {
		t.Errorf("l7rule list = %v", rows)
	}
	r.ok(t, "loadbalancer", "l7rule", "set", polID, ruleID, "--value", "/v2", "--invert")
	lbActive(t, r, lbID)
	if got := r.show(t, "loadbalancer", "l7rule", "show", polID, ruleID); field(got, "value") != "/v2" ||
		field(got, "invert") != "true" {
		t.Errorf("after l7rule set: %v", got)
	}

	// Children go leaf-first; each delete makes the load balancer immutable.
	r.ok(t, "loadbalancer", "l7rule", "delete", polID, ruleID)
	lbActive(t, r, lbID)
	r.ok(t, "loadbalancer", "l7policy", "delete", polID)
	lbActive(t, r, lbID)
	r.fails(t, "loadbalancer", "l7policy", "show", polID)
	r.ok(t, "loadbalancer", "healthmonitor", "delete", hmID)
	lbActive(t, r, lbID)
	r.ok(t, "loadbalancer", "member", "delete", poolID, memID)
	lbActive(t, r, lbID)
	r.ok(t, "loadbalancer", "pool", "delete", poolID)
	lbActive(t, r, lbID)
	r.ok(t, "loadbalancer", "listener", "delete", lsnID)
	lbActive(t, r, lbID)
	r.fails(t, "loadbalancer", "listener", "show", lsnID)
}

// TestLoadBalancerAmphorae drives the admin amphora API. The no-op compute
// driver still records an amphora per load balancer (SINGLE topology: one,
// STANDALONE), so list, show, configure, stats and failover run as on a real
// cloud. Delete is the exception: octavia deletes only a READY or ERROR
// amphora, and a no-op one is ALLOCATED for its load balancer's whole life, so
// the test asserts octavia's refusal — which proves koc sent the request.
func TestLoadBalancerAmphorae(t *testing.T) {
	c := ft(t)
	requireFeature(t, "lb")
	r := defaultRunner(c)
	_, lbID, _ := newLoadBalancer(t, r)

	amphorae := func() []map[string]any {
		return r.list(t, "loadbalancer", "amphora", "list", "--loadbalancer", lbID, "--long")
	}
	rows := amphorae()
	if len(rows) != 1 || field(rows[0], "role") != "STANDALONE" {
		t.Fatalf("amphora list --loadbalancer = %v", rows)
	}
	ampID := field(rows[0], "id")
	if got := r.show(t, "loadbalancer", "amphora", "show", ampID); field(got, "loadbalancer_id") != lbID ||
		field(got, "status") != "ALLOCATED" {
		t.Errorf("amphora show = %v", got)
	}
	if rows := r.list(t, "loadbalancer", "amphora", "list", "--status", "ALLOCATED", "--role", "STANDALONE"); !in(rows, ampID) {
		t.Errorf("amphora list --status --role = %v", rows)
	}

	// Octavia answers 404 until the health manager has stored a sample, and a
	// no-op amphora never reports one.
	if res := r.run(t, "loadbalancer", "amphora", "stats", "show", ampID, "-f", "json"); res.code != 0 &&
		!strings.Contains(strings.ToLower(res.stderr), "not found") {
		t.Errorf("amphora stats show: exit %d: %s", res.code, res.stderr)
	}

	r.ok(t, "loadbalancer", "amphora", "configure", ampID)

	stderr := r.fails(t, "loadbalancer", "amphora", "delete", ampID)
	if !strings.Contains(stderr, "409") && !strings.Contains(strings.ToLower(stderr), "immutable") {
		t.Errorf("amphora delete of an ALLOCATED amphora: %s, want octavia's 409", stderr)
	}

	// Failover replaces the amphora: a new ID, the old one gone from the list.
	r.ok(t, "loadbalancer", "amphora", "failover", ampID)
	waitFor(t, "the amphora to be replaced", lbTimeout, func() bool {
		rows := amphorae()
		return len(rows) == 1 && field(rows[0], "id") != ampID && field(rows[0], "status") == "ALLOCATED"
	})
	lbActive(t, r, lbID)
}

func TestLoadBalancerFlavors(t *testing.T) {
	c := ft(t)
	requireFeature(t, "lb")
	r := defaultRunner(c)

	if rows := r.list(t, "loadbalancer", "provider", "list"); !in(rows, "amphora") {
		t.Errorf("provider list has no amphora: %v", rows)
	}
	if caps := r.list(t, "loadbalancer", "provider", "capability", "list", "amphora"); !in(caps, "loadbalancer_topology") {
		t.Errorf("provider capability list amphora = %v", caps)
	}

	fpName := uniq("fp")
	fp := r.show(t, "loadbalancer", "flavorprofile", "create", "--name", fpName, "--provider", "amphora",
		"--flavor-data", `{"loadbalancer_topology": "SINGLE"}`)
	fpID := field(fp, "id")
	t.Cleanup(func() { r.run(t, "loadbalancer", "flavorprofile", "delete", fpID) })
	if rows := r.list(t, "loadbalancer", "flavorprofile", "list", "--name", fpName); !in(rows, fpID) {
		t.Errorf("flavorprofile list --name = %v", rows)
	}
	r.ok(t, "loadbalancer", "flavorprofile", "set", fpID, "--flavor-data", `{"loadbalancer_topology": "ACTIVE_STANDBY"}`)
	if got := r.show(t, "loadbalancer", "flavorprofile", "show", fpName); !strings.Contains(field(got, "flavor_data"), "ACTIVE_STANDBY") {
		t.Errorf("after flavorprofile set: %v", got)
	}

	fvName := uniq("fv")
	fv := r.show(t, "loadbalancer", "flavor", "create", "--name", fvName, "--flavor-profile", fpName,
		"--description", "koc functional")
	fvID := field(fv, "id")
	t.Cleanup(func() { r.run(t, "loadbalancer", "flavor", "delete", fvID) })
	if rows := r.list(t, "loadbalancer", "flavor", "list", "--name", fvName); !in(rows, fvID) {
		t.Errorf("flavor list --name = %v", rows)
	}
	r.ok(t, "loadbalancer", "flavor", "set", fvID, "--disable", "--description", "functional 2")
	if got := r.show(t, "loadbalancer", "flavor", "show", fvName); field(got, "enabled") != "false" ||
		field(got, "description") != "functional 2" {
		t.Errorf("after flavor set --disable: %v", got)
	}

	r.ok(t, "loadbalancer", "flavor", "delete", fvID)
	r.fails(t, "loadbalancer", "flavor", "show", fvID)
	r.ok(t, "loadbalancer", "flavorprofile", "delete", fpID)
	r.fails(t, "loadbalancer", "flavorprofile", "show", fpID)
}

// TestLoadBalancerQuotas sets, unsets and resets octavia quotas on devstack's
// demo project, which no other test creates load balancers in.
func TestLoadBalancerQuotas(t *testing.T) {
	c := ft(t)
	requireFeature(t, "lb")
	r := defaultRunner(c)
	t.Cleanup(func() { r.run(t, "loadbalancer", "quota", "reset", "demo") })

	r.show(t, "loadbalancer", "quota", "defaults", "show")

	r.ok(t, "loadbalancer", "quota", "set", "demo", "--loadbalancer", "3", "--pool", "7", "--member", "-1")
	got := r.show(t, "loadbalancer", "quota", "show", "demo")
	if field(got, "loadbalancer") != "3" || field(got, "pool") != "7" || field(got, "member") != "-1" {
		t.Errorf("after quota set: %v", got)
	}
	if rows := r.list(t, "loadbalancer", "quota", "list", "--project", "demo"); len(rows) != 1 {
		t.Errorf("quota list --project demo = %v", rows)
	}

	// unset clears only the quotas named: the pool quota falls back to the
	// default, the load balancer quota stays.
	r.ok(t, "loadbalancer", "quota", "unset", "demo", "--pool")
	got = r.show(t, "loadbalancer", "quota", "show", "demo")
	if field(got, "pool") == "7" || field(got, "loadbalancer") != "3" {
		t.Errorf("after quota unset --pool: %v", got)
	}

	r.ok(t, "loadbalancer", "quota", "reset", "demo")
	if got := r.show(t, "loadbalancer", "quota", "show", "demo"); field(got, "loadbalancer") == "3" {
		t.Errorf("after quota reset: %v", got)
	}
}

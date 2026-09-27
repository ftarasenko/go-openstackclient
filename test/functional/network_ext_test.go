//go:build functional

package functional

import (
	"slices"
	"strings"
	"testing"
)

// The neutron extension nouns that devstack's "net" feature switches on (qos,
// trunk, segments) and the ones neutron always loads (agents, RBAC, address
// scopes and groups, subnet pools), plus the discovery verbs. Each test gates
// on the extension alias the cloud advertises, not on the series.

func TestNetworkDiscovery(t *testing.T) {
	c := ft(t)
	r := defaultRunner(c)
	if e := r.show(t, "network", "extension", "show", "router"); field(e, "alias") != "router" {
		t.Errorf("network extension show router = %v", e)
	}
	r.fails(t, "network", "extension", "show", "ft-no-such-extension")
	// Only the service plugins with drivers (L3 router flavors, vpnaas) list
	// providers; an empty list is still a working call.
	r.ok(t, "network", "service", "provider", "list")
}

func TestNetworkAgents(t *testing.T) {
	c := ft(t)
	requireExtension(t, "agent")
	r := defaultRunner(c)

	agents := r.list(t, "network", "agent", "list", "--long")
	if len(agents) == 0 {
		t.Fatal("network agent list is empty")
	}
	first := field(agents[0], "id")
	a := r.show(t, "network", "agent", "show", first)
	if field(a, "host") == "" {
		t.Errorf("network agent show = %v", a)
	}
	r.ok(t, "network", "agent", "set", first, "--description", "koc functional")
	t.Cleanup(func() { r.run(t, "network", "agent", "set", first, "--description", "") })
	if field(r.show(t, "network", "agent", "show", first), "description") != "koc functional" {
		t.Error("network agent set --description did not stick")
	}
	r.ok(t, "network", "agent", "set", first, "--enable")
	r.fails(t, "network", "agent", "delete", "8f4b5c6d-7e8f-4a0b-9c1d-2e3f4a5b6c7d")

	f := newNetwork(t, r, "203.0.113.0/24")
	router := uniq("agentrouter")
	r.ok(t, "router", "create", router)
	t.Cleanup(func() { r.run(t, "router", "delete", router) })

	agentOf := func(kind string) string {
		for _, ag := range agents {
			if strings.Contains(strings.ToLower(anyField(ag, "agent_type", "Agent Type")), kind) {
				return field(ag, "id")
			}
		}
		return ""
	}
	dhcp, l3 := agentOf("dhcp"), agentOf("l3")

	// ML2/OVS schedules networks and routers on agents; ML2/OVN has neither
	// agent, and asking an OVN agent to host a network is refused.
	if c.Backend == "ovs" {
		if dhcp == "" || l3 == "" {
			t.Fatalf("ML2/OVS with no DHCP (%q) or L3 (%q) agent: %v", dhcp, l3, agents)
		}
		r.ok(t, "network", "agent", "remove", "network", "--dhcp", dhcp, f.net)
		r.ok(t, "network", "agent", "add", "network", "--dhcp", dhcp, f.net)
		if rows := r.list(t, "network", "agent", "list", "--network", f.net); !in(rows, dhcp) {
			t.Errorf("network agent list --network = %v, want %s", rows, dhcp)
		}
		r.run(t, "network", "agent", "remove", "router", "--l3", l3, router) // it may not be scheduled yet
		r.ok(t, "network", "agent", "add", "router", "--l3", l3, router)
		if rows := r.list(t, "network", "agent", "list", "--router", router); !in(rows, l3) {
			t.Errorf("network agent list --router = %v, want %s", rows, l3)
		}
		r.ok(t, "network", "agent", "remove", "router", "--l3", l3, router)
		// HA chassis priority is an ML2/OVN notion.
		r.fails(t, "network", "agent", "router", "set", "--ha-chassis-priority", "10", l3, router)
		return
	}
	r.fails(t, "network", "agent", "add", "network", "--dhcp", first, f.net)
	r.fails(t, "network", "agent", "remove", "network", "--dhcp", first, f.net)
	// Scheduling a router onto an OVN gateway chassis is newer than some cells:
	// either answer is neutron's, and which one this cloud gives is logged.
	for _, res := range []result{
		r.run(t, "network", "agent", "add", "router", "--l3", "--ha-chassis-priority", "10", first, router),
		r.run(t, "network", "agent", "router", "set", "--ha-chassis-priority", "20", first, router),
		r.run(t, "network", "agent", "remove", "router", "--l3", first, router),
	} {
		if res.code != 0 && res.stderr == "" {
			t.Errorf("agent router scheduling failed with no message (exit %d)", res.code)
		}
		t.Logf("exit %d %s", res.code, strings.TrimSpace(res.stderr))
	}
}

func TestNetworkQoS(t *testing.T) {
	c := ft(t)
	requireExtension(t, "qos")
	r := defaultRunner(c)

	if types := r.list(t, "network", "qos", "rule", "type", "list"); !in(types, "bandwidth_limit") {
		t.Fatalf("network qos rule type list = %v, want bandwidth_limit", types)
	}
	r.ok(t, "network", "qos", "rule", "type", "list", "--all-supported")
	if rt := r.ok(t, "network", "qos", "rule", "type", "show", "bandwidth_limit", "-f", "json"); !strings.Contains(rt, "max_kbps") {
		t.Errorf("network qos rule type show bandwidth_limit = %s", rt)
	}

	name := uniq("qos")
	p := r.show(t, "network", "qos", "policy", "create", name, "--description", "functional", "--share")
	id := field(p, "id")
	t.Cleanup(func() { r.run(t, "network", "qos", "policy", "delete", id) })
	r.ok(t, "network", "qos", "policy", "set", id, "--name", name+"-renamed", "--no-share")
	if p = r.show(t, "network", "qos", "policy", "show", id); field(p, "name") != name+"-renamed" || field(p, "shared") != "false" {
		t.Errorf("after qos policy set: %v", p)
	}
	if !slices.Contains(column(r.list(t, "network", "qos", "policy", "list"), "id"), id) {
		t.Errorf("qos policy list does not list %s", id)
	}

	rule := r.show(t, "network", "qos", "rule", "create", id, "--type", "bandwidth-limit",
		"--max-kbps", "1000", "--max-burst-kbits", "100", "--egress")
	rid := field(rule, "id")
	r.ok(t, "network", "qos", "rule", "set", id, rid, "--max-kbps", "2000")
	if got := r.show(t, "network", "qos", "rule", "show", id, rid); field(got, "max_kbps") != "2000" {
		t.Errorf("qos rule show after set = %v", got)
	}
	if !in(r.list(t, "network", "qos", "rule", "list", id), rid) {
		t.Errorf("qos rule list does not list %s", rid)
	}

	// The policy on a network, then off it again.
	f := newNetwork(t, r, "203.0.113.0/24")
	r.ok(t, "network", "set", f.net, "--qos-policy", id)
	if field(r.show(t, "network", "show", f.net), "qos_policy_id") != id {
		t.Error("network set --qos-policy did not stick")
	}
	r.ok(t, "network", "set", f.net, "--no-qos-policy")

	r.ok(t, "network", "qos", "rule", "delete", id, rid)
	r.fails(t, "network", "qos", "rule", "show", id, rid)
	r.ok(t, "network", "qos", "policy", "delete", id)
}

func TestNetworkTrunks(t *testing.T) {
	c := ft(t)
	requireExtension(t, "trunk")
	r := defaultRunner(c)
	f := newNetwork(t, r, "203.0.113.0/24")

	port := func() string {
		p := r.show(t, "port", "create", uniq("tport"), "--network", f.net)
		id := field(p, "id")
		t.Cleanup(func() { r.run(t, "port", "delete", id) })
		return id
	}
	parent, sub1, sub2 := port(), port(), port()

	name := uniq("trunk")
	tr := r.show(t, "network", "trunk", "create", name, "--parent-port", parent,
		"--subport", "port="+sub1+",segmentation-type=vlan,segmentation-id=100")
	id := field(tr, "id")
	t.Cleanup(func() { r.run(t, "network", "trunk", "delete", id) })
	r.ok(t, "network", "trunk", "set", id, "--name", name+"-renamed", "--description", "functional")
	if tr = r.show(t, "network", "trunk", "show", id); field(tr, "name") != name+"-renamed" || field(tr, "port_id") != parent {
		t.Errorf("after trunk set: %v", tr)
	}
	if !slices.Contains(column(r.list(t, "network", "trunk", "list", "--long"), "id"), id) {
		t.Errorf("trunk list does not list %s", id)
	}

	r.ok(t, "network", "trunk", "subport", "add", id, sub2, "--segmentation-type", "vlan", "--segmentation-id", "101")
	if subs := r.list(t, "network", "trunk", "subport", "list", id); !in(subs, sub1) || !in(subs, sub2) {
		t.Errorf("trunk subport list = %v, want %s and %s", subs, sub1, sub2)
	}
	r.ok(t, "network", "trunk", "subport", "remove", id, sub2)
	r.ok(t, "network", "trunk", "unset", id, "--subport", sub1)
	if subs := r.list(t, "network", "subport", "list", "--trunk", id); len(subs) != 0 {
		t.Errorf("network subport list after removing both = %v", subs)
	}
	r.ok(t, "network", "trunk", "set", id, "--subport", "port="+sub1+",segmentation-type=vlan,segmentation-id=100")
	if subs := r.list(t, "network", "subport", "list", "--trunk", id); !in(subs, sub1) {
		t.Errorf("network subport list after trunk set --subport = %v", subs)
	}
	r.ok(t, "network", "trunk", "delete", id)
	r.fails(t, "network", "trunk", "show", id)
}

func TestNetworkSegments(t *testing.T) {
	c := ft(t)
	requireExtension(t, "segment")
	r := defaultRunner(c)
	f := newNetwork(t, r, "203.0.113.0/24")

	// A second segment of the tenant type, on a segment ID nothing else uses.
	typ := "vxlan"
	if c.Backend == "ovn" {
		typ = "geneve"
	}
	name := uniq("seg")
	s := r.show(t, "network", "segment", "create", name, "--network", f.net, "--network-type", typ, "--segment", "4242")
	id := field(s, "id")
	t.Cleanup(func() { r.run(t, "network", "segment", "delete", id) })
	r.ok(t, "network", "segment", "set", id, "--name", name+"-renamed", "--description", "functional")
	if s = r.show(t, "network", "segment", "show", id); field(s, "name") != name+"-renamed" || field(s, "segmentation_id") != "4242" {
		t.Errorf("after segment set: %v", s)
	}
	if segs := r.list(t, "network", "segment", "list", "--network", f.net, "--long"); len(segs) != 2 {
		t.Errorf("segment list --network = %v, want the original and ours", segs)
	}
	r.ok(t, "network", "segment", "delete", id)
	r.fails(t, "network", "segment", "show", id)
}

func TestNetworkRBAC(t *testing.T) {
	c := ft(t)
	requireExtension(t, "rbac-policies")
	r := defaultRunner(c)
	f := newNetwork(t, r, "203.0.113.0/24")

	p := r.show(t, "network", "rbac", "create", f.net, "--type", "network", "--action", "access_as_shared",
		"--target-project", "demo")
	id := field(p, "id")
	t.Cleanup(func() { r.run(t, "network", "rbac", "delete", id) })
	if !slices.Contains(column(r.list(t, "network", "rbac", "list", "--type", "network", "--long"), "id"), id) {
		t.Errorf("network rbac list does not list %s", id)
	}
	// demo sees the network now.
	if !slices.Contains(column(demoRunner(c).list(t, "network", "list"), "name"), f.net) {
		t.Errorf("demo cannot see %s after access_as_shared", f.net)
	}
	r.ok(t, "network", "rbac", "set", id, "--target-project", "admin")
	if got := r.show(t, "network", "rbac", "show", id); field(got, "target_project") != issue(t, r).project {
		t.Errorf("network rbac show after set = %v", got)
	}
	r.ok(t, "network", "rbac", "delete", id)
	r.fails(t, "network", "rbac", "show", id)
}

func TestAddressScopesAndSubnetPools(t *testing.T) {
	c := ft(t)
	requireExtension(t, "address-scope")
	r := defaultRunner(c)

	scope := uniq("scope")
	s := r.show(t, "address", "scope", "create", scope, "--ip-version", "4")
	sid := field(s, "id")
	t.Cleanup(func() { r.run(t, "address", "scope", "delete", sid) })
	// --no-share first, while it is still a no-op: neutron never unshares a
	// shared scope.
	r.ok(t, "address", "scope", "set", sid, "--no-share")
	r.ok(t, "address", "scope", "set", sid, "--name", scope+"-renamed", "--share")
	if s = r.show(t, "address", "scope", "show", sid); field(s, "name") != scope+"-renamed" || field(s, "shared") != "true" {
		t.Errorf("after address scope set: %v", s)
	}
	r.fails(t, "address", "scope", "set", sid, "--no-share")
	if !slices.Contains(column(r.list(t, "address", "scope", "list", "--ip-version", "4"), "id"), sid) {
		t.Errorf("address scope list does not list %s", sid)
	}

	pool := uniq("pool")
	p := r.show(t, "subnet", "pool", "create", pool, "--pool-prefix", "192.0.2.0/24",
		"--default-prefix-length", "26", "--address-scope", sid, "--tag", "ft-pool")
	pid := field(p, "id")
	t.Cleanup(func() { r.run(t, "subnet", "pool", "delete", pid) })
	if field(p, "address_scope_id") != sid || field(p, "default_prefixlen") != "26" {
		t.Errorf("subnet pool create = %v", p)
	}
	r.ok(t, "subnet", "pool", "set", pid, "--name", pool+"-renamed", "--description", "functional",
		"--default-quota", "4", "--max-prefix-length", "28")
	if p = r.show(t, "subnet", "pool", "show", pid); field(p, "name") != pool+"-renamed" || field(p, "max_prefixlen") != "28" {
		t.Errorf("after subnet pool set: %v", p)
	}
	r.ok(t, "subnet", "pool", "unset", pid, "--tag", "ft-pool")
	if strings.Contains(field(r.show(t, "subnet", "pool", "show", pid), "tags"), "ft-pool") {
		t.Error("subnet pool unset --tag left the tag")
	}
	if !slices.Contains(column(r.list(t, "subnet", "pool", "list", "--address-scope", sid, "--long"), "id"), pid) {
		t.Errorf("subnet pool list --address-scope does not list %s", pid)
	}
	r.ok(t, "subnet", "pool", "delete", pid)
	r.fails(t, "subnet", "pool", "show", pid)
	r.ok(t, "address", "scope", "delete", sid)
	r.fails(t, "address", "scope", "show", sid)
}

func TestAddressGroups(t *testing.T) {
	c := ft(t)
	requireExtension(t, "address-group")
	r := defaultRunner(c)

	name := uniq("ag")
	g := r.show(t, "address", "group", "create", name, "--address", "192.0.2.1/32", "--description", "functional")
	id := field(g, "id")
	t.Cleanup(func() { r.run(t, "address", "group", "delete", id) })
	r.ok(t, "address", "group", "set", id, "--name", name+"-renamed", "--address", "192.0.2.2/32")
	r.ok(t, "address", "group", "unset", id, "--address", "192.0.2.1/32")
	got := r.ok(t, "address", "group", "show", id, "-f", "json")
	if !strings.Contains(got, name+"-renamed") || !strings.Contains(got, "192.0.2.2/32") || strings.Contains(got, "192.0.2.1/32") {
		t.Errorf("address group show after set/unset = %s", got)
	}
	if !slices.Contains(column(r.list(t, "address", "group", "list", "--name", name+"-renamed"), "id"), id) {
		t.Errorf("address group list --name does not list %s", id)
	}
	r.ok(t, "address", "group", "delete", id)
	r.fails(t, "address", "group", "show", id)
}

//go:build functional

package functional

import (
	"slices"
	"strings"
	"testing"
)

// The neutron core suite: networks, subnets, ports, security groups, routers
// and floating IPs, each written and read back. Every object is the test's own;
// devstack's "public" network is used only as a router gateway and a floating
// IP pool. Addresses are RFC 5737 (203.0.113.0/24 here, 198.51.100.0/24 in the
// server suite, 192.0.2.0/24 for routes and pairs that are never reachable).

// netFixture is a tenant network with one subnet, deleted at the end.
type netFixture struct{ net, subnet, subnetID string }

func newNetwork(t *testing.T, r runner, cidr string) netFixture {
	t.Helper()
	f := netFixture{net: uniq("net")}
	f.subnet = f.net + "-sub"
	r.ok(t, "network", "create", f.net)
	t.Cleanup(func() { r.run(t, "network", "delete", f.net) })
	s := r.show(t, "subnet", "create", f.subnet, "--network", f.net, "--subnet-range", cidr)
	f.subnetID = field(s, "id")
	t.Cleanup(func() { r.run(t, "subnet", "delete", f.subnetID) })
	return f
}

func TestNetworkAndSubnet(t *testing.T) {
	c := ft(t)
	r := defaultRunner(c)
	f := newNetwork(t, r, "203.0.113.0/24")

	r.ok(t, "network", "set", f.net, "--description", "functional", "--mtu", "1400",
		"--tag", "ft-a", "--tag", "ft-b")
	n := r.show(t, "network", "show", f.net)
	if field(n, "description") != "functional" || field(n, "mtu") != "1400" ||
		!strings.Contains(field(n, "tags"), "ft-a") {
		t.Errorf("after network set: %v", n)
	}
	r.ok(t, "network", "unset", f.net, "--tag", "ft-a")
	if tags := field(r.show(t, "network", "show", f.net), "tags"); strings.Contains(tags, "ft-a") || !strings.Contains(tags, "ft-b") {
		t.Errorf("tags after unset --tag ft-a = %s", tags)
	}
	r.ok(t, "network", "unset", f.net, "--all-tag")

	// --allocation-pool adds a pool; with --no-allocation-pool it replaces the
	// one the subnet was created with, which would overlap it.
	r.ok(t, "subnet", "set", f.subnet, "--description", "functional", "--dns-nameserver", "192.0.2.53",
		"--no-allocation-pool", "--allocation-pool", "start=203.0.113.100,end=203.0.113.150",
		"--host-route", "destination=192.0.2.0/24,gateway=203.0.113.1", "--tag", "ft-subtag")
	s := r.ok(t, "subnet", "show", f.subnet, "-f", "json")
	for _, want := range []string{"192.0.2.53", "203.0.113.150", "192.0.2.0/24", "ft-subtag", "functional"} {
		if !strings.Contains(s, want) {
			t.Errorf("subnet show after set lacks %q: %s", want, s)
		}
	}
	r.ok(t, "subnet", "unset", f.subnet, "--dns-nameserver", "192.0.2.53",
		"--host-route", "destination=192.0.2.0/24,gateway=203.0.113.1", "--tag", "ft-subtag")
	if s = r.ok(t, "subnet", "show", f.subnet, "-f", "json"); strings.Contains(s, "192.0.2.53") || strings.Contains(s, "ft-subtag") {
		t.Errorf("subnet show after unset: %s", s)
	}
	if rows := r.list(t, "subnet", "list", "--network", f.net, "--long"); len(rows) != 1 || field(rows[0], "id") != f.subnetID {
		t.Errorf("subnet list --network = %v", rows)
	}

	// How much of the network is in use.
	if a := r.ok(t, "ip", "availability", "show", f.net, "-f", "json"); !strings.Contains(a, f.subnetID) {
		t.Errorf("ip availability show = %s, want subnet %s", a, f.subnetID)
	}
	r.ok(t, "ip", "availability", "list", "--ip-version", "4")

	r.ok(t, "subnet", "delete", f.subnetID)
	r.fails(t, "subnet", "show", f.subnetID)
}

func TestPorts(t *testing.T) {
	c := ft(t)
	r := defaultRunner(c)
	f := newNetwork(t, r, "203.0.113.0/24")

	name := uniq("port")
	p := r.show(t, "port", "create", name, "--network", f.net)
	id := field(p, "id")
	t.Cleanup(func() { r.run(t, "port", "delete", id) })

	r.ok(t, "port", "set", id, "--name", name+"-renamed", "--description", "functional", "--no-fixed-ip",
		"--fixed-ip", "subnet="+f.subnetID+",ip-address=203.0.113.20",
		"--allowed-address", "ip-address=192.0.2.99", "--tag", "ft-porttag", "--disable")
	got := r.ok(t, "port", "show", id, "-f", "json")
	for _, want := range []string{name + "-renamed", "203.0.113.20", "192.0.2.99", "ft-porttag"} {
		if !strings.Contains(got, want) {
			t.Errorf("port show after set lacks %q: %s", want, got)
		}
	}
	if field(r.show(t, "port", "show", id), "admin_state_up") != "false" {
		t.Error("port set --disable left the port up")
	}
	r.ok(t, "port", "unset", id, "--allowed-address", "ip-address=192.0.2.99", "--tag", "ft-porttag")
	if got = r.ok(t, "port", "show", id, "-f", "json"); strings.Contains(got, "192.0.2.99") || strings.Contains(got, "ft-porttag") {
		t.Errorf("port show after unset: %s", got)
	}
	if rows := r.list(t, "port", "list", "--network", f.net, "--fixed-ip", "ip-address=203.0.113.20"); len(rows) != 1 {
		t.Errorf("port list --fixed-ip = %v", rows)
	}
}

func TestSecurityGroups(t *testing.T) {
	c := ft(t)
	r := defaultRunner(c)

	name := uniq("sg")
	g := r.show(t, "security", "group", "create", name, "--description", "functional")
	id := field(g, "id")
	t.Cleanup(func() { r.run(t, "security", "group", "delete", id) })
	r.ok(t, "security", "group", "set", id, "--name", name+"-renamed", "--description", "changed", "--tag", "ft-sg")
	g = r.show(t, "security", "group", "show", id)
	if field(g, "name") != name+"-renamed" || field(g, "description") != "changed" || !strings.Contains(field(g, "tags"), "ft-sg") {
		t.Errorf("after security group set: %v", g)
	}
	if !slices.Contains(column(r.list(t, "security", "group", "list", "--tags", "ft-sg"), "id"), id) {
		t.Errorf("security group list --tags ft-sg does not list %s", id)
	}
	r.ok(t, "security", "group", "unset", id, "--tag", "ft-sg")
	if slices.Contains(column(r.list(t, "security", "group", "list", "--tags", "ft-sg"), "id"), id) {
		t.Errorf("security group list --tags ft-sg still lists %s after unset", id)
	}

	rule := r.show(t, "security", "group", "rule", "create", id, "--ingress", "--protocol", "tcp",
		"--dst-port", "22:23", "--remote-ip", "192.0.2.0/24", "--description", "functional")
	rid := field(rule, "id")
	if field(rule, "port_range_min") != "22" || field(rule, "port_range_max") != "23" ||
		field(rule, "remote_ip_prefix") != "192.0.2.0/24" {
		t.Errorf("security group rule create = %v", rule)
	}
	if got := r.show(t, "security", "group", "rule", "show", rid); field(got, "security_group_id") != id {
		t.Errorf("security group rule show = %v", got)
	}
	if !slices.Contains(column(r.list(t, "security", "group", "rule", "list", id, "--ingress", "--protocol", "tcp"), "id"), rid) {
		t.Errorf("security group rule list does not list %s", rid)
	}
	r.ok(t, "security", "group", "rule", "delete", rid)
	r.fails(t, "security", "group", "rule", "show", rid)
}

func TestRouters(t *testing.T) {
	c := ft(t)
	r := defaultRunner(c)
	f := newNetwork(t, r, "203.0.113.0/24")
	other := newNetwork(t, r, "198.51.100.128/25")

	name := uniq("router")
	rt := r.show(t, "router", "create", name, "--description", "functional")
	id := field(rt, "id")
	t.Cleanup(func() {
		r.run(t, "router", "remove", "subnet", id, f.subnetID)
		r.run(t, "router", "remove", "gateway", id)
		r.run(t, "router", "delete", id)
	})
	r.ok(t, "router", "set", id, "--name", name+"-renamed", "--description", "changed", "--tag", "ft-rt")
	if rt = r.show(t, "router", "show", id); field(rt, "name") != name+"-renamed" || !strings.Contains(field(rt, "tags"), "ft-rt") {
		t.Errorf("after router set: %v", rt)
	}
	r.ok(t, "router", "unset", id, "--tag", "ft-rt")

	// Interfaces: one by subnet, one by a port on a second network.
	r.ok(t, "router", "add", "subnet", id, f.subnetID)
	port := r.show(t, "port", "create", uniq("rtport"), "--network", other.net)
	portID2 := field(port, "id")
	t.Cleanup(func() { r.run(t, "port", "delete", portID2) })
	r.ok(t, "router", "add", "port", id, portID2)
	if ports := r.list(t, "port", "list", "--router", id); len(ports) != 2 {
		t.Errorf("port list --router = %v, want two interfaces", ports)
	}

	r.ok(t, "router", "add", "gateway", id, "public")
	if gw := field(r.show(t, "router", "show", id), "external_gateway_info"); gw == "" {
		t.Error("router has no external gateway after add gateway")
	}

	// Static routes, added and removed by add/remove route, then by set/unset.
	route := "destination=192.0.2.0/24,gateway=203.0.113.5"
	r.ok(t, "router", "add", "route", id, "--route", route)
	if !strings.Contains(r.ok(t, "router", "show", id, "-f", "json"), "192.0.2.0/24") {
		t.Error("router show lacks the added route")
	}
	r.ok(t, "router", "remove", "route", id, "--route", route)
	r.ok(t, "router", "set", id, "--route", route)
	r.ok(t, "router", "unset", id, "--route", route)
	if strings.Contains(r.ok(t, "router", "show", id, "-f", "json"), "192.0.2.0/24") {
		t.Error("router show still has the route after unset")
	}
	if !slices.Contains(column(r.list(t, "router", "list", "--long"), "id"), id) {
		t.Errorf("router list does not list %s", id)
	}

	r.ok(t, "router", "remove", "port", id, portID2)
	r.ok(t, "router", "remove", "gateway", id)
	r.ok(t, "router", "remove", "subnet", id, f.subnetID)
	r.ok(t, "router", "delete", id)
	r.fails(t, "router", "show", id)
}

// A floating IP associated through a router to a port, and a port forwarding on
// a second one.
func TestFloatingIPs(t *testing.T) {
	c := ft(t)
	r := defaultRunner(c)
	f := newNetwork(t, r, "203.0.113.0/24")

	router := uniq("fiprouter")
	r.ok(t, "router", "create", router)
	t.Cleanup(func() {
		r.run(t, "router", "remove", "subnet", router, f.subnetID)
		r.run(t, "router", "delete", router)
	})
	r.ok(t, "router", "add", "gateway", router, "public")
	r.ok(t, "router", "add", "subnet", router, f.subnetID)
	port := r.show(t, "port", "create", uniq("fipport"), "--network", f.net,
		"--fixed-ip", "subnet="+f.subnetID+",ip-address=203.0.113.30")
	portID := field(port, "id")
	t.Cleanup(func() { r.run(t, "port", "delete", portID) })

	fip := r.show(t, "floating", "ip", "create", "public")
	fipID := field(fip, "id")
	t.Cleanup(func() { r.run(t, "floating", "ip", "delete", fipID) })
	r.ok(t, "floating", "ip", "set", fipID, "--port", portID, "--description", "functional", "--tag", "ft-fip")
	got := r.show(t, "floating", "ip", "show", fipID)
	if field(got, "port_id") != portID || field(got, "fixed_ip_address") != "203.0.113.30" ||
		!strings.Contains(field(got, "tags"), "ft-fip") {
		t.Errorf("after floating ip set: %v", got)
	}
	if rows := r.list(t, "floating", "ip", "list", "--port", portID, "--long"); len(rows) != 1 {
		t.Errorf("floating ip list --port = %v", rows)
	}
	r.ok(t, "floating", "ip", "unset", fipID, "--port", "--tag", "ft-fip")
	if got = r.show(t, "floating", "ip", "show", fipID); field(got, "port_id") != "" {
		t.Errorf("after floating ip unset --port: %v", got)
	}

	t.Run("port forwarding", func(t *testing.T) {
		requireExtension(t, "floating-ip-port-forwarding")
		pf := r.show(t, "floating", "ip", "port", "forwarding", "create", fipID, "--port", portID,
			"--internal-ip-address", "203.0.113.30", "--internal-protocol-port", "22",
			"--external-protocol-port", "2222", "--protocol", "tcp", "--description", "functional")
		pfID := field(pf, "id")
		t.Cleanup(func() { r.run(t, "floating", "ip", "port", "forwarding", "delete", fipID, pfID) })
		r.ok(t, "floating", "ip", "port", "forwarding", "set", fipID, pfID, "--external-protocol-port", "2223")
		if got := r.ok(t, "floating", "ip", "port", "forwarding", "show", fipID, pfID, "-f", "json"); !strings.Contains(got, "2223") {
			t.Errorf("port forwarding show after set = %s", got)
		}
		if rows := r.list(t, "floating", "ip", "port", "forwarding", "list", fipID, "--protocol", "tcp"); len(rows) != 1 {
			t.Errorf("port forwarding list = %v", rows)
		}
		r.ok(t, "floating", "ip", "port", "forwarding", "delete", fipID, pfID)
		r.fails(t, "floating", "ip", "port", "forwarding", "show", fipID, pfID)
	})
}

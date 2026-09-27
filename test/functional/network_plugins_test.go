//go:build functional

package functional

import (
	"strings"
	"testing"
)

// The neutron service plugins up.sh's "net" feature stacks: dynamic routing
// (BGP), networking-bgpvpn, fwaas v2, vpnaas and tap-as-a-service's mirrors.
// These are API-level tests: objects are created, associated, read back and
// removed. No peer, VPN endpoint or mirror target ever answers, so nothing
// here asserts on traffic.

// gatewayRouter is a router with an external gateway on "public" and one
// tenant subnet, as vpnaas and bgpvpn router associations want.
func gatewayRouter(t *testing.T, r runner, f netFixture) string {
	t.Helper()
	name := uniq("gwrouter")
	id := field(r.show(t, "router", "create", name), "id")
	t.Cleanup(func() {
		r.run(t, "router", "remove", "subnet", id, f.subnetID)
		r.run(t, "router", "remove", "gateway", id)
		r.run(t, "router", "delete", id)
	})
	r.ok(t, "router", "add", "gateway", id, "public")
	r.ok(t, "router", "add", "subnet", id, f.subnetID)
	return id
}

func TestBGPDynamicRouting(t *testing.T) {
	c := ft(t)
	requireExtension(t, "bgp")
	r := defaultRunner(c)

	peer := uniq("peer")
	p := r.show(t, "bgp", "peer", "create", peer, "--peer-ip", "192.0.2.30", "--remote-as", "65002",
		"--auth-type", "md5", "--password", "ft-secret")
	peerID := field(p, "id")
	t.Cleanup(func() { r.run(t, "bgp", "peer", "delete", peerID) })
	r.ok(t, "bgp", "peer", "set", peerID, "--name", peer+"-renamed")
	if got := r.show(t, "bgp", "peer", "show", peerID); field(got, "name") != peer+"-renamed" || field(got, "peer_ip") != "192.0.2.30" {
		t.Errorf("bgp peer show after set = %v", got)
	}
	if !in(r.list(t, "bgp", "peer", "list"), peerID) {
		t.Errorf("bgp peer list does not list %s", peerID)
	}

	speaker := uniq("speaker")
	s := r.show(t, "bgp", "speaker", "create", speaker, "--local-as", "65001", "--ip-version", "4",
		"--no-advertise-tenant-networks")
	spID := field(s, "id")
	t.Cleanup(func() { r.run(t, "bgp", "speaker", "delete", spID) })
	r.ok(t, "bgp", "speaker", "set", spID, "--name", speaker+"-renamed", "--advertise-tenant-networks")
	if got := r.show(t, "bgp", "speaker", "show", spID); field(got, "name") != speaker+"-renamed" ||
		field(got, "advertise_tenant_networks") != "true" {
		t.Errorf("bgp speaker show after set = %v", got)
	}

	r.ok(t, "bgp", "speaker", "add", "network", spID, "public")
	r.ok(t, "bgp", "speaker", "add", "peer", spID, peerID)
	got := r.show(t, "bgp", "speaker", "show", spID)
	if !strings.Contains(field(got, "peers"), peerID) || field(got, "networks") == "" {
		t.Errorf("bgp speaker show after add network/peer = %v", got)
	}
	r.ok(t, "bgp", "speaker", "list", "advertised", "routes", spID)

	// A DR agent hosts speakers when up.sh's cell runs one; the scheduling API
	// is there either way, and an agent that does not exist is refused.
	agents := r.list(t, "bgp", "dragent", "list")
	if len(agents) > 0 {
		ag := field(agents[0], "id")
		r.run(t, "bgp", "dragent", "remove", "speaker", ag, spID) // it may be scheduled already
		r.ok(t, "bgp", "dragent", "add", "speaker", ag, spID)
		if !in(r.list(t, "bgp", "dragent", "list", "--bgp-speaker", spID), ag) {
			t.Errorf("bgp dragent list --bgp-speaker does not list %s", ag)
		}
		r.ok(t, "bgp", "dragent", "remove", "speaker", ag, spID)
	} else {
		t.Log("no BGP DR agent on this cell: scheduling checked by refusal only")
		r.fails(t, "bgp", "dragent", "add", "speaker", "3c2b1a09-8f7e-4d6c-9b5a-4a3b2c1d0e9f", spID)
		r.fails(t, "bgp", "dragent", "remove", "speaker", "3c2b1a09-8f7e-4d6c-9b5a-4a3b2c1d0e9f", spID)
	}

	r.ok(t, "bgp", "speaker", "remove", "peer", spID, peerID)
	r.ok(t, "bgp", "speaker", "remove", "network", spID, "public")
	r.ok(t, "bgp", "speaker", "delete", spID)
	r.ok(t, "bgp", "peer", "delete", peerID)
	r.fails(t, "bgp", "peer", "show", peerID)
}

func TestBGPVPN(t *testing.T) {
	c := ft(t)
	requireExtension(t, "bgpvpn")
	r := defaultRunner(c)
	f := newNetwork(t, r, "203.0.113.0/24")

	name := uniq("bgpvpn")
	v := r.show(t, "bgpvpn", "create", "--name", name, "--type", "l3", "--route-target", "64512:1")
	id := field(v, "id")
	t.Cleanup(func() { r.run(t, "bgpvpn", "delete", id) })
	r.ok(t, "bgpvpn", "set", id, "--name", name+"-renamed", "--import-target", "64512:2", "--export-target", "64512:3")
	r.ok(t, "bgpvpn", "unset", id, "--all-import-target")
	got := r.ok(t, "bgpvpn", "show", id, "-f", "json")
	if !strings.Contains(got, name+"-renamed") || !strings.Contains(got, "64512:3") || strings.Contains(got, "64512:2") {
		t.Errorf("bgpvpn show after set/unset = %s", got)
	}
	if !in(r.list(t, "bgpvpn", "list", "--long"), id) {
		t.Errorf("bgpvpn list does not list %s", id)
	}

	na := r.show(t, "bgpvpn", "network", "association", "create", id, f.net)
	naID := field(na, "id")
	if got := r.show(t, "bgpvpn", "network", "association", "show", naID, id); field(got, "network_id") == "" {
		t.Errorf("bgpvpn network association show = %v", got)
	}
	if !in(r.list(t, "bgpvpn", "network", "association", "list", id), naID) {
		t.Errorf("network association list does not list %s", naID)
	}
	r.ok(t, "bgpvpn", "network", "association", "delete", naID, id)

	router := gatewayRouter(t, r, newNetwork(t, r, "198.51.100.0/25"))
	ra := r.show(t, "bgpvpn", "router", "association", "create", id, router)
	raID := field(ra, "id")
	if !in(r.list(t, "bgpvpn", "router", "association", "list", id), raID) {
		t.Errorf("router association list does not list %s", raID)
	}

	port := field(r.show(t, "port", "create", uniq("vpnport"), "--network", f.net), "id")
	t.Cleanup(func() { r.run(t, "port", "delete", port) })
	pa := r.show(t, "bgpvpn", "port", "association", "create", id, port)
	paID := field(pa, "id")
	if !in(r.list(t, "bgpvpn", "port", "association", "list", id), paID) {
		t.Errorf("port association list does not list %s", paID)
	}

	// Route control (advertise flags, prefix routes) is its own extension.
	t.Run("routes control", func(t *testing.T) {
		requireExtension(t, "bgpvpn-routes-control")
		r.ok(t, "bgpvpn", "router", "association", "set", raID, id, "--no-advertise")
		if got := r.show(t, "bgpvpn", "router", "association", "show", raID, id); field(got, "advertise_extra_routes") != "false" {
			t.Errorf("router association after --no-advertise = %v", got)
		}
		r.ok(t, "bgpvpn", "router", "association", "unset", raID, id, "--advertise")
		r.ok(t, "bgpvpn", "port", "association", "set", paID, id, "--prefix-route", "prefix=192.0.2.0/24",
			"--no-advertise-fixed-ips")
		if got := r.ok(t, "bgpvpn", "port", "association", "show", paID, id, "-f", "json"); !strings.Contains(got, "192.0.2.0/24") {
			t.Errorf("port association show after set = %s", got)
		}
		r.ok(t, "bgpvpn", "port", "association", "unset", paID, id, "--all-prefix-routes", "--advertise-fixed-ips")
	})

	r.ok(t, "bgpvpn", "port", "association", "delete", paID, id)
	r.ok(t, "bgpvpn", "router", "association", "delete", raID, id)
	r.ok(t, "bgpvpn", "delete", id)
	r.fails(t, "bgpvpn", "show", id)
}

func TestFirewallV2(t *testing.T) {
	c := ft(t)
	requireExtension(t, "fwaas_v2")
	r := defaultRunner(c)

	rule := func(port string) string {
		ru := r.show(t, "firewall", "group", "rule", "create", uniq("fwrule"), "--protocol", "tcp",
			"--destination-port", port, "--action", "allow", "--description", "functional")
		id := field(ru, "id")
		t.Cleanup(func() { r.run(t, "firewall", "group", "rule", "delete", id) })
		return id
	}
	r1, r2 := rule("22"), rule("443")
	r.ok(t, "firewall", "group", "rule", "set", r1, "--action", "deny", "--source-ip-address", "192.0.2.0/24")
	r.ok(t, "firewall", "group", "rule", "unset", r1, "--source-ip-address")
	if got := r.show(t, "firewall", "group", "rule", "show", r1); field(got, "action") != "deny" || field(got, "Source IP Address") != "" {
		t.Errorf("firewall rule show after set/unset = %v", got)
	}
	if !in(r.list(t, "firewall", "group", "rule", "list", "--long"), r1) {
		t.Errorf("firewall rule list does not list %s", r1)
	}

	pol := r.show(t, "firewall", "group", "policy", "create", uniq("fwpol"), "--firewall-rule", r1)
	polID := field(pol, "id")
	t.Cleanup(func() { r.run(t, "firewall", "group", "policy", "delete", polID) })
	r.ok(t, "firewall", "group", "policy", "add", "rule", polID, r2, "--insert-before", r1)
	rules := field(r.show(t, "firewall", "group", "policy", "show", polID), "Firewall Rules")
	if i1, i2 := strings.Index(rules, r1), strings.Index(rules, r2); i1 < 0 || i2 < 0 || i2 > i1 {
		t.Errorf("policy rules = %s, want %s inserted before %s", rules, r2, r1)
	}
	r.ok(t, "firewall", "group", "policy", "remove", "rule", polID, r2)
	r.ok(t, "firewall", "group", "policy", "set", polID, "--description", "changed", "--audited")
	if got := r.show(t, "firewall", "group", "policy", "show", polID); field(got, "audited") != "true" {
		t.Errorf("policy show after set --audited = %v", got)
	}
	r.ok(t, "firewall", "group", "policy", "unset", polID, "--audited")
	if !in(r.list(t, "firewall", "group", "policy", "list", "--long"), polID) {
		t.Errorf("firewall policy list does not list %s", polID)
	}

	g := r.show(t, "firewall", "group", "create", uniq("fwg"), "--ingress-firewall-policy", polID, "--no-port")
	gID := field(g, "id")
	t.Cleanup(func() { r.run(t, "firewall", "group", "delete", gID) })
	r.ok(t, "firewall", "group", "set", gID, "--egress-firewall-policy", polID, "--description", "functional")
	if got := r.show(t, "firewall", "group", "show", gID); field(got, "Egress Policy ID") != polID {
		t.Errorf("firewall group show after set = %v", got)
	}
	r.ok(t, "firewall", "group", "unset", gID, "--egress-firewall-policy", "--ingress-firewall-policy")
	if !in(r.list(t, "firewall", "group", "list", "--long"), gID) {
		t.Errorf("firewall group list does not list %s", gID)
	}

	r.ok(t, "firewall", "group", "delete", gID)
	r.ok(t, "firewall", "group", "policy", "unset", polID, "--all-firewall-rule")
	r.ok(t, "firewall", "group", "policy", "delete", polID)
	r.ok(t, "firewall", "group", "rule", "delete", r1)
	r.fails(t, "firewall", "group", "rule", "show", r1)
}

func TestVPNaaS(t *testing.T) {
	c := ft(t)
	requireExtension(t, "vpnaas")
	r := defaultRunner(c)
	f := newNetwork(t, r, "203.0.113.0/24")
	router := gatewayRouter(t, r, f)

	ike := r.show(t, "vpn", "ike", "policy", "create", uniq("ike"), "--ike-version", "v2",
		"--encryption-algorithm", "aes-256", "--auth-algorithm", "sha256")
	ikeID := field(ike, "id")
	t.Cleanup(func() { r.run(t, "vpn", "ike", "policy", "delete", ikeID) })
	r.ok(t, "vpn", "ike", "policy", "set", ikeID, "--description", "functional", "--lifetime", "units=seconds,value=7200")
	if got := r.ok(t, "vpn", "ike", "policy", "show", ikeID, "-f", "json"); !strings.Contains(got, "7200") {
		t.Errorf("ike policy show after set = %s", got)
	}
	if !in(r.list(t, "vpn", "ike", "policy", "list", "--long"), ikeID) {
		t.Errorf("ike policy list does not list %s", ikeID)
	}

	ipsec := r.show(t, "vpn", "ipsec", "policy", "create", uniq("ipsec"), "--transform-protocol", "esp",
		"--encryption-algorithm", "aes-256", "--auth-algorithm", "sha256")
	ipsecID := field(ipsec, "id")
	t.Cleanup(func() { r.run(t, "vpn", "ipsec", "policy", "delete", ipsecID) })
	r.ok(t, "vpn", "ipsec", "policy", "set", ipsecID, "--description", "functional", "--pfs", "group14")
	if got := r.show(t, "vpn", "ipsec", "policy", "show", ipsecID); field(got, "Perfect Forward Secrecy (PFS)") != "group14" {
		t.Errorf("ipsec policy show after set = %v", got)
	}
	if !in(r.list(t, "vpn", "ipsec", "policy", "list", "--long"), ipsecID) {
		t.Errorf("ipsec policy list does not list %s", ipsecID)
	}

	local := r.show(t, "vpn", "endpoint", "group", "create", uniq("local"), "--type", "subnet", "--value", f.subnetID)
	localID := field(local, "id")
	t.Cleanup(func() { r.run(t, "vpn", "endpoint", "group", "delete", localID) })
	peer := r.show(t, "vpn", "endpoint", "group", "create", uniq("peer"), "--type", "cidr", "--value", "192.0.2.0/24")
	peerID := field(peer, "id")
	t.Cleanup(func() { r.run(t, "vpn", "endpoint", "group", "delete", peerID) })
	r.ok(t, "vpn", "endpoint", "group", "set", peerID, "--description", "functional")
	if got := r.ok(t, "vpn", "endpoint", "group", "show", peerID, "-f", "json"); !strings.Contains(got, "192.0.2.0/24") {
		t.Errorf("endpoint group show = %s", got)
	}
	if !in(r.list(t, "vpn", "endpoint", "group", "list", "--long"), peerID) {
		t.Errorf("endpoint group list does not list %s", peerID)
	}

	svc := r.show(t, "vpn", "service", "create", uniq("vpnsvc"), "--router", router)
	svcID := field(svc, "id")
	t.Cleanup(func() { r.run(t, "vpn", "service", "delete", svcID) })
	r.ok(t, "vpn", "service", "set", svcID, "--description", "functional")
	if got := r.show(t, "vpn", "service", "show", svcID); field(got, "description") != "functional" {
		t.Errorf("vpn service show after set = %v", got)
	}
	if !in(r.list(t, "vpn", "service", "list", "--long"), svcID) {
		t.Errorf("vpn service list does not list %s", svcID)
	}

	conn := r.show(t, "vpn", "ipsec", "site", "connection", "create", uniq("conn"), "--vpnservice", svcID,
		"--ikepolicy", ikeID, "--ipsecpolicy", ipsecID, "--peer-address", "192.0.2.40", "--peer-id", "192.0.2.40",
		"--psk", "ft-preshared", "--local-endpoint-group", localID, "--peer-endpoint-group", peerID)
	connID := field(conn, "id")
	t.Cleanup(func() { r.run(t, "vpn", "ipsec", "site", "connection", "delete", connID) })
	r.ok(t, "vpn", "ipsec", "site", "connection", "set", connID, "--mtu", "1400", "--description", "functional")
	if got := r.show(t, "vpn", "ipsec", "site", "connection", "show", connID); field(got, "mtu") != "1400" {
		t.Errorf("site connection show after set = %v", got)
	}
	if !in(r.list(t, "vpn", "ipsec", "site", "connection", "list", "--long"), connID) {
		t.Errorf("site connection list does not list %s", connID)
	}

	r.ok(t, "vpn", "ipsec", "site", "connection", "delete", connID)
	r.ok(t, "vpn", "service", "delete", svcID)
	r.ok(t, "vpn", "endpoint", "group", "delete", peerID, localID)
	r.ok(t, "vpn", "ipsec", "policy", "delete", ipsecID)
	r.ok(t, "vpn", "ike", "policy", "delete", ikeID)
	r.fails(t, "vpn", "ike", "policy", "show", ikeID)
}

// Tap mirrors are tap-as-a-service's from 2025.1 (TestCapabilityMatrix pins
// that); under OVN the API loads but nothing is mirrored, so this is API-only.
func TestTapMirrors(t *testing.T) {
	c := ft(t)
	requireExtension(t, "tap-mirror")
	r := defaultRunner(c)
	f := newNetwork(t, r, "203.0.113.0/24")
	port := field(r.show(t, "port", "create", uniq("mirrored"), "--network", f.net), "id")
	t.Cleanup(func() { r.run(t, "port", "delete", port) })

	name := uniq("mirror")
	m := r.show(t, "tap", "mirror", "create", "--name", name, "--port", port, "--directions", "IN=101",
		"--remote-ip", "192.0.2.50", "--mirror-type", "gre")
	id := field(m, "id")
	t.Cleanup(func() { r.run(t, "tap", "mirror", "delete", id) })
	r.ok(t, "tap", "mirror", "update", id, "--name", name+"-renamed", "--description", "functional")
	if got := r.show(t, "tap", "mirror", "show", id); field(got, "name") != name+"-renamed" || field(got, "remote_ip") != "192.0.2.50" {
		t.Errorf("tap mirror show after update = %v", got)
	}
	if !in(r.list(t, "tap", "mirror", "list"), id) {
		t.Errorf("tap mirror list does not list %s", id)
	}
	r.ok(t, "tap", "mirror", "delete", id)
	r.fails(t, "tap", "mirror", "show", id)
}

//go:build functional

package functional

import "testing"

// TestCapabilityMatrix pins what each cell of the nightly matrix offers, as
// recorded in docs/verification/2026-09-27-devstack-bringup.md "Results".
//
// Every other test skips when a capability is missing, which is right for the
// test and wrong for the suite: an extension that silently vanished from a
// release would turn its tests into skips and the run would stay green. This
// test is the one place that notices, because it fails on an unexpected absence
// as loudly as on an unexpected presence.
func TestCapabilityMatrix(t *testing.T) {
	c := ft(t)
	requireFeature(t, "net")
	ext := extensions(t)

	// Stacked on every release (up.sh's "net" feature).
	// l2_adjacency is the segments plugin's.
	for _, alias := range []string{"qos", "trunk", "segment", "l2_adjacency", "floating-ip-port-forwarding",
		"bgp", "bgpvpn", "vpnaas", "fwaas_v2", "taas"} {
		if !ext[alias] {
			t.Errorf("extension %q missing on %s (%s): every cell stacks it", alias, c.Series, c.Backend)
		}
	}
	// Loaded by ML2 itself on both backends, whatever the plugins.
	for _, alias := range []string{"agent", "rbac-policies", "address-scope", "address-group"} {
		if !ext[alias] {
			t.Errorf("extension %q missing on %s (%s): ML2 always loads it", alias, c.Series, c.Backend)
		}
	}

	// tap-as-a-service ships tap mirrors from 2025.1. Under OVN the API loads
	// but no traffic is mirrored, so tap-mirror tests are API-only everywhere.
	if want := c.Series.atLeast("2025.1"); ext["tap-mirror"] != want {
		t.Errorf("tap-mirror present = %v on %s, want %v (tap-as-a-service ships it from 2025.1)",
			ext["tap-mirror"], c.Series, want)
	}

	// ML2/OVN schedules routers onto gateway chassis by priority from 2026.2
	// (neutron 29.0.0); TestNetworkAgents asserts the verbs when it is there.
	if want := c.Backend == "ovn" && c.Series.atLeast("2026.2"); ext["l3-agent-scheduler-ha-chassis-priority"] != want {
		t.Errorf("l3-agent-scheduler-ha-chassis-priority present = %v on %s (%s), want %v",
			ext["l3-agent-scheduler-ha-chassis-priority"], c.Series, c.Backend, want)
	}

	// ML2 reports router:external on subnets from 2024.2 (neutron 25.0.0), on
	// both backends; TestNetworkAndSubnet asserts subnet show carries it.
	if want := c.Series.atLeast("2024.2"); ext["subnet-external-network"] != want {
		t.Errorf("subnet-external-network present = %v on %s, want %v (ML2 loads it from 2024.2)",
			ext["subnet-external-network"], c.Series, want)
	}

	// ML2/OVN exposes the router's read-only top-level enable_snat from 2026.1
	// (neutron 28.0.0, common/ovn/extensions.py); the OVS L3 plugin does not
	// load it. TestRouters asserts router show carries it.
	if want := c.Backend == "ovn" && c.Series.atLeast("2026.1"); ext["router-enable-snat"] != want {
		t.Errorf("router-enable-snat present = %v on %s (%s), want %v",
			ext["router-enable-snat"], c.Series, c.Backend, want)
	}

	// The backend follows the series (up.sh): OVS through 2024.1, OVN after.
	if want := map[bool]string{true: "ovn", false: "ovs"}[c.Series.atLeast("2025.1")]; c.Backend != want {
		t.Errorf("backend %q on %s, want %q", c.Backend, c.Series, want)
	}
}

// TestCatalog is the harness's own smoke test: koc authenticates by the
// default credential path and the catalog holds each stacked service.
func TestCatalog(t *testing.T) {
	c := ft(t)
	var rows []map[string]any
	defaultRunner(c).json(t, &rows, "catalog", "list")

	types := map[string]bool{}
	for _, r := range rows {
		if typ, ok := r["Type"].(string); ok {
			types[typ] = true
		}
	}
	want := []string{"identity", "network"}
	if c.Features["core"] {
		want = append(want, "compute", "image", "block-storage", "placement", "object-store")
	}
	if c.Features["dns"] {
		want = append(want, "dns")
	}
	for _, typ := range want {
		if !types[typ] {
			t.Errorf("catalog has no %q service (auth=%s); got %v", typ, c.Auth, types)
		}
	}
}

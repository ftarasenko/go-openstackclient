//go:build functional

package functional

import (
	"slices"
	"testing"
)

// `extension list/show` is upstream's openstack.common spelling: the list
// merges nova's, cinder's and neutron's discovery documents; show is neutron's.
func TestExtensionCommon(t *testing.T) {
	c := ft(t)
	requireFeature(t, "core")
	r := defaultRunner(c)

	aliases := func(args ...string) []string {
		return column(r.list(t, append([]string{"extension", "list"}, args...)...), "Alias")
	}
	network := aliases("--network")
	if !slices.Contains(network, "router") {
		t.Errorf("extension list --network = %v, want router", network)
	}
	if slices.Contains(network, "os-aggregates") {
		t.Errorf("extension list --network carries nova's os-aggregates: %v", network)
	}
	if compute := aliases("--compute"); !slices.Contains(compute, "os-aggregates") {
		t.Errorf("extension list --compute = %v, want nova's os-aggregates", compute)
	}
	if volume := aliases("--volume"); len(volume) == 0 {
		t.Error("extension list --volume is empty")
	}
	// No flag lists every service.
	all := r.list(t, "extension", "list", "--long")
	if got := column(all, "Alias"); !slices.Contains(got, "router") || !slices.Contains(got, "os-aggregates") {
		t.Errorf("extension list --long = %v, want neutron's and nova's", got)
	}
	if _, ok := all[0]["Namespace"]; !ok {
		t.Errorf("extension list --long has no Namespace column: %v", all[0])
	}

	// show takes an alias or a name.
	byAlias := r.show(t, "extension", "show", "router")
	if field(byAlias, "alias") != "router" {
		t.Errorf("extension show router = %v", byAlias)
	}
	if byName := r.show(t, "extension", "show", field(byAlias, "name")); field(byName, "alias") != "router" {
		t.Errorf("extension show %q = %v", field(byAlias, "name"), byName)
	}
	r.fails(t, "extension", "show", "ft-no-such-extension")
}

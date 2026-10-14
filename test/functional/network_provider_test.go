//go:build functional

package functional

import "testing"

// network list --provider (koc-only) renders each network's provider:* from
// the listing itself; devstack's "public" network is a flat network on a
// physnet, so the row must agree with network show.
func TestNetworkListProviderColumns(t *testing.T) {
	c := ft(t)
	requireExtension(t, "provider")
	r := defaultRunner(c)

	show := r.show(t, "network", "show", "public")
	var row map[string]any
	for _, n := range r.list(t, "network", "list", "--provider", "--name", "public") {
		if field(n, "ID") == field(show, "id") {
			row = n
		}
	}
	if row == nil {
		t.Fatal("network list --provider --name public has no row for public")
	}
	for col, key := range map[string]string{
		"Network Type":              "provider:network_type",
		"Provider Physical Network": "provider:physical_network",
		"Provider Segment":          "provider:segmentation_id",
	} {
		if field(row, col) != field(show, key) {
			t.Errorf("network list %s = %q, network show %s = %q", col, field(row, col), key, field(show, key))
		}
	}
	if field(row, "Provider Physical Network") == "" {
		t.Errorf("public has no physical network: %v", row)
	}

	// -c selects one without --provider, on the short listing.
	rows := r.list(t, "network", "list", "--name", "public", "-c", "Name", "-c", "Provider Physical Network")
	if len(rows) != 1 || field(rows[0], "Provider Physical Network") != field(show, "provider:physical_network") {
		t.Errorf("network list -c 'Provider Physical Network' = %v", rows)
	}
}

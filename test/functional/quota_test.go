//go:build functional

package functional

import "testing"

// quota is the one noun that spans nova, cinder and neutron. It is exercised on
// a project of its own, so no other suite's limits move under it.
func TestQuota(t *testing.T) {
	c := ft(t)
	requireFeature(t, "core")
	r := defaultRunner(c)

	proj := uniq("quota")
	p := r.show(t, "project", "create", proj)
	id := field(p, "id")
	t.Cleanup(func() { r.run(t, "project", "delete", id) })

	r.ok(t, "quota", "set", id, "--cores", "7", "--gigabytes", "11", "--networks", "13")
	q := r.show(t, "quota", "show", id)
	if field(q, "cores") != "7" || field(q, "gigabytes") != "11" || anyField(q, "network", "networks") != "13" {
		t.Errorf("quota show after set = %v", q)
	}
	if q = r.show(t, "quota", "show", id, "--network"); field(q, "cores") != "" || anyField(q, "network", "networks") != "13" {
		t.Errorf("quota show --network = %v, want neutron's quotas only", q)
	}
	if d := r.show(t, "quota", "show", "--default", "--compute"); field(d, "cores") == "" || field(d, "cores") == "7" {
		t.Errorf("quota show --default --compute = %v, want the default, not the project's 7", d)
	}
	// neutron serves defaults too, so a bare --default covers all three services.
	if d := r.show(t, "quota", "show", id, "--default"); anyField(d, "network", "networks") == "" ||
		anyField(d, "network", "networks") == "13" || field(d, "gigabytes") == "" {
		t.Errorf("quota show --default = %v, want every service's defaults", d)
	}
}

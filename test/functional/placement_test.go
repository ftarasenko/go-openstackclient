//go:build functional

package functional

import (
	"slices"
	"strings"
	"testing"
)

// The placement suite, on providers, classes and traits of its own: nova's
// compute provider is never written to, so a failure here cannot take the
// node's capacity with it.

// customName is a placement custom name (CUSTOM_ and upper case, digits and
// underscores only), unique per call.
func customName(prefix string) string {
	return "CUSTOM_" + strings.ToUpper(strings.NewReplacer("-", "_").Replace(uniq(prefix)))
}

// Synthetic UUIDs placement takes on trust: an aggregate is only a UUID to it,
// and a consumer is whatever the allocation names.
const (
	ftAggregate = "5f0c9a52-7c1e-4b8e-9a61-2d3e4f5a6b7c"
	ftConsumer  = "0e1f2a3b-4c5d-4e6f-8a9b-0c1d2e3f4a5b"
)

func TestPlacementProvidersAndInventory(t *testing.T) {
	c := ft(t)
	requireFeature(t, "core")
	r := defaultRunner(c)

	class := customName("rc")
	r.ok(t, "resource", "class", "create", class)
	t.Cleanup(func() { r.run(t, "resource", "class", "delete", class) })
	if got := r.show(t, "resource", "class", "show", class); field(got, "name") != class {
		t.Errorf("resource class show = %v", got)
	}
	if !slices.Contains(column(r.list(t, "resource", "class", "list"), "name"), class) {
		t.Errorf("resource class list does not list %s", class)
	}
	// set is PUT: it creates a class that is not there, and is a no-op on one that is.
	setClass := customName("rcset")
	r.ok(t, "resource", "class", "set", setClass)
	r.ok(t, "resource", "class", "set", setClass)
	r.ok(t, "resource", "class", "delete", setClass)
	r.fails(t, "resource", "class", "show", setClass)

	name := uniq("rp")
	p := r.show(t, "resource", "provider", "create", name)
	rp := field(p, "uuid")
	t.Cleanup(func() { r.run(t, "resource", "provider", "delete", rp) })
	child := r.show(t, "resource", "provider", "create", name+"-child", "--parent-provider", rp)
	childID := field(child, "uuid")
	t.Cleanup(func() { r.run(t, "resource", "provider", "delete", childID) })
	if field(child, "parent_provider_uuid") != rp || field(child, "root_provider_uuid") != rp {
		t.Errorf("child provider = %v, want parent and root %s", child, rp)
	}
	// Re-parenting to nothing makes the child a root (placement 1.37).
	r.ok(t, "resource", "provider", "set", childID, "--name", name+"-orphan", "--parent-provider", "")
	if got := r.show(t, "resource", "provider", "show", childID); field(got, "name") != name+"-orphan" ||
		field(got, "root_provider_uuid") != childID {
		t.Errorf("after provider set: %v", got)
	}
	r.ok(t, "resource", "provider", "delete", childID)
	r.fails(t, "resource", "provider", "show", childID)
	if rows := r.list(t, "resource", "provider", "list", "--name", name); len(rows) != 1 || field(rows[0], "uuid") != rp {
		t.Errorf("resource provider list --name = %v", rows)
	}

	// Inventory: the whole set, then one class, then gone again.
	r.ok(t, "resource", "provider", "inventory", "set", rp, "--resource", class+":total=10",
		"--resource", class+":max_unit=5", "--resource", "VCPU:total=4")
	if inv := r.list(t, "resource", "provider", "inventory", "list", rp); !in(inv, class) || !in(inv, "VCPU") {
		t.Errorf("inventory list after set = %v", inv)
	}
	r.ok(t, "resource", "provider", "inventory", "class", "set", rp, class, "--total", "20", "--reserved", "1",
		"--max_unit", "5", "--allocation_ratio", "1.0")
	if inv := r.show(t, "resource", "provider", "inventory", "show", rp, class); field(inv, "total") != "20" ||
		field(inv, "reserved") != "1" {
		t.Errorf("inventory show after class set = %v", inv)
	}
	r.ok(t, "resource", "provider", "inventory", "delete", rp, "--resource-class", "VCPU")
	if inv := r.list(t, "resource", "provider", "inventory", "list", rp); in(inv, "VCPU") || !in(inv, class) {
		t.Errorf("inventory list after deleting VCPU = %v", inv)
	}

	// Aggregates are bare UUIDs to placement.
	r.ok(t, "resource", "provider", "aggregate", "set", rp, "--aggregate", ftAggregate)
	if aggs := r.list(t, "resource", "provider", "aggregate", "list", rp); !in(aggs, ftAggregate) {
		t.Errorf("aggregate list = %v, want %s", aggs, ftAggregate)
	}

	// An allocation against the provider, as a consumer of our own making.
	who := issue(t, r)
	r.ok(t, "resource", "provider", "allocation", "set", ftConsumer, "--allocation", "rp="+rp+","+class+"=2",
		"--project-id", who.project, "--user-id", who.user, "--consumer-type", "INSTANCE")
	t.Cleanup(func() { r.run(t, "resource", "provider", "allocation", "delete", ftConsumer) })
	if a := r.list(t, "resource", "provider", "allocation", "show", ftConsumer); !in(a, rp) || !in(a, "2") {
		t.Errorf("allocation show = %v, want 2 of %s on %s", a, class, rp)
	}
	if u := r.list(t, "resource", "provider", "usage", "show", rp); !in(u, class) || !in(u, "2") {
		t.Errorf("provider usage show = %v", u)
	}
	if u := r.list(t, "resource", "usage", "show", who.project, "--user-id", who.user); !in(u, class) {
		t.Errorf("resource usage show = %v, want %s", u, class)
	}
	if got := r.ok(t, "resource", "provider", "show", rp, "--allocations", "-f", "json"); !strings.Contains(got, ftConsumer) {
		t.Errorf("provider show --allocations = %s, want consumer %s", got, ftConsumer)
	}
	cands := r.list(t, "allocation", "candidate", "list", "--resource", class+"=3", "--member-of", ftAggregate, "--limit", "5")
	if !in(cands, rp) {
		t.Errorf("allocation candidate list = %v, want %s", cands, rp)
	}
	r.ok(t, "resource", "provider", "allocation", "unset", ftConsumer, "--resource-class", class)
	if a := r.list(t, "resource", "provider", "allocation", "show", ftConsumer); len(a) != 0 {
		t.Errorf("allocation show after unset = %v", a)
	}
	r.ok(t, "resource", "provider", "allocation", "delete", ftConsumer)
	r.ok(t, "resource", "provider", "delete", rp)
	r.fails(t, "resource", "provider", "show", rp)
}

func TestPlacementTraits(t *testing.T) {
	c := ft(t)
	requireFeature(t, "core")
	r := defaultRunner(c)

	trait := customName("trait")
	r.ok(t, "trait", "create", trait)
	t.Cleanup(func() { r.run(t, "trait", "delete", trait) })
	if got := r.show(t, "trait", "show", trait); field(got, "name") != trait {
		t.Errorf("trait show = %v", got)
	}
	if !slices.Contains(column(r.list(t, "trait", "list", "--name", "startswith:CUSTOM_FT_TRAIT"), "name"), trait) {
		t.Errorf("trait list --name startswith: does not list %s", trait)
	}

	p := r.show(t, "resource", "provider", "create", uniq("rp-traits"))
	rp := field(p, "uuid")
	t.Cleanup(func() { r.run(t, "resource", "provider", "delete", rp) })
	r.ok(t, "resource", "provider", "trait", "set", rp, "--trait", trait, "--trait", "HW_CPU_X86_AVX2")
	if tr := column(r.list(t, "resource", "provider", "trait", "list", rp), "name"); !slices.Contains(tr, trait) || !slices.Contains(tr, "HW_CPU_X86_AVX2") {
		t.Errorf("provider trait list = %v", tr)
	}
	if !slices.Contains(column(r.list(t, "trait", "list", "--associated"), "name"), trait) {
		t.Errorf("trait list --associated does not list %s", trait)
	}
	r.fails(t, "trait", "delete", trait) // still on a provider
	r.ok(t, "resource", "provider", "trait", "delete", rp)
	if tr := r.list(t, "resource", "provider", "trait", "list", rp); len(tr) != 0 {
		t.Errorf("provider trait list after delete = %v", tr)
	}
	r.ok(t, "trait", "delete", trait)
	r.fails(t, "trait", "show", trait)
}

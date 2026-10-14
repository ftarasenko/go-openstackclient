//go:build functional

package functional

import (
	"testing"
)

// The standalone ironic nouns: inspection rules, volume connectors and
// targets, shards. Like baremetal_test.go they run against devstack's
// API-only ironic.

func TestBaremetalInspectionRules(t *testing.T) {
	c := ft(t)
	requireIronicAPI(t, "1.96")
	r := defaultRunner(c)

	rule := r.show(t, "baremetal", "inspection", "rule", "create",
		"--description", "koc functional", "--priority", "10",
		"--conditions", `[{"op":"is-true","args":{"value":true}}]`,
		"--actions", `[{"op":"add-trait","args":{"name":"CUSTOM_FT"}}]`)
	id := field(rule, "uuid")
	t.Cleanup(func() { r.run(t, "baremetal", "inspection", "rule", "delete", id) })
	if field(rule, "priority") != "10" || len(items(rule, "actions")) != 1 || len(items(rule, "conditions")) != 1 {
		t.Errorf("inspection rule create = %v", rule)
	}

	rows := r.list(t, "baremetal", "inspection", "rule", "list", "--long")
	if !in(rows, id) {
		t.Errorf("inspection rule list --long has no %s: %v", id, rows)
	}

	r.ok(t, "baremetal", "inspection", "rule", "set", id, "--priority", "20", "--description", "koc functional 2",
		"--actions", `[{"op":"log","args":{"msg":"koc"}},{"op":"add-trait","args":{"name":"CUSTOM_FT"}}]`)
	got := r.show(t, "baremetal", "inspection", "rule", "show", id)
	if field(got, "priority") != "20" || field(got, "description") != "koc functional 2" || len(items(got, "actions")) != 2 {
		t.Errorf("after inspection rule set: %v", got)
	}

	r.ok(t, "baremetal", "inspection", "rule", "unset", id, "--description", "--action", "0", "--condition", "0")
	got = r.show(t, "baremetal", "inspection", "rule", "show", id)
	if field(got, "description") != "" || len(items(got, "actions")) != 1 || len(items(got, "conditions")) != 0 {
		t.Errorf("after inspection rule unset: %v", got)
	}

	r.ok(t, "baremetal", "inspection", "rule", "delete", id)
	r.fails(t, "baremetal", "inspection", "rule", "show", id)
}

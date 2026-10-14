//go:build functional

package functional

import (
	"strings"
	"testing"
)

// Deploy templates and runbooks are step documents stored by ironic. ironic
// validates their schema on write, not against a driver, so the fake-hardware
// stack can drive their whole lifecycle.

const ftSteps = `[{"interface":"bios","step":"apply_configuration","priority":150,` +
	`"args":{"settings":[{"name":"LogicalProc","value":"Enabled"}]}}]`

// customTrait returns a unique CUSTOM_ trait name, the form ironic requires
// of a deploy template's name.
func customTrait(prefix string) string {
	return "CUSTOM_" + strings.ToUpper(strings.ReplaceAll(uniq(prefix), "-", "_"))
}

// itemObject reads element i of a JSON array member as an object; nil when
// absent.
func itemObject(m map[string]any, key string, i int) map[string]any {
	if l := items(m, key); i < len(l) {
		v, _ := l[i].(map[string]any)
		return v
	}
	return nil
}

func TestBaremetalDeployTemplates(t *testing.T) {
	c := ft(t)
	requireIronicAPI(t, "1.55")
	r := defaultRunner(c)

	name := customTrait("dt")
	tmpl := r.show(t, "baremetal", "deploy", "template", "create", name, "--steps", ftSteps, "--extra", "team=ops")
	id := field(tmpl, "uuid")
	t.Cleanup(func() { r.run(t, "baremetal", "deploy", "template", "delete", id) })
	if steps := items(tmpl, "steps"); len(steps) != 1 || field(object(tmpl, "extra"), "team") != "ops" {
		t.Errorf("deploy template create = %v", tmpl)
	}

	if got := r.show(t, "baremetal", "deploy", "template", "show", name); field(got, "uuid") != id {
		t.Errorf("deploy template show = %v", got)
	}
	rows := r.list(t, "baremetal", "deploy", "template", "list", "--long")
	if !in(rows, id) {
		t.Errorf("deploy template list --long has no %s: %v", id, rows)
	}

	renamed := name + "_B"
	stdinSteps := `[{"interface":"raid","step":"delete_configuration","priority":200,"args":{}}]`
	r.withStdin(stdinSteps).ok(t, "baremetal", "deploy", "template", "set", id, "--name", renamed, "--steps", "-",
		"--extra", "rack=r1")
	got := r.show(t, "baremetal", "deploy", "template", "show", id)
	if field(got, "name") != renamed || field(object(got, "extra"), "rack") != "r1" ||
		field(itemObject(got, "steps", 0), "step") != "delete_configuration" {
		t.Errorf("after deploy template set: %v", got)
	}

	r.ok(t, "baremetal", "deploy", "template", "unset", id, "--extra", "rack", "--extra", "team")
	if got := r.show(t, "baremetal", "deploy", "template", "show", id); len(object(got, "extra")) != 0 {
		t.Errorf("after deploy template unset: %v", got)
	}

	r.ok(t, "baremetal", "deploy", "template", "delete", id)
	r.fails(t, "baremetal", "deploy", "template", "show", id)
}

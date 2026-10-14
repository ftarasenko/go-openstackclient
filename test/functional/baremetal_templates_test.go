//go:build functional

package functional

import (
	"slices"
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
	r := systemRunner(c)

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

const ftRunbookSteps = `[{"interface":"bios","step":"apply_configuration","order":1,` +
	`"args":{"settings":[{"name":"LogicalProc","value":"Enabled"}]}}]`

func TestBaremetalRunbooks(t *testing.T) {
	c := ft(t)
	requireIronicAPI(t, "1.92")
	r := systemRunner(c)

	// Below 1.112 a runbook's name must be a trait; a CUSTOM_ name is valid
	// on every version.
	name := customTrait("rb")
	rb := r.show(t, "baremetal", "runbook", "create", "--name", name, "--steps", ftRunbookSteps,
		"--extra", "team=ops", "--public=false")
	id := field(rb, "uuid")
	t.Cleanup(func() { r.run(t, "baremetal", "runbook", "delete", id) })
	if len(items(rb, "steps")) != 1 || field(rb, "public") != "false" || field(object(rb, "extra"), "team") != "ops" {
		t.Errorf("runbook create = %v", rb)
	}

	if got := r.show(t, "baremetal", "runbook", "show", name); field(got, "uuid") != id {
		t.Errorf("runbook show = %v", got)
	}
	if rows := r.list(t, "baremetal", "runbook", "list", "--long"); !in(rows, id) {
		t.Errorf("runbook list --long has no %s: %v", id, rows)
	}

	// ironic refuses an owner on a public runbook, so the two are set apart.
	r.ok(t, "baremetal", "runbook", "set", id, "--owner", "ft-owner", "--disable-ramdisk", "--extra", "rack=r1")
	got := r.show(t, "baremetal", "runbook", "show", id)
	if field(got, "owner") != "ft-owner" || field(got, "disable_ramdisk") != "true" || field(object(got, "extra"), "rack") != "r1" {
		t.Errorf("after runbook set: %v", got)
	}
	r.ok(t, "baremetal", "runbook", "unset", id, "--owner", "--disable-ramdisk", "--extra", "rack")
	got = r.show(t, "baremetal", "runbook", "show", id)
	if field(got, "owner") != "" || field(got, "disable_ramdisk") == "true" || field(object(got, "extra"), "rack") != "" {
		t.Errorf("after runbook unset: %v", got)
	}
	r.ok(t, "baremetal", "runbook", "set", id, "--public")
	if got := r.show(t, "baremetal", "runbook", "show", id); field(got, "public") != "true" {
		t.Errorf("after runbook set --public: %v", got)
	}
	r.ok(t, "baremetal", "runbook", "unset", id, "--public")
	if got := r.show(t, "baremetal", "runbook", "show", id); field(got, "public") == "true" {
		t.Errorf("after runbook unset --public: %v", got)
	}

	t.Run("traits", func(t *testing.T) {
		requireIronicAPI(t, "1.112")
		r.ok(t, "baremetal", "runbook", "set", id, "--description", "koc functional")
		if got := r.show(t, "baremetal", "runbook", "show", id); field(got, "description") != "koc functional" {
			t.Errorf("runbook set --description: %v", got)
		}
		r.ok(t, "baremetal", "runbook", "trait", "add", id, "CUSTOM_FT_A", "CUSTOM_FT_B")
		traits := column(r.list(t, "baremetal", "runbook", "trait", "list", id), "Traits")
		if !slices.Contains(traits, "CUSTOM_FT_A") || !slices.Contains(traits, "CUSTOM_FT_B") {
			t.Errorf("runbook trait list = %v", traits)
		}
		r.ok(t, "baremetal", "runbook", "trait", "remove", id, "CUSTOM_FT_A")
		if traits := column(r.list(t, "baremetal", "runbook", "trait", "list", id), "Traits"); slices.Contains(traits, "CUSTOM_FT_A") {
			t.Errorf("after runbook trait remove: %v", traits)
		}
		r.ok(t, "baremetal", "runbook", "trait", "remove", id, "--all")
		if traits := r.list(t, "baremetal", "runbook", "trait", "list", id); len(traits) != 0 {
			t.Errorf("after runbook trait remove --all: %v", traits)
		}
		r.ok(t, "baremetal", "runbook", "unset", id, "--description")
	})

	r.ok(t, "baremetal", "runbook", "delete", id)
	r.fails(t, "baremetal", "runbook", "show", id)
}

package baremetal

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

const testTemplateUUID = "dddddddd-0000-0000-0000-000000000001"

var testSteps = []any{map[string]any{
	"interface": "bios", "step": "apply_configuration", "priority": float64(150),
	"args": map[string]any{"settings": []any{map[string]any{"name": "LogicalProc", "value": "Enabled"}}},
}}

const testStepsJSON = `[{"interface":"bios","step":"apply_configuration","priority":150,` +
	`"args":{"settings":[{"name":"LogicalProc","value":"Enabled"}]}}]`

func testTemplate() map[string]any {
	return map[string]any{
		"uuid": testTemplateUUID, "name": "CUSTOM_HYPERTHREADING_ON", "steps": testSteps,
		"extra": map[string]any{"owner": "ops"}, "created_at": "2026-10-14T00:00:00+00:00",
		"updated_at": nil, "links": []any{},
	}
}

func TestRunDeployTemplateList_ShortAndLong(t *testing.T) {
	client, fake := newFakeIronic(t, map[string]fakeRoute{
		"GET /deploy_templates": {http.StatusOK, map[string]any{"deploy_templates": []any{testTemplate()}}},
	})
	var buf bytes.Buffer
	if err := runDeployTemplateList(context.Background(), client, &output.Options{Format: output.FormatValue},
		&resourceListFlags{limit: 5, sortKey: "name"}, &buf); err != nil {
		t.Fatal(err)
	}
	if q := fake.call("GET /deploy_templates").Query; q != "limit=5&sort_key=name" {
		t.Errorf("query = %q", q)
	}
	if buf.String() != testTemplateUUID+" CUSTOM_HYPERTHREADING_ON\n" {
		t.Errorf("short output = %q", buf.String())
	}

	buf.Reset()
	if err := runDeployTemplateList(context.Background(), client, &output.Options{Format: output.FormatJSON},
		&resourceListFlags{long: true}, &buf); err != nil {
		t.Fatal(err)
	}
	if q := fake.calls[len(fake.calls)-1].Query; q != "detail=True" {
		t.Errorf("--long query = %q, want detail=True (ironic has no /deploy_templates/detail)", q)
	}
	rows := decodeJSON[[]map[string]any](t, buf.String())
	jsonEq(t, "Steps", rows[0]["Steps"], testStepsJSON)
	for _, col := range []string{"UUID", "Name", "Steps", "Extra", "Created At", "Updated At"} {
		if _, ok := rows[0][col]; !ok {
			t.Errorf("--long has no %q column", col)
		}
	}
}

func TestRunDeployTemplateShow(t *testing.T) {
	client, fake := newFakeIronic(t, map[string]fakeRoute{
		"GET /deploy_templates/CUSTOM_HYPERTHREADING_ON": {http.StatusOK, testTemplate()},
	})
	var buf bytes.Buffer
	if err := runDeployTemplateShow(context.Background(), client, &output.Options{Format: output.FormatJSON},
		"CUSTOM_HYPERTHREADING_ON", &buf); err != nil {
		t.Fatal(err)
	}
	fake.call("GET /deploy_templates/CUSTOM_HYPERTHREADING_ON")
	got := decodeJSON[map[string]any](t, buf.String())
	if got["uuid"] != testTemplateUUID {
		t.Errorf("uuid = %v", got["uuid"])
	}
	if _, ok := got["links"]; ok {
		t.Error("show renders links")
	}
}

func TestRunDeployTemplateCreate_StepsFromFileAndStdin(t *testing.T) {
	file := filepath.Join(t.TempDir(), "steps.yaml")
	yamlSteps := "- interface: bios\n  step: apply_configuration\n  priority: 150\n" +
		"  args:\n    settings:\n      - name: LogicalProc\n        value: Enabled\n"
	if err := os.WriteFile(file, []byte(yamlSteps), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		steps string
		stdin string
	}{
		"json":  {testStepsJSON, ""},
		"file":  {file, ""},
		"stdin": {"-", testStepsJSON},
	} {
		t.Run(name, func(t *testing.T) {
			client, fake := newFakeIronic(t, map[string]fakeRoute{
				"POST /deploy_templates": {http.StatusCreated, testTemplate()},
			})
			f := &deployTemplateFlags{name: "CUSTOM_HYPERTHREADING_ON", steps: tc.steps, uuid: testTemplateUUID, extra: []string{"owner=ops"}}
			var buf bytes.Buffer
			o := &output.Options{Format: output.FormatValue, Columns: []string{"name"}}
			if err := runDeployTemplateCreate(context.Background(), client, o, f, strings.NewReader(tc.stdin), &buf); err != nil {
				t.Fatal(err)
			}
			jsonEq(t, "create body", fake.call("POST /deploy_templates").Body, `{
				"name": "CUSTOM_HYPERTHREADING_ON", "uuid": "`+testTemplateUUID+`",
				"extra": {"owner": "ops"}, "steps": `+testStepsJSON+`}`)
			if buf.String() != "CUSTOM_HYPERTHREADING_ON\n" {
				t.Errorf("output = %q", buf.String())
			}
		})
	}
}

func TestRunDeployTemplateCreate_BadStepsSendNothing(t *testing.T) {
	client, fake := newFakeIronic(t, nil)
	f := &deployTemplateFlags{name: "CUSTOM_X", steps: `[{"step":"x"}]`}
	err := runDeployTemplateCreate(context.Background(), client, &output.Options{Format: output.FormatTable}, f, nil, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "interface") {
		t.Errorf("err = %v", err)
	}
	if len(fake.calls) != 0 {
		t.Errorf("sent %v", fake.calls)
	}
}

func TestRunDeployTemplateSetUnset_PatchBodies(t *testing.T) {
	client, fake := newFakeIronic(t, map[string]fakeRoute{
		"PATCH /deploy_templates/CUSTOM_A": {http.StatusOK, testTemplate()},
	})
	o := &output.Options{Format: output.FormatValue, Columns: []string{"uuid"}}
	f := &deployTemplateFlags{name: "CUSTOM_B", steps: testStepsJSON, extra: []string{"k=v"}}
	if err := runDeployTemplateSet(context.Background(), client, o, "CUSTOM_A", f, nil, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	jsonEq(t, "set patch", fake.call("PATCH /deploy_templates/CUSTOM_A").Body, `[
		{"op":"add","path":"/name","value":"CUSTOM_B"},
		{"op":"add","path":"/steps","value":`+testStepsJSON+`},
		{"op":"add","path":"/extra/k","value":"v"}]`)

	fake.calls = nil
	if err := runDeployTemplateUnset(context.Background(), client, o, "CUSTOM_A", []string{"k", "a/b"}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	jsonEq(t, "unset patch", fake.call("PATCH /deploy_templates/CUSTOM_A").Body,
		`[{"op":"remove","path":"/extra/k"},{"op":"remove","path":"/extra/a~1b"}]`)

	if err := runDeployTemplateSet(context.Background(), client, o, "CUSTOM_A", &deployTemplateFlags{}, nil, &bytes.Buffer{}); err == nil {
		t.Error("set with no flags should fail")
	}
}

func TestRunDeployTemplateDelete_Each(t *testing.T) {
	client, _ := newFakeIronic(t, map[string]fakeRoute{
		"DELETE /deploy_templates/CUSTOM_A": {http.StatusNoContent, nil},
		"DELETE /deploy_templates/CUSTOM_B": {http.StatusNotFound, map[string]any{"error_message": "not found"}},
	})
	var buf bytes.Buffer
	err := runDeployTemplateDelete(context.Background(), client, []string{"CUSTOM_A", "CUSTOM_B"}, &buf)
	if err == nil || !strings.Contains(err.Error(), "CUSTOM_B") {
		t.Errorf("err = %v", err)
	}
	if buf.String() != "Deleted deploy template CUSTOM_A\n" {
		t.Errorf("output = %q", buf.String())
	}
}

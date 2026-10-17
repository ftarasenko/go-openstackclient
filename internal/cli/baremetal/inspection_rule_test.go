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

const testRuleUUID = "77777777-0000-0000-0000-000000000001"

func testInspectionRule() map[string]any {
	return map[string]any{
		"uuid": testRuleUUID, "description": "tag nvme", "priority": 10, "sensitive": false, "phase": "main",
		"actions":    []any{map[string]any{"op": "add-trait", "args": map[string]any{"name": "CUSTOM_NVME"}}},
		"conditions": []any{map[string]any{"op": "is-true", "args": map[string]any{"value": "{inventory[disks]}"}}},
		"links":      []any{},
	}
}

func TestRunInspectionRuleList_ShortAndLong(t *testing.T) {
	for _, tc := range []struct {
		long  bool
		query string
		cols  int
	}{
		{false, "limit=5", 2},
		{true, "detail=true&limit=5", len(inspectionRuleDetailFields)},
	} {
		client, fake := newFakeIronic(t, map[string]fakeRoute{
			"GET /inspection_rules": {http.StatusOK, map[string]any{"inspection_rules": []any{testInspectionRule()}}},
		})
		o := &output.Options{Format: output.FormatJSON}
		var buf bytes.Buffer
		if err := runInspectionRuleList(context.Background(), client, o, &inspectionRuleListFlags{long: tc.long, limit: 5}, &buf); err != nil {
			t.Fatal(err)
		}
		if q := fake.call("GET /inspection_rules").Query; q != tc.query {
			t.Errorf("long=%v: query = %q, want %q", tc.long, q, tc.query)
		}
		rows := decodeJSON[[]map[string]any](t, buf.String())
		if len(rows) != 1 || len(rows[0]) != tc.cols || rows[0]["Description"] != "tag nvme" {
			t.Errorf("long=%v: rows = %v", tc.long, rows)
		}
	}
}

func TestRunInspectionRuleShow(t *testing.T) {
	client, _ := newFakeIronic(t, map[string]fakeRoute{
		"GET /inspection_rules/" + testRuleUUID: {http.StatusOK, testInspectionRule()},
	})
	o := &output.Options{Format: output.FormatJSON}
	var buf bytes.Buffer
	if err := runInspectionRuleShow(context.Background(), client, o, testRuleUUID, &buf); err != nil {
		t.Fatal(err)
	}
	got := decodeJSON[map[string]any](t, buf.String())
	jsonEq(t, "actions", got["actions"], `[{"op":"add-trait","args":{"name":"CUSTOM_NVME"}}]`)
	if _, ok := got["links"]; ok {
		t.Error("show renders links")
	}
	if _, ok := got["created_at"]; !ok {
		t.Error("show has no created_at key")
	}
}

func TestRunInspectionRuleCreate_BodyFromInlineFileAndStdin(t *testing.T) {
	client, fake := newFakeIronic(t, map[string]fakeRoute{
		"POST /inspection_rules": {http.StatusCreated, testInspectionRule()},
	})
	dir := t.TempDir()
	condFile := filepath.Join(dir, "conditions.yaml")
	if err := os.WriteFile(condFile, []byte("- op: is-true\n  args: {value: '{inventory[disks]}'}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	o := &output.Options{Format: output.FormatValue, Columns: []string{"uuid"}}
	var buf bytes.Buffer
	f := &inspectionRuleCreateFlags{inspectionRuleAttrFlags: inspectionRuleAttrFlags{
		description: "tag nvme", descriptionSet: true, priority: 10, prioritySet: true, phase: "main",
		actions: "-", conditions: condFile,
	}, sensitive: "true"}
	stdin := strings.NewReader(`[{"op":"add-trait","args":{"name":"CUSTOM_NVME"}}]`)
	if err := runInspectionRuleCreate(context.Background(), client, o, f, stdin, &buf); err != nil {
		t.Fatal(err)
	}
	jsonEq(t, "create body", fake.call("POST /inspection_rules").Body, `{
		"description":"tag nvme","priority":10,"phase":"main","sensitive":true,
		"actions":[{"op":"add-trait","args":{"name":"CUSTOM_NVME"}}],
		"conditions":[{"op":"is-true","args":{"value":"{inventory[disks]}"}}]}`)
	if buf.String() != testRuleUUID+"\n" {
		t.Errorf("output = %q", buf.String())
	}
}

func TestRunInspectionRuleCreate_BadJSONAndSensitive(t *testing.T) {
	client, _ := newFakeIronic(t, nil)
	o := &output.Options{Format: output.FormatTable}
	err := runInspectionRuleCreate(context.Background(), client, o, &inspectionRuleCreateFlags{inspectionRuleAttrFlags: inspectionRuleAttrFlags{actions: "not json"}}, nil, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "cannot be parsed as JSON") {
		t.Errorf("err = %v", err)
	}
	err = runInspectionRuleCreate(context.Background(), client, o, &inspectionRuleCreateFlags{inspectionRuleAttrFlags: inspectionRuleAttrFlags{actions: "[]"}, sensitive: "maybe"}, nil, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "--sensitive") {
		t.Errorf("err = %v", err)
	}
}

func TestRunInspectionRuleSetUnset_PatchBodies(t *testing.T) {
	client, fake := newFakeIronic(t, map[string]fakeRoute{
		"PATCH /inspection_rules/" + testRuleUUID: {http.StatusOK, testInspectionRule()},
	})
	o := &output.Options{Format: output.FormatValue, Columns: []string{"uuid"}}
	f := &inspectionRuleAttrFlags{priority: 0, prioritySet: true, actions: `[{"op":"fail","args":{"msg":"x"}}]`}
	if err := runInspectionRuleSet(context.Background(), client, o, testRuleUUID, f, nil, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	jsonEq(t, "set patch", fake.call("PATCH /inspection_rules/"+testRuleUUID).Body, `[
		{"op":"add","path":"/priority","value":0},
		{"op":"add","path":"/actions","value":[{"op":"fail","args":{"msg":"x"}}]}]`)

	fake.calls = nil
	u := &inspectionRuleUnsetFlags{description: true, phase: true, actions: []int{0, 2, 2}, conditions: []int{1}}
	if err := runInspectionRuleUnset(context.Background(), client, o, testRuleUUID, u, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	jsonEq(t, "unset patch", fake.call("PATCH /inspection_rules/"+testRuleUUID).Body, `[
		{"op":"remove","path":"/description"},{"op":"remove","path":"/phase"},
		{"op":"remove","path":"/actions/2"},{"op":"remove","path":"/actions/0"},
		{"op":"remove","path":"/conditions/1"}]`)

	if err := runInspectionRuleSet(context.Background(), client, o, testRuleUUID, &inspectionRuleAttrFlags{}, nil, &bytes.Buffer{}); err == nil {
		t.Error("set with no flags should fail")
	}
}

// Below 1.96 ironic refuses a write to the whole route with 405.
func TestRunInspectionRuleCreate_OldCloudNamesVersion(t *testing.T) {
	client, fake := newFakeIronic(t, map[string]fakeRoute{
		"POST /inspection_rules": {http.StatusMethodNotAllowed, map[string]any{"error_message": "The API version does not allow inspection rules"}},
	})
	fake.max = "1.82"
	o := &output.Options{Format: output.FormatTable}
	err := runInspectionRuleCreate(context.Background(), client, o, &inspectionRuleCreateFlags{inspectionRuleAttrFlags: inspectionRuleAttrFlags{actions: "[]"}}, nil, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "requires ironic API 1.96 (OpenStack 2025.1); this cloud supports up to 1.82") {
		t.Errorf("err = %v", err)
	}
}

func TestRunInspectionRuleDelete_Each(t *testing.T) {
	client, fake := newFakeIronic(t, map[string]fakeRoute{
		"DELETE /inspection_rules/r1": {http.StatusNoContent, nil},
		"DELETE /inspection_rules/r2": {http.StatusNotFound, map[string]any{"error_message": "not found"}},
	})
	var buf bytes.Buffer
	err := runInspectionRuleDelete(context.Background(), client, []string{"r1", "r2"}, &buf)
	if err == nil || !strings.Contains(err.Error(), "r2") {
		t.Errorf("err = %v", err)
	}
	if buf.String() != "Deleted inspection rule r1\n" {
		t.Errorf("output = %q", buf.String())
	}
	fake.call("DELETE /inspection_rules/r1")
}

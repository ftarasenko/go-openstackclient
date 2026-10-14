package baremetal

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

const (
	testRunbookUUID  = "eeeeeeee-0000-0000-0000-000000000001"
	testRunbookSteps = `[{"interface":"bios","step":"apply_configuration","order":1,"args":{}}]`
)

func testRunbook() map[string]any {
	return map[string]any{
		"uuid": testRunbookUUID, "name": "CUSTOM_FIRMWARE_UPDATE", "description": "fw",
		"traits": []any{"CUSTOM_FIRMWARE_UPDATE"}, "disable_ramdisk": false, "owner": nil,
		"public": true, "steps": []any{}, "extra": map[string]any{}, "links": []any{},
	}
}

func TestRunRunbookList_ShortAndLong(t *testing.T) {
	client, fake := newFakeIronic(t, map[string]fakeRoute{
		"GET /runbooks": {http.StatusOK, map[string]any{"runbooks": []any{
			map[string]any{"uuid": testRunbookUUID, "name": "CUSTOM_FIRMWARE_UPDATE"},
		}}},
		"GET /runbooks/detail": {http.StatusOK, map[string]any{"runbooks": []any{testRunbook()}}},
	})
	var buf bytes.Buffer
	if err := runRunbookList(context.Background(), client, &output.Options{Format: output.FormatValue},
		&resourceListFlags{marker: testRunbookUUID}, &buf); err != nil {
		t.Fatal(err)
	}
	if q := fake.call("GET /runbooks").Query; q != "marker="+testRunbookUUID {
		t.Errorf("query = %q", q)
	}
	if buf.String() != testRunbookUUID+" CUSTOM_FIRMWARE_UPDATE\n" {
		t.Errorf("output = %q", buf.String())
	}

	buf.Reset()
	// A -c naming a detail column reads /runbooks/detail without --long.
	o := &output.Options{Format: output.FormatValue, Columns: []string{"Name", "Traits", "Public"}}
	if err := runRunbookList(context.Background(), client, o, &resourceListFlags{}, &buf); err != nil {
		t.Fatal(err)
	}
	if buf.String() != `CUSTOM_FIRMWARE_UPDATE ["CUSTOM_FIRMWARE_UPDATE"] true`+"\n" {
		t.Errorf("detail output = %q", buf.String())
	}
}

func TestRunRunbookShow_BelowMicroversionExplains(t *testing.T) {
	client, _ := newFakeIronic(t, map[string]fakeRoute{
		"GET /runbooks/rb": {http.StatusNotFound, nil},
	})
	// fakeIronic advertises a 1.96 maximum, which has runbooks: a 404 there is
	// a real "not found" and is left alone.
	err := runRunbookShow(context.Background(), client, &output.Options{Format: output.FormatTable}, "rb", &bytes.Buffer{})
	if err == nil || strings.Contains(err.Error(), "requires ironic API") {
		t.Errorf("err = %v, want the plain 404", err)
	}
}

func TestRunRunbookCreate_Body(t *testing.T) {
	client, fake := newFakeIronic(t, map[string]fakeRoute{
		"POST /runbooks": {http.StatusCreated, testRunbook()},
	})
	f := &runbookFlags{
		name: "CUSTOM_FIRMWARE_UPDATE", steps: testRunbookSteps, owner: "p1", extra: []string{"k=v"},
		public: false, publicSet: true, disableRamdisk: true, ramdiskSet: true,
	}
	o := &output.Options{Format: output.FormatValue, Columns: []string{"uuid"}}
	var buf bytes.Buffer
	if err := runRunbookCreate(context.Background(), client, o, f, nil, &buf); err != nil {
		t.Fatal(err)
	}
	jsonEq(t, "create body", fake.call("POST /runbooks").Body, `{
		"name": "CUSTOM_FIRMWARE_UPDATE", "steps": `+testRunbookSteps+`, "owner": "p1",
		"public": false, "disable_ramdisk": true, "extra": {"k": "v"}}`)
	if buf.String() != testRunbookUUID+"\n" {
		t.Errorf("output = %q", buf.String())
	}
}

func TestRunRunbookCreate_DescriptionOnOldCloudNamesVersion(t *testing.T) {
	client, _ := newFakeIronic(t, map[string]fakeRoute{
		"POST /runbooks": {http.StatusBadRequest, map[string]any{"error_message": "Additional properties are not allowed ('description')"}},
	})
	f := &runbookFlags{name: "CUSTOM_X", steps: testRunbookSteps, description: "d"}
	err := runRunbookCreate(context.Background(), client, &output.Options{Format: output.FormatTable}, f, nil, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "requires ironic API 1.112 (OpenStack 2026.2); this cloud supports up to 1.96") {
		t.Errorf("err = %v", err)
	}
}

func TestRunRunbookSetUnset_PatchBodies(t *testing.T) {
	client, fake := newFakeIronic(t, map[string]fakeRoute{
		"PATCH /runbooks/rb": {http.StatusOK, testRunbook()},
	})
	o := &output.Options{Format: output.FormatValue, Columns: []string{"uuid"}}
	f := &runbookFlags{
		name: "rb2", description: "d", owner: "p1", public: true, publicSet: true,
		steps: "-", extra: []string{"k=v"}, disableRamdisk: false, ramdiskSet: true,
	}
	if err := runRunbookSet(context.Background(), client, o, "rb", f, strings.NewReader(testRunbookSteps), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	jsonEq(t, "set patch", fake.call("PATCH /runbooks/rb").Body, `[
		{"op":"add","path":"/name","value":"rb2"},
		{"op":"add","path":"/description","value":"d"},
		{"op":"add","path":"/owner","value":"p1"},
		{"op":"add","path":"/public","value":true},
		{"op":"add","path":"/steps","value":`+testRunbookSteps+`},
		{"op":"add","path":"/extra/k","value":"v"},
		{"op":"add","path":"/disable_ramdisk","value":false}]`)

	fake.calls = nil
	u := &runbookUnsetFlags{name: true, description: true, public: true, owner: true, disableRamdisk: true, extra: []string{"k"}}
	if err := runRunbookUnset(context.Background(), client, o, "rb", u, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	jsonEq(t, "unset patch", fake.call("PATCH /runbooks/rb").Body, `[
		{"op":"remove","path":"/name"},{"op":"remove","path":"/owner"},{"op":"remove","path":"/public"},
		{"op":"remove","path":"/disable_ramdisk"},{"op":"remove","path":"/description"},
		{"op":"remove","path":"/extra/k"}]`)
}

func TestRunRunbookDelete_Each(t *testing.T) {
	client, _ := newFakeIronic(t, map[string]fakeRoute{
		"DELETE /runbooks/a": {http.StatusNoContent, nil},
		"DELETE /runbooks/b": {http.StatusNoContent, nil},
	})
	var buf bytes.Buffer
	if err := runRunbookDelete(context.Background(), client, []string{"a", "b"}, &buf); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "Deleted runbook a\nDeleted runbook b\n" {
		t.Errorf("output = %q", buf.String())
	}
}

func TestRunRunbookTraits(t *testing.T) {
	client, fake := newFakeIronic(t, map[string]fakeRoute{
		"GET /runbooks/rb/traits":                {http.StatusOK, map[string]any{"traits": []any{"CUSTOM_A", "CUSTOM_B"}}},
		"PUT /runbooks/rb/traits/CUSTOM_A":       {http.StatusNoContent, nil},
		"PUT /runbooks/rb/traits/CUSTOM_B":       {http.StatusNoContent, nil},
		"DELETE /runbooks/rb/traits/CUSTOM_A":    {http.StatusNoContent, nil},
		"DELETE /runbooks/rb/traits":             {http.StatusNoContent, nil},
		"DELETE /runbooks/old/traits/CUSTOM_OLD": {http.StatusNotFound, nil},
	})
	ctx := context.Background()
	var buf bytes.Buffer
	if err := runRunbookTraitList(ctx, client, &output.Options{Format: output.FormatValue}, "rb", &buf); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "CUSTOM_A\nCUSTOM_B\n" {
		t.Errorf("trait list = %q", buf.String())
	}

	buf.Reset()
	if err := runRunbookTraitAdd(ctx, client, "rb", []string{"CUSTOM_A", "CUSTOM_B"}, &buf); err != nil {
		t.Fatal(err)
	}
	if c := fake.call("PUT /runbooks/rb/traits/CUSTOM_A"); c.Body != nil {
		t.Errorf("trait add sent a body: %v", c.Body)
	}
	if buf.String() != "Added trait CUSTOM_A\nAdded trait CUSTOM_B\n" {
		t.Errorf("trait add output = %q", buf.String())
	}

	buf.Reset()
	if err := runRunbookTraitRemove(ctx, client, "rb", []string{"CUSTOM_A"}, false, &buf); err != nil {
		t.Fatal(err)
	}
	if err := runRunbookTraitRemove(ctx, client, "rb", nil, true, &buf); err != nil {
		t.Fatal(err)
	}
	fake.call("DELETE /runbooks/rb/traits")
	if buf.String() != "Removed trait CUSTOM_A\nRemoved all traits from runbook rb\n" {
		t.Errorf("trait remove output = %q", buf.String())
	}

	for _, tc := range []struct {
		traits []string
		all    bool
	}{{nil, false}, {[]string{"CUSTOM_A"}, true}} {
		if err := runRunbookTraitRemove(ctx, client, "rb", tc.traits, tc.all, &buf); err == nil {
			t.Errorf("trait remove %v --all=%v should fail", tc.traits, tc.all)
		}
	}
}

// fakeIronic advertises 1.96: the /traits sub-resource (1.112) is not routed
// there, and the 404 must say so rather than read as "no such runbook".
func TestRunRunbookTraitRemove_BelowMicroversionExplains(t *testing.T) {
	client, _ := newFakeIronic(t, map[string]fakeRoute{
		"DELETE /runbooks/old/traits/CUSTOM_OLD": {http.StatusNotFound, nil},
	})
	err := runRunbookTraitRemove(context.Background(), client, "old", []string{"CUSTOM_OLD"}, false, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "baremetal runbook trait requires ironic API 1.112 (OpenStack 2026.2); this cloud supports up to 1.96") {
		t.Errorf("err = %v", err)
	}
}

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

const testChassisUUID = "cccccccc-0000-0000-0000-000000000001"

func testChassis() map[string]any {
	return map[string]any{
		"uuid": testChassisUUID, "description": "rack 12", "extra": map[string]any{"row": "b"},
		"created_at": "2026-10-20T00:00:00+00:00", "updated_at": nil, "links": []any{}, "nodes": []any{},
	}
}

func TestRunChassisList_ShortAndDetail(t *testing.T) {
	client, fake := newFakeIronic(t, map[string]fakeRoute{
		"GET /chassis":        {http.StatusOK, map[string]any{"chassis": []any{testChassis()}}},
		"GET /chassis/detail": {http.StatusOK, map[string]any{"chassis": []any{testChassis()}}},
	})
	var buf bytes.Buffer
	if err := runChassisList(context.Background(), client, &output.Options{Format: output.FormatValue},
		&resourceListFlags{sortKey: "description", sortDir: "desc"}, &buf); err != nil {
		t.Fatal(err)
	}
	if q := fake.call("GET /chassis").Query; q != "sort_dir=desc&sort_key=description" {
		t.Errorf("query = %q", q)
	}
	if buf.String() != testChassisUUID+" rack 12\n" {
		t.Errorf("short output = %q", buf.String())
	}
	buf.Reset()
	if err := runChassisList(context.Background(), client, &output.Options{Format: output.FormatJSON},
		&resourceListFlags{long: true}, &buf); err != nil {
		t.Fatal(err)
	}
	fake.call("GET /chassis/detail")
	rows := decodeJSON[[]map[string]any](t, buf.String())
	for _, col := range []string{"UUID", "Description", "Created At", "Updated At", "Extra"} {
		if _, ok := rows[0][col]; !ok {
			t.Errorf("--long has no %q column: %v", col, rows[0])
		}
	}
}

// show drops the navigation-only "nodes" link, as upstream pops it.
func TestRunChassisShow(t *testing.T) {
	client, _ := newFakeIronic(t, map[string]fakeRoute{
		"GET /chassis/" + testChassisUUID: {http.StatusOK, testChassis()},
	})
	var buf bytes.Buffer
	if err := runChassisShow(context.Background(), client, &output.Options{Format: output.FormatJSON}, testChassisUUID, &buf); err != nil {
		t.Fatal(err)
	}
	got := decodeJSON[map[string]any](t, buf.String())
	if _, ok := got["nodes"]; ok {
		t.Errorf("show carries the nodes link: %v", got)
	}
	if got["description"] != "rack 12" {
		t.Errorf("show = %v", got)
	}
}

func TestRunChassisCreateSetUnsetDelete(t *testing.T) {
	client, fake := newFakeIronic(t, map[string]fakeRoute{
		"POST /chassis":                      {http.StatusCreated, testChassis()},
		"PATCH /chassis/" + testChassisUUID:  {http.StatusOK, testChassis()},
		"DELETE /chassis/" + testChassisUUID: {http.StatusNoContent, nil},
	})
	ctx, o := context.Background(), &output.Options{Format: output.FormatValue, Columns: []string{"uuid"}}
	var buf bytes.Buffer
	if err := runChassisCreate(ctx, client, o, &chassisFlags{uuid: testChassisUUID, description: "rack 12", extra: []string{"row=b"}}, &buf); err != nil {
		t.Fatal(err)
	}
	jsonEq(t, "create body", fake.call("POST /chassis").Body,
		`{"uuid":"`+testChassisUUID+`","description":"rack 12","extra":{"row":"b"}}`)

	if err := runChassisSet(ctx, client, o, testChassisUUID, &chassisFlags{description: "rack 13", extra: []string{"row=c"}}, &buf); err != nil {
		t.Fatal(err)
	}
	if err := runChassisUnset(ctx, client, o, testChassisUUID, true, []string{"row"}, &buf); err != nil {
		t.Fatal(err)
	}
	var patches []any
	for _, c := range fake.calls {
		if c.Method == http.MethodPatch {
			patches = append(patches, c.Body)
		}
	}
	jsonEq(t, "set/unset patches", patches, `[
		[{"op":"add","path":"/description","value":"rack 13"},{"op":"add","path":"/extra/row","value":"c"}],
		[{"op":"remove","path":"/description"},{"op":"remove","path":"/extra/row"}]]`)

	if err := runChassisSet(ctx, client, o, testChassisUUID, &chassisFlags{}, &buf); err == nil {
		t.Error("chassis set with no flags should fail before a request")
	}
	if err := runChassisDelete(ctx, client, []string{testChassisUUID}, &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "Deleted chassis "+testChassisUUID) {
		t.Errorf("output = %q", buf.String())
	}
}

func TestRunPortUnset_PatchAndGate(t *testing.T) {
	const port = "aaaaaaaa-0000-0000-0000-000000000001"
	client, fake := newFakeIronic(t, map[string]fakeRoute{
		"PATCH /ports/" + port: {http.StatusOK, map[string]any{"uuid": port}},
	})
	o := &output.Options{Format: output.FormatValue, Columns: []string{"uuid"}}
	f := &portUnsetFlags{extra: []string{"a/b"}, llc: []string{"switch_info"}, portGroup: true, physicalNetwork: true,
		isSmartNIC: true, name: true, description: true}
	if err := runPortUnset(context.Background(), client, o, port, f, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	jsonEq(t, "patch", fake.call("PATCH /ports/"+port).Body, `[
		{"op":"remove","path":"/extra/a~1b"},
		{"op":"remove","path":"/local_link_connection/switch_info"},
		{"op":"remove","path":"/portgroup_uuid"},
		{"op":"remove","path":"/physical_network"},
		{"op":"add","path":"/is_smartnic","value":false},
		{"op":"remove","path":"/name"},
		{"op":"remove","path":"/description"}]`)
	if err := runPortUnset(context.Background(), client, o, port, &portUnsetFlags{}, &bytes.Buffer{}); err == nil {
		t.Error("port unset with no flags should fail")
	}
}

// On a cloud below 1.101 ironic rejects the unknown path; the error names the
// release that added the field.
func TestRunPortUnset_PostZedFieldExplained(t *testing.T) {
	const port = "aaaaaaaa-0000-0000-0000-000000000002"
	client, fake := newFakeIronic(t, map[string]fakeRoute{
		"PATCH /ports/" + port: {http.StatusBadRequest, map[string]any{"error_message": "bad path"}},
	})
	fake.max = "1.80"
	err := runPortUnset(context.Background(), client, &output.Options{Format: output.FormatTable}, port,
		&portUnsetFlags{vendor: true, category: true}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "requires ironic API 1.101 (OpenStack 2025.2); this cloud supports up to 1.80") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunNodeBootModeSet(t *testing.T) {
	client, fake := newFakeIronic(t, map[string]fakeRoute{
		"PUT /nodes/n1/states/boot_mode": {http.StatusAccepted, nil},
		"PUT /nodes/n2/states/boot_mode": {http.StatusAccepted, nil},
	})
	var buf bytes.Buffer
	if err := runNodeBootModeSet(context.Background(), client, []string{"n1", "n2"}, "uefi", &buf); err != nil {
		t.Fatal(err)
	}
	jsonEq(t, "body", fake.call("PUT /nodes/n2/states/boot_mode").Body, `{"target":"uefi"}`)
	if buf.String() != "Requested boot mode uefi for node n1\nRequested boot mode uefi for node n2\n" {
		t.Errorf("output = %q", buf.String())
	}
}

func TestRunDriverPassthru(t *testing.T) {
	client, fake := newFakeIronic(t, map[string]fakeRoute{
		"GET /drivers/ipmi/vendor_passthru/methods": {http.StatusOK, map[string]any{
			"send_raw": map[string]any{"http_methods": []any{"POST"}, "async": true, "description": "raw IPMI", "attach": false},
		}},
		"POST /drivers/ipmi/vendor_passthru/send_raw": {http.StatusOK, map[string]any{"ok": true}},
	})
	var buf bytes.Buffer
	if err := runPassthruList(context.Background(), client, &output.Options{Format: output.FormatValue}, driverPassthru, "ipmi", &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(buf.String(), "send_raw POST true raw IPMI false") {
		t.Errorf("list = %q", buf.String())
	}
	buf.Reset()
	if err := runPassthruCall(context.Background(), client, driverPassthru, "ipmi", "send_raw", http.MethodPost, []string{"raw_bytes=0x00"}, &buf); err != nil {
		t.Fatal(err)
	}
	jsonEq(t, "call body", fake.call("POST /drivers/ipmi/vendor_passthru/send_raw").Body, `{"raw_bytes":"0x00"}`)
}

// baremetal create walks chassis → nodes → port groups → ports, filling each
// parent's UUID in, sets a node's traits, and carries on past a failure.
func TestRunCreateResources_NestingAndErrors(t *testing.T) {
	dir := t.TempDir()
	yamlDoc := `chassis:
  - description: rack 12
    nodes:
      - driver: fake-hardware
        name: n1
        traits: [CUSTOM_GOLD]
        portgroups:
          - name: bond0
            ports:
              - address: "52:54:00:00:00:01"
        ports:
          - address: "52:54:00:00:00:02"
nodes:
  - driver: fake-hardware
    name: n2
    chassis_uuid: other
`
	if err := os.WriteFile(filepath.Join(dir, "inv.yaml"), []byte(yamlDoc), 0o600); err != nil {
		t.Fatal(err)
	}
	jsonDoc := `{"nodes": [{"driver": "bad"}]}`
	if err := os.WriteFile(filepath.Join(dir, "more.json"), []byte(jsonDoc), 0o600); err != nil {
		t.Fatal(err)
	}
	docs, err := loadResourceFiles([]string{filepath.Join(dir, "inv.yaml"), filepath.Join(dir, "more.json")})
	if err != nil {
		t.Fatal(err)
	}

	client, fake := newFakeIronic(t, map[string]fakeRoute{
		"POST /chassis":            {http.StatusCreated, map[string]any{"uuid": "ch-1"}},
		"POST /nodes":              {http.StatusCreated, map[string]any{"uuid": "node-1"}},
		"PUT /nodes/node-1/traits": {http.StatusNoContent, nil},
		"POST /portgroups":         {http.StatusCreated, map[string]any{"uuid": "pg-1"}},
		"POST /ports":              {http.StatusCreated, map[string]any{"uuid": "port-1"}},
	})
	var buf bytes.Buffer
	err = runCreateResources(context.Background(), client, docs, &buf)
	var bodies []string
	for _, c := range fake.calls {
		bodies = append(bodies, c.Method+" "+c.Path+" "+compactJSON(c.Body))
	}
	want := []string{
		`POST /chassis {"description":"rack 12"}`,
		`POST /nodes {"chassis_uuid":"ch-1","driver":"fake-hardware","name":"n1"}`,
		`PUT /nodes/node-1/traits {"traits":["CUSTOM_GOLD"]}`,
		`POST /portgroups {"name":"bond0","node_uuid":"node-1"}`,
		`POST /ports {"address":"52:54:00:00:00:01","node_uuid":"node-1","portgroup_uuid":"pg-1"}`,
		`POST /ports {"address":"52:54:00:00:00:02","node_uuid":"node-1"}`,
		`POST /nodes {"chassis_uuid":"other","driver":"fake-hardware","name":"n2"}`,
		`POST /nodes {"driver":"bad"}`,
	}
	if strings.Join(bodies, "\n") != strings.Join(want, "\n") {
		t.Errorf("requests =\n%s\nwant\n%s", strings.Join(bodies, "\n"), strings.Join(want, "\n"))
	}
	if err != nil {
		t.Fatalf("every create succeeded on the fake, got %v", err)
	}
	if !strings.Contains(buf.String(), "Created port group pg-1\n") {
		t.Errorf("output = %q", buf.String())
	}
}

func TestLoadResourceFiles_ValidatesEveryFileFirst(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	_, err := loadResourceFiles([]string{
		write("a.txt", "{}"),
		write("b.json", `{"ports": []}`),
		write("c.yaml", "nodes: {driver: x}"),
		write("d.json", `{"nodes": [1]}`),
		filepath.Join(dir, "missing.json"),
	})
	if err == nil {
		t.Fatal("want a validation error")
	}
	for _, want := range []string{"must have a .json or .yaml extension", `unexpected key "ports"`, "must be an array",
		"item 1 must be an object", "cannot read file"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}
}

// A port that names another node is refused, and the error is reported after
// the rest is created.
func TestRunCreateResources_ConflictingParent(t *testing.T) {
	client, fake := newFakeIronic(t, map[string]fakeRoute{
		"POST /nodes": {http.StatusCreated, map[string]any{"uuid": "node-1"}},
	})
	docs := []resourceDoc{{nodes: []map[string]any{{
		"driver": "fake-hardware",
		"ports":  []any{map[string]any{"address": "52:54:00:00:00:03", "node_uuid": "node-9"}},
	}}}}
	err := runCreateResources(context.Background(), client, docs, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "cannot create a port as part of node node-1") {
		t.Fatalf("err = %v", err)
	}
	if len(fake.calls) != 1 {
		t.Errorf("calls = %v, want only the node", fake.calls)
	}
}

package baremetal

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"
	fakeclient "github.com/gophercloud/gophercloud/v2/testhelper/client"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

const nodeCreatedBody = `{
  "uuid": "11111111-1111-1111-1111-111111111111",
  "name": "new-node",
  "driver": "ipmi",
  "resource_class": "baremetal",
  "provision_state": "enroll"
}`

func TestRunNodeCreate_RequestBodyAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotIronicVersion string
	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/nodes", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotIronicVersion = r.Header.Get("X-OpenStack-Ironic-API-Version")
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(nodeCreatedBody))
	})

	client := baremetalClient(fakeServer, "1.80")
	o := &output.Options{Format: output.FormatValue}
	f := &nodeCreateFlags{
		name:          "new-node",
		driver:        "ipmi",
		resourceClass: "baremetal",
		property:      []string{"cpu=4"},
		driverInfo:    []string{"ipmi_address=10.0.0.1"},
		extra:         []string{"rack=r1"},
	}

	var buf bytes.Buffer
	if err := runNodeCreate(context.Background(), client, o, f, &buf); err != nil {
		t.Fatalf("runNodeCreate returned error: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("request method = %q, want POST", gotMethod)
	}
	if gotIronicVersion != "1.80" {
		t.Errorf("X-OpenStack-Ironic-API-Version = %q, want 1.80", gotIronicVersion)
	}
	if gotBody["name"] != "new-node" || gotBody["driver"] != "ipmi" || gotBody["resource_class"] != "baremetal" {
		t.Errorf("unexpected create body: %#v", gotBody)
	}
	props, ok := gotBody["properties"].(map[string]any)
	if !ok || props["cpu"] != "4" {
		t.Errorf("create body properties = %#v, want cpu=4", gotBody["properties"])
	}
	dinfo, ok := gotBody["driver_info"].(map[string]any)
	if !ok || dinfo["ipmi_address"] != "10.0.0.1" {
		t.Errorf("create body driver_info = %#v", gotBody["driver_info"])
	}
	extra, ok := gotBody["extra"].(map[string]any)
	if !ok || extra["rack"] != "r1" {
		t.Errorf("create body extra = %#v", gotBody["extra"])
	}

	out := buf.String()
	for _, want := range []string{"new-node", "ipmi", "baremetal"} {
		if !strings.Contains(out, want) {
			t.Errorf("node create output missing %q\n---\n%s", want, out)
		}
	}
}

func TestRunNodeDelete_MultipleIDs(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	deleted := map[string]string{}
	for _, id := range []string{"node-a", "node-b"} {
		id := id
		fakeServer.Mux.HandleFunc("/nodes/"+id, func(w http.ResponseWriter, r *http.Request) {
			deleted[id] = r.Method
			w.WriteHeader(http.StatusNoContent)
		})
	}

	client := baremetalClient(fakeServer, "1.80")

	var buf bytes.Buffer
	if err := runNodeDelete(context.Background(), client, []string{"node-a", "node-b"}, &buf); err != nil {
		t.Fatalf("runNodeDelete returned error: %v", err)
	}

	for _, id := range []string{"node-a", "node-b"} {
		if deleted[id] != http.MethodDelete {
			t.Errorf("node %s: method = %q, want DELETE", id, deleted[id])
		}
	}
	out := buf.String()
	if !strings.Contains(out, "Deleted node node-a") || !strings.Contains(out, "Deleted node node-b") {
		t.Errorf("unexpected delete output:\n%s", out)
	}
}

func TestRunNodeSet_PatchBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	var gotOps []map[string]any
	fakeServer.Mux.HandleFunc("/nodes/node-a", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotOps)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(nodeCreatedBody))
	})

	client := baremetalClient(fakeServer, "1.80")
	o := &output.Options{Format: output.FormatValue}
	f := &nodeSetFlags{
		name:          "renamed",
		resourceClass: "gpu",
		property:      []string{"ram=64"},
	}

	var buf bytes.Buffer
	if err := runNodeSet(context.Background(), client, o, "node-a", f, &buf); err != nil {
		t.Fatalf("runNodeSet returned error: %v", err)
	}

	if gotMethod != http.MethodPatch {
		t.Errorf("request method = %q, want PATCH", gotMethod)
	}
	assertPatchOp(t, gotOps, "replace", "/name", "renamed")
	assertPatchOp(t, gotOps, "replace", "/resource_class", "gpu")
	assertPatchOp(t, gotOps, "add", "/properties/ram", "64")
}

func TestRunNodeSet_NoFlagsErrors(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	client := baremetalClient(fakeServer, "1.80")
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	err := runNodeSet(context.Background(), client, o, "node-a", &nodeSetFlags{}, &buf)
	if err == nil {
		t.Fatal("expected error when no attribute flags are set, got nil")
	}
	if !strings.Contains(err.Error(), "at least one attribute flag") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRunNodeUnset_PatchBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	var gotOps []map[string]any
	fakeServer.Mux.HandleFunc("/nodes/node-a", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotOps)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(nodeCreatedBody))
	})

	client := baremetalClient(fakeServer, "1.80")
	o := &output.Options{Format: output.FormatValue}
	f := &nodeUnsetFlags{
		name:         true,
		instanceUUID: true,
		property:     []string{"ram"},
	}

	var buf bytes.Buffer
	if err := runNodeUnset(context.Background(), client, o, "node-a", f, &buf); err != nil {
		t.Fatalf("runNodeUnset returned error: %v", err)
	}

	if gotMethod != http.MethodPatch {
		t.Errorf("request method = %q, want PATCH", gotMethod)
	}
	assertPatchRemove(t, gotOps, "/name")
	assertPatchRemove(t, gotOps, "/instance_uuid")
	assertPatchRemove(t, gotOps, "/properties/ram")
}

// assertPatchOp asserts a JSON-patch op with matching op/path/value is present.
func assertPatchOp(t *testing.T, ops []map[string]any, op, path string, value any) {
	t.Helper()
	for _, o := range ops {
		if o["op"] == op && o["path"] == path && o["value"] == value {
			return
		}
	}
	t.Errorf("missing patch op {op:%q path:%q value:%v} in %#v", op, path, value, ops)
}

// assertPatchRemove asserts a remove op for the path is present.
func assertPatchRemove(t *testing.T, ops []map[string]any, path string) {
	t.Helper()
	for _, o := range ops {
		if o["op"] == "remove" && o["path"] == path {
			return
		}
	}
	t.Errorf("missing remove op for path %q in %#v", path, ops)
}

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

const portShowBody = `{
  "uuid": "aaaaaaaa-0000-0000-0000-000000000001",
  "address": "11:22:33:44:55:66",
  "node_uuid": "11111111-1111-1111-1111-111111111111",
  "pxe_enabled": true,
  "physical_network": "physnet1"
}`

func TestRunPortShow_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	fakeServer.Mux.HandleFunc("/ports/aaaaaaaa-0000-0000-0000-000000000001", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(portShowBody))
	})

	client := baremetalClient(fakeServer, "1.80")
	o := &output.Options{Format: output.FormatValue}

	var buf bytes.Buffer
	if err := runPortShow(context.Background(), client, o, "aaaaaaaa-0000-0000-0000-000000000001", &buf); err != nil {
		t.Fatalf("runPortShow returned error: %v", err)
	}

	if gotMethod != http.MethodGet {
		t.Errorf("request method = %q, want GET", gotMethod)
	}
	out := buf.String()
	for _, want := range []string{"11:22:33:44:55:66", "physnet1", "11111111-1111-1111-1111-111111111111"} {
		if !strings.Contains(out, want) {
			t.Errorf("port show output missing %q\n---\n%s", want, out)
		}
	}
}

func TestRunPortCreate_RequestBodyAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotIronicVersion string
	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/ports", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotIronicVersion = r.Header.Get("X-OpenStack-Ironic-API-Version")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(portShowBody))
	})

	client := baremetalClient(fakeServer, "1.80")
	o := &output.Options{Format: output.FormatValue}
	pxe := true
	f := &portCreateFlags{
		node:            "11111111-1111-1111-1111-111111111111",
		address:         "11:22:33:44:55:66",
		physicalNetwork: "physnet1",
		pxeEnabled:      pxe,
		pxeEnabledSet:   true,
		extra:           []string{"slot=1"},
	}

	var buf bytes.Buffer
	if err := runPortCreate(context.Background(), client, o, f, &buf); err != nil {
		t.Fatalf("runPortCreate returned error: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("request method = %q, want POST", gotMethod)
	}
	if gotIronicVersion != "1.80" {
		t.Errorf("X-OpenStack-Ironic-API-Version = %q, want 1.80", gotIronicVersion)
	}
	if gotBody["node_uuid"] != "11111111-1111-1111-1111-111111111111" {
		t.Errorf("create body node_uuid = %v", gotBody["node_uuid"])
	}
	if gotBody["address"] != "11:22:33:44:55:66" {
		t.Errorf("create body address = %v", gotBody["address"])
	}
	if gotBody["physical_network"] != "physnet1" {
		t.Errorf("create body physical_network = %v", gotBody["physical_network"])
	}
	if gotBody["pxe_enabled"] != true {
		t.Errorf("create body pxe_enabled = %v, want true", gotBody["pxe_enabled"])
	}
	extra, ok := gotBody["extra"].(map[string]any)
	if !ok || extra["slot"] != "1" {
		t.Errorf("create body extra = %#v", gotBody["extra"])
	}
	if !strings.Contains(buf.String(), "11:22:33:44:55:66") {
		t.Errorf("port create output missing address:\n%s", buf.String())
	}
}

func TestRunPortCreate_PxeEnabledOmittedWhenUnset(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/ports", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(portShowBody))
	})

	client := baremetalClient(fakeServer, "1.80")
	o := &output.Options{Format: output.FormatValue}
	f := &portCreateFlags{
		node:    "11111111-1111-1111-1111-111111111111",
		address: "11:22:33:44:55:66",
		// pxeEnabledSet is false: the field must not be sent.
	}

	var buf bytes.Buffer
	if err := runPortCreate(context.Background(), client, o, f, &buf); err != nil {
		t.Fatalf("runPortCreate returned error: %v", err)
	}
	if _, present := gotBody["pxe_enabled"]; present {
		t.Errorf("pxe_enabled must be omitted when the flag was not set: %#v", gotBody)
	}
}

func TestRunPortDelete_MultipleIDs(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	deleted := map[string]string{}
	for _, id := range []string{"port-a", "port-b"} {
		id := id
		fakeServer.Mux.HandleFunc("/ports/"+id, func(w http.ResponseWriter, r *http.Request) {
			deleted[id] = r.Method
			w.WriteHeader(http.StatusNoContent)
		})
	}

	client := baremetalClient(fakeServer, "1.80")

	var buf bytes.Buffer
	if err := runPortDelete(context.Background(), client, []string{"port-a", "port-b"}, &buf); err != nil {
		t.Fatalf("runPortDelete returned error: %v", err)
	}

	for _, id := range []string{"port-a", "port-b"} {
		if deleted[id] != http.MethodDelete {
			t.Errorf("port %s: method = %q, want DELETE", id, deleted[id])
		}
	}
	out := buf.String()
	if !strings.Contains(out, "Deleted port port-a") || !strings.Contains(out, "Deleted port port-b") {
		t.Errorf("unexpected delete output:\n%s", out)
	}
}

func TestRunPortSet_PatchBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	var gotOps []map[string]any
	fakeServer.Mux.HandleFunc("/ports/port-a", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotOps)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(portShowBody))
	})

	client := baremetalClient(fakeServer, "1.80")
	o := &output.Options{Format: output.FormatValue}
	f := &portSetFlags{
		address:         "aa:bb:cc:dd:ee:ff",
		physicalNetwork: "physnet2",
		pxeEnabled:      false,
		pxeEnabledSet:   true,
		extra:           []string{"slot=2"},
	}

	var buf bytes.Buffer
	if err := runPortSet(context.Background(), client, o, "port-a", f, &buf); err != nil {
		t.Fatalf("runPortSet returned error: %v", err)
	}

	if gotMethod != http.MethodPatch {
		t.Errorf("request method = %q, want PATCH", gotMethod)
	}
	assertPatchOp(t, gotOps, "replace", "/address", "aa:bb:cc:dd:ee:ff")
	assertPatchOp(t, gotOps, "replace", "/physical_network", "physnet2")
	assertPatchOp(t, gotOps, "replace", "/pxe_enabled", false)
	assertPatchOp(t, gotOps, "add", "/extra/slot", "2")
}

func TestRunPortSet_NoFlagsErrors(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	client := baremetalClient(fakeServer, "1.80")
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	err := runPortSet(context.Background(), client, o, "port-a", &portSetFlags{}, &buf)
	if err == nil {
		t.Fatal("expected error when no attribute flags are set, got nil")
	}
	if !strings.Contains(err.Error(), "at least one attribute flag") {
		t.Errorf("unexpected error: %v", err)
	}
}

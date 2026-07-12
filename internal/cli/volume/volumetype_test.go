package volume

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

const typeListBody = `{
  "volume_types": [
    {"id": "t1111111-1111-1111-1111-111111111111", "name": "ssd", "description": "fast", "is_public": true, "extra_specs": {"k": "v"}},
    {"id": "t2222222-2222-2222-2222-222222222222", "name": "hdd", "description": "cheap", "is_public": false, "extra_specs": {}}
  ]
}`

const typeGetBody = `{
  "volume_type": {
    "id": "t1111111-1111-1111-1111-111111111111",
    "name": "ssd",
    "description": "fast",
    "is_public": true,
    "extra_specs": {"volume_backend_name": "lvm"},
    "qos_specs_id": "q1"
  }
}`

func TestRunTypeList_RequestAndTableOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	fakeServer.Mux.HandleFunc("/types", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		assertVolumeMicroversion(t, r, "3.59")
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(typeListBody))
	})

	client := volumeClient(fakeServer, "3.59")
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runTypeList(context.Background(), client, o, &buf); err != nil {
		t.Fatalf("runTypeList returned error: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	out := buf.String()
	for _, want := range []string{
		"ID", "Name", "Is Public", "Description",
		"ssd", "hdd", "fast", "cheap",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("type list output missing %q\n---\n%s", want, out)
		}
	}
}

func TestRunTypeShow_ByID(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const id = "t1111111-1111-1111-1111-111111111111"
	fakeServer.Mux.HandleFunc("/types/"+id, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		assertVolumeMicroversion(t, r, "3.59")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(typeGetBody))
	})

	client := volumeClient(fakeServer, "3.59")
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runTypeShow(context.Background(), client, o, id, &buf); err != nil {
		t.Fatalf("runTypeShow returned error: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"id", "name", "ssd", "extra_specs", "volume_backend_name", "qos_specs_id", id} {
		if !strings.Contains(out, want) {
			t.Errorf("type show output missing %q\n---\n%s", want, out)
		}
	}
}

func TestRunTypeCreate_RequestBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/types", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		assertVolumeMicroversion(t, r, "3.59")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"volume_type":{"id":"t9","name":"gold","description":"d","is_public":false,"extra_specs":{"k":"v"}}}`))
	})

	client := volumeClient(fakeServer, "3.59")
	o := &output.Options{Format: output.FormatJSON}
	f := &typeCreateFlags{description: "d", private: true, property: []string{"k=v"}}
	var buf bytes.Buffer
	// visibilitySet=true so is_public is emitted.
	if err := runTypeCreate(context.Background(), client, o, "gold", f, true, &buf); err != nil {
		t.Fatalf("runTypeCreate returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	vt, ok := gotBody["volume_type"].(map[string]any)
	if !ok {
		t.Fatalf("body missing volume_type object: %#v", gotBody)
	}
	if vt["name"] != "gold" || vt["description"] != "d" {
		t.Errorf("unexpected create body: %#v", vt)
	}
	// --private ⇒ is_public=false via the os-volume-type-access:is_public key.
	if vt["os-volume-type-access:is_public"] != false {
		t.Errorf("body is_public = %v, want false", vt["os-volume-type-access:is_public"])
	}
	specs, ok := vt["extra_specs"].(map[string]any)
	if !ok || specs["k"] != "v" {
		t.Errorf("body extra_specs = %v, want {k:v}", vt["extra_specs"])
	}
	if !strings.Contains(buf.String(), "gold") {
		t.Errorf("output missing created type:\n%s", buf.String())
	}
}

func TestRunTypeDelete_ByName(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const id = "t1111111-1111-1111-1111-111111111111"
	// GET by name 404s → name-filtered list resolves the ID → DELETE by ID.
	fakeServer.Mux.HandleFunc("/types/ssd", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	var listed, gotDelete bool
	fakeServer.Mux.HandleFunc("/types", func(w http.ResponseWriter, _ *http.Request) {
		listed = true
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(typeListBody))
	})
	fakeServer.Mux.HandleFunc("/types/"+id, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %q, want DELETE", r.Method)
		}
		gotDelete = true
		w.WriteHeader(http.StatusAccepted)
	})

	client := volumeClient(fakeServer, "3.59")
	var buf bytes.Buffer
	if err := runTypeDelete(context.Background(), client, []string{"ssd"}, &buf); err != nil {
		t.Fatalf("runTypeDelete returned error: %v", err)
	}
	if !listed {
		t.Error("expected a name-filtered list on /types")
	}
	if !gotDelete {
		t.Error("expected a DELETE on /types/<id>")
	}
	if !strings.Contains(buf.String(), "Deleted volume type: ssd") {
		t.Errorf("delete output missing confirmation:\n%s", buf.String())
	}
}

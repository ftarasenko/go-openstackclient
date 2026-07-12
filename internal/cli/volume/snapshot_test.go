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

const snapshotListBody = `{
  "snapshots": [
    {"id": "s1111111-1111-1111-1111-111111111111", "name": "snap-a", "description": "d", "status": "available", "size": 10, "volume_id": "v1"},
    {"id": "s2222222-2222-2222-2222-222222222222", "name": "snap-b", "description": "", "status": "creating", "size": 20, "volume_id": "v2"}
  ]
}`

const snapshotGetBody = `{
  "snapshot": {
    "id": "s1111111-1111-1111-1111-111111111111",
    "name": "snap-a",
    "description": "d",
    "status": "available",
    "size": 10,
    "volume_id": "v1",
    "metadata": {"k": "v"}
  }
}`

func TestRunSnapshotList_RequestAndTableOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	fakeServer.Mux.HandleFunc("/snapshots", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		assertVolumeMicroversion(t, r, "3.59")
		th.TestFormValues(t, r, map[string]string{
			"all_tenants": "true",
			"name":        "snap-a",
			"status":      "available",
			"volume_id":   "v1",
		})
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(snapshotListBody))
	})

	client := volumeClient(fakeServer, "3.59")
	o := &output.Options{Format: output.FormatTable}
	f := &snapshotListFlags{allProjects: true, name: "snap-a", status: "available", volume: "v1"}

	var buf bytes.Buffer
	if err := runSnapshotList(context.Background(), client, o, f, &buf); err != nil {
		t.Fatalf("runSnapshotList returned error: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	out := buf.String()
	for _, want := range []string{
		"ID", "Name", "Description", "Status", "Size",
		"snap-a", "snap-b", "available", "creating",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("snapshot list output missing %q\n---\n%s", want, out)
		}
	}
}

func TestRunSnapshotShow_ByName(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const id = "s1111111-1111-1111-1111-111111111111"
	// GET by name 404s → name-filtered list resolves the ID → GET by ID.
	fakeServer.Mux.HandleFunc("/snapshots/snap-a", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	var listed bool
	fakeServer.Mux.HandleFunc("/snapshots", func(w http.ResponseWriter, r *http.Request) {
		listed = true
		th.TestFormValues(t, r, map[string]string{"name": "snap-a"})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(snapshotListBody))
	})
	fakeServer.Mux.HandleFunc("/snapshots/"+id, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(snapshotGetBody))
	})

	client := volumeClient(fakeServer, "3.59")
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runSnapshotShow(context.Background(), client, o, "snap-a", &buf); err != nil {
		t.Fatalf("runSnapshotShow returned error: %v", err)
	}
	if !listed {
		t.Error("expected a name-filtered list on /snapshots")
	}
	out := buf.String()
	for _, want := range []string{"id", "name", "snap-a", "volume_id", id} {
		if !strings.Contains(out, want) {
			t.Errorf("snapshot show output missing %q\n---\n%s", want, out)
		}
	}
}

func TestRunSnapshotCreate_RequestBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const volID = "v1111111-1111-1111-1111-111111111111"
	fakeServer.Mux.HandleFunc("/volumes/"+volID, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"volume":{"id":"` + volID + `","name":"vol-a"}}`))
	})

	var gotMethod string
	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/snapshots", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		assertVolumeMicroversion(t, r, "3.59")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"snapshot":{"id":"s9","name":"snap-new","status":"creating","volume_id":"` + volID + `"}}`))
	})

	client := volumeClient(fakeServer, "3.59")
	o := &output.Options{Format: output.FormatJSON}
	f := &snapshotCreateFlags{volume: volID, description: "d", force: true}
	var buf bytes.Buffer
	if err := runSnapshotCreate(context.Background(), client, o, "snap-new", f, &buf); err != nil {
		t.Fatalf("runSnapshotCreate returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	snap, ok := gotBody["snapshot"].(map[string]any)
	if !ok {
		t.Fatalf("body missing snapshot object: %#v", gotBody)
	}
	if snap["name"] != "snap-new" || snap["volume_id"] != volID || snap["description"] != "d" || snap["force"] != true {
		t.Errorf("unexpected create body: %#v", snap)
	}
	if !strings.Contains(buf.String(), "snap-new") {
		t.Errorf("output missing created snapshot:\n%s", buf.String())
	}
}

func TestRunSnapshotDelete_ByID(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const id = "s1111111-1111-1111-1111-111111111111"
	var gotDelete bool
	fakeServer.Mux.HandleFunc("/snapshots/"+id, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet: // resolver short-circuit
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(snapshotGetBody))
		case http.MethodDelete:
			gotDelete = true
			w.WriteHeader(http.StatusAccepted)
		default:
			t.Errorf("unexpected method %q", r.Method)
		}
	})

	client := volumeClient(fakeServer, "3.59")
	var buf bytes.Buffer
	if err := runSnapshotDelete(context.Background(), client, []string{id}, &buf); err != nil {
		t.Fatalf("runSnapshotDelete returned error: %v", err)
	}
	if !gotDelete {
		t.Error("expected a DELETE on /snapshots/<id>")
	}
	if !strings.Contains(buf.String(), "Deleted snapshot: "+id) {
		t.Errorf("delete output missing confirmation:\n%s", buf.String())
	}
}

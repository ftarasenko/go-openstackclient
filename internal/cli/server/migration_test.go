package server

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

const migrationListBody = `{
  "migrations": [
    {
      "id": 42,
      "uuid": "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
      "source_compute": "cmp-1",
      "dest_compute": "cmp-2",
      "instance_uuid": "11111111-1111-1111-1111-111111111111",
      "status": "finished",
      "migration_type": "live-migration",
      "old_instance_type_id": 1,
      "new_instance_type_id": 2,
      "created_at": "2016-03-04T06:27:59.000000"
    }
  ]
}`

// TestRunServerMigrationList covers the raw os-migrations GET, the KeyStack
// created-since/created-before filters (KCP-9165/7192) appended as query
// params, and the rendered table. Integer flavor ids must decode cleanly.
func TestRunServerMigrationList(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotQuery url.Values
	fakeServer.Mux.HandleFunc("/os-migrations", func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(migrationListBody))
	})

	client := computeClient(fakeServer, "2.80")
	o := &output.Options{Format: output.FormatTable}
	f := &migrationListFlags{
		host:          "cmp-1",
		migrationType: "live-migration",
		createdSince:  "2016-03-04T06:27:59Z",
		createdBefore: "2016-04-04T06:27:59Z",
	}
	var buf bytes.Buffer
	if err := runServerMigrationList(context.Background(), client, o, f, &buf); err != nil {
		t.Fatalf("runServerMigrationList: %v", err)
	}
	for k, v := range map[string]string{
		"host":           "cmp-1",
		"migration_type": "live-migration",
		"created-since":  "2016-03-04T06:27:59Z",
		"created-before": "2016-04-04T06:27:59Z",
	} {
		if got := gotQuery.Get(k); got != v {
			t.Errorf("query %q = %q, want %q", k, got, v)
		}
	}
	out := buf.String()
	for _, want := range []string{"42", "cmp-1", "cmp-2", "finished", "live-migration"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// TestRunServerMigrationList_DefaultQueryUnchanged guards graceful degradation:
// with no filters set, no KeyStack-only params leak into the request.
func TestRunServerMigrationList_DefaultQueryUnchanged(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotQuery url.Values
	fakeServer.Mux.HandleFunc("/os-migrations", func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"migrations":[]}`))
	})

	client := computeClient(fakeServer, "2.80")
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runServerMigrationList(context.Background(), client, o, &migrationListFlags{}, &buf); err != nil {
		t.Fatalf("runServerMigrationList: %v", err)
	}
	for _, k := range []string{"created-since", "created-before", "host", "status", "migration_type"} {
		if _, ok := gotQuery[k]; ok {
			t.Errorf("unexpected query param %q in default migration list: %v", k, gotQuery)
		}
	}
}

// TestRunServerEvacuate covers the evacuate action body, including the KeyStack
// preserve_ephemeral extension.
func TestRunServerEvacuate(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/servers/"+serverUUID+"/action", func(w http.ResponseWriter, r *http.Request) {
		gotBody = decodeBody(t, r)
		w.WriteHeader(http.StatusOK)
	})

	client := computeClient(fakeServer, "2.90")
	var buf bytes.Buffer
	if err := runServerEvacuate(context.Background(), client, serverUUID, "cmp-2", "s3cret", true, &buf); err != nil {
		t.Fatalf("runServerEvacuate: %v", err)
	}
	action, ok := gotBody["evacuate"].(map[string]any)
	if !ok {
		t.Fatalf("body missing evacuate object: %v", gotBody)
	}
	if action["host"] != "cmp-2" {
		t.Errorf("host = %v, want cmp-2", action["host"])
	}
	if action["adminPass"] != "s3cret" {
		t.Errorf("adminPass = %v, want s3cret", action["adminPass"])
	}
	if action["preserve_ephemeral"] != true {
		t.Errorf("preserve_ephemeral = %v, want true", action["preserve_ephemeral"])
	}
	if _, ok := action["onSharedStorage"]; ok {
		t.Errorf("onSharedStorage must not be sent on a modern microversion: %v", action)
	}
	if !strings.Contains(buf.String(), "Requested evacuation of server "+serverUUID) {
		t.Errorf("output = %q", buf.String())
	}
}

// TestRunServerEvacuate_MinimalBody confirms an empty evacuate body (scheduler
// picks the host, no password, no preserve-ephemeral) omits every field.
func TestRunServerEvacuate_MinimalBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/servers/"+serverUUID+"/action", func(w http.ResponseWriter, r *http.Request) {
		gotBody = decodeBody(t, r)
		w.WriteHeader(http.StatusOK)
	})

	client := computeClient(fakeServer, "2.90")
	var buf bytes.Buffer
	if err := runServerEvacuate(context.Background(), client, serverUUID, "", "", false, &buf); err != nil {
		t.Fatalf("runServerEvacuate: %v", err)
	}
	action, ok := gotBody["evacuate"].(map[string]any)
	if !ok {
		t.Fatalf("body missing evacuate object: %v", gotBody)
	}
	if len(action) != 0 {
		t.Errorf("evacuate body = %v, want empty", action)
	}
}

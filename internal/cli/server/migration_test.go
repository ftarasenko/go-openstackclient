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

// TestRunServerMigrate_Cold covers the default cold migration: a bare
// {"migrate": null} server action and the "Migrated" confirmation.
func TestRunServerMigrate_Cold(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/servers/"+serverUUID+"/action", func(w http.ResponseWriter, r *http.Request) {
		gotBody = decodeBody(t, r)
		w.WriteHeader(http.StatusAccepted)
	})

	client := computeClient(fakeServer, "2.90")
	var buf bytes.Buffer
	if err := runServerMigrate(context.Background(), client, serverUUID, &serverMigrateFlags{}, &buf); err != nil {
		t.Fatalf("runServerMigrate: %v", err)
	}
	if _, ok := gotBody["migrate"]; !ok {
		t.Fatalf("body missing migrate key: %v", gotBody)
	}
	if v := gotBody["migrate"]; v != nil {
		t.Errorf("cold migrate body = %v, want {\"migrate\": null}", gotBody)
	}
	if !strings.Contains(buf.String(), "Migrated server "+serverUUID) {
		t.Errorf("output = %q", buf.String())
	}
}

// TestRunServerMigrate_LiveDefault covers the default (shared-storage) live
// migration: nova >= 2.25 requires block_migration, which must default to
// "auto"; host is a required property so it is sent as null when no target is
// given, and disk_over_commit (2.24-) is not sent. This is the case that
// previously 400'd — first with no block_migration, then with host omitted.
func TestRunServerMigrate_LiveDefault(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/servers/"+serverUUID+"/action", func(w http.ResponseWriter, r *http.Request) {
		gotBody = decodeBody(t, r)
		w.WriteHeader(http.StatusAccepted)
	})

	client := computeClient(fakeServer, "2.90")
	var buf bytes.Buffer
	if err := runServerMigrate(context.Background(), client, serverUUID, &serverMigrateFlags{live: true}, &buf); err != nil {
		t.Fatalf("runServerMigrate live default: %v", err)
	}
	action, ok := gotBody["os-migrateLive"].(map[string]any)
	if !ok {
		t.Fatalf("body missing os-migrateLive object: %v", gotBody)
	}
	if action["block_migration"] != "auto" {
		t.Errorf("block_migration = %v, want \"auto\"", action["block_migration"])
	}
	if v, ok := action["host"]; !ok || v != nil {
		t.Errorf("host must be sent as null when no target given: %v", action)
	}
	if _, ok := action["disk_over_commit"]; ok {
		t.Errorf("disk_over_commit must not be sent at microversion >= 2.25: %v", action)
	}
	if !strings.Contains(buf.String(), "Requested live migration of server "+serverUUID) {
		t.Errorf("output = %q", buf.String())
	}
}

// TestRunServerMigrate_LiveBlockWithHost covers --block-migration + --host: an
// explicit block migration to a named target host.
func TestRunServerMigrate_LiveBlockWithHost(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/servers/"+serverUUID+"/action", func(w http.ResponseWriter, r *http.Request) {
		gotBody = decodeBody(t, r)
		w.WriteHeader(http.StatusAccepted)
	})

	client := computeClient(fakeServer, "2.90")
	var buf bytes.Buffer
	f := &serverMigrateFlags{live: true, host: "cmp-2", blockMigration: true}
	if err := runServerMigrate(context.Background(), client, serverUUID, f, &buf); err != nil {
		t.Fatalf("runServerMigrate live block: %v", err)
	}
	action := gotBody["os-migrateLive"].(map[string]any)
	if action["host"] != "cmp-2" {
		t.Errorf("host = %v, want cmp-2", action["host"])
	}
	if action["block_migration"] != true {
		t.Errorf("block_migration = %v, want true", action["block_migration"])
	}
}

// TestRunServerMigrate_LiveShared covers --shared-migration forcing
// block_migration=false regardless of the "auto" default.
func TestRunServerMigrate_LiveShared(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/servers/"+serverUUID+"/action", func(w http.ResponseWriter, r *http.Request) {
		gotBody = decodeBody(t, r)
		w.WriteHeader(http.StatusAccepted)
	})

	client := computeClient(fakeServer, "2.90")
	var buf bytes.Buffer
	if err := runServerMigrate(context.Background(), client, serverUUID, &serverMigrateFlags{live: true, sharedMigration: true}, &buf); err != nil {
		t.Fatalf("runServerMigrate live shared: %v", err)
	}
	action := gotBody["os-migrateLive"].(map[string]any)
	if action["block_migration"] != false {
		t.Errorf("block_migration = %v, want false", action["block_migration"])
	}
}

// TestRunServerMigrate_LiveLegacyMicroversion covers a pinned pre-2.25 compute
// API: block_migration defaults to false (not "auto") and disk_over_commit is
// required, so it is always sent.
func TestRunServerMigrate_LiveLegacyMicroversion(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/servers/"+serverUUID+"/action", func(w http.ResponseWriter, r *http.Request) {
		gotBody = decodeBody(t, r)
		w.WriteHeader(http.StatusAccepted)
	})

	client := computeClient(fakeServer, "2.20")
	var buf bytes.Buffer
	if err := runServerMigrate(context.Background(), client, serverUUID, &serverMigrateFlags{live: true}, &buf); err != nil {
		t.Fatalf("runServerMigrate live legacy: %v", err)
	}
	action := gotBody["os-migrateLive"].(map[string]any)
	if action["block_migration"] != false {
		t.Errorf("block_migration = %v, want false", action["block_migration"])
	}
	if action["disk_over_commit"] != false {
		t.Errorf("disk_over_commit = %v, want false (sent at <= 2.24)", action["disk_over_commit"])
	}
}

// TestRunServerMigrate_LiveWait covers --wait: after os-migrateLive the poll
// GETs the server and returns once it reaches ACTIVE with no task in flight.
func TestRunServerMigrate_LiveWait(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/servers/"+serverUUID+"/action", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	fakeServer.Mux.HandleFunc("/servers/"+serverUUID, func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, "GET")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"server":{"id":"` + serverUUID + `","status":"ACTIVE","OS-EXT-STS:task_state":null}}`))
	})

	client := computeClient(fakeServer, "2.90")
	var buf bytes.Buffer
	f := &serverMigrateFlags{live: true, wait: true}
	if err := runServerMigrate(context.Background(), client, serverUUID, f, &buf); err != nil {
		t.Fatalf("runServerMigrate live+wait: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Requested live migration of server "+serverUUID) {
		t.Errorf("output missing request line:\n%s", out)
	}
	if !strings.Contains(out, "migration complete (status ACTIVE)") {
		t.Errorf("output missing completion line:\n%s", out)
	}
}

const serverMigrationShowBody = `{
  "migration": {
    "id": 5,
    "uuid": "22222222-2222-2222-2222-222222222222",
    "status": "running",
    "server_uuid": "11111111-1111-1111-1111-111111111111",
    "source_compute": "cmp-1",
    "source_node": "node-1",
    "dest_compute": "cmp-2",
    "dest_host": "10.0.0.2",
    "dest_node": "node-2",
    "memory_total_bytes": 1048576,
    "memory_processed_bytes": 524288,
    "memory_remaining_bytes": 524288,
    "disk_total_bytes": 0,
    "disk_processed_bytes": 0,
    "disk_remaining_bytes": 0,
    "created_at": "2016-03-04T06:27:59.000000",
    "updated_at": "2016-03-04T06:28:59.000000"
  }
}`

// TestRunServerMigrationShow covers the raw os-server-migrations GET and the
// rendered single-migration fields, including the live-migration byte counters.
func TestRunServerMigrationShow(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/servers/"+serverUUID+"/migrations/5", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, "GET")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(serverMigrationShowBody))
	})

	client := computeClient(fakeServer, "2.80")
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runServerMigrationShow(context.Background(), client, o, serverUUID, "5", &buf); err != nil {
		t.Fatalf("runServerMigrationShow: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"running", "cmp-1", "cmp-2", "node-2", "1048576", "524288"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// TestRunServerMigrationAbort covers the DELETE against os-server-migrations.
func TestRunServerMigrationAbort(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/servers/"+serverUUID+"/migrations/5", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, "DELETE")
		w.WriteHeader(http.StatusAccepted)
	})

	client := computeClient(fakeServer, "2.80")
	var buf bytes.Buffer
	if err := runServerMigrationAbort(context.Background(), client, serverUUID, "5", &buf); err != nil {
		t.Fatalf("runServerMigrationAbort: %v", err)
	}
	if !strings.Contains(buf.String(), "Requested abort of migration 5 of server "+serverUUID) {
		t.Errorf("output = %q", buf.String())
	}
}

// TestRunServerMigrationForceComplete covers the force_complete action body.
func TestRunServerMigrationForceComplete(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/servers/"+serverUUID+"/migrations/5/action", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, "POST")
		gotBody = decodeBody(t, r)
		w.WriteHeader(http.StatusAccepted)
	})

	client := computeClient(fakeServer, "2.80")
	var buf bytes.Buffer
	if err := runServerMigrationForceComplete(context.Background(), client, serverUUID, "5", &buf); err != nil {
		t.Fatalf("runServerMigrationForceComplete: %v", err)
	}
	if _, ok := gotBody["force_complete"]; !ok {
		t.Errorf("body missing force_complete key: %v", gotBody)
	}
	if !strings.Contains(buf.String(), "Requested force-complete of migration 5 of server "+serverUUID) {
		t.Errorf("output = %q", buf.String())
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

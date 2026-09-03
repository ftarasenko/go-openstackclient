package server

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

const migrationListProgressBody = `{
  "migrations": [
    {
      "id": 7,
      "uuid": "aaaaaaaa-0000-0000-0000-000000000007",
      "instance_uuid": "11111111-1111-1111-1111-111111111111",
      "source_compute": "compute-1",
      "dest_compute": "compute-2",
      "status": "running",
      "migration_type": "live-migration",
      "created_at": "2026-09-01T10:00:00.000000"
    },
    {
      "id": 8,
      "uuid": "aaaaaaaa-0000-0000-0000-000000000008",
      "instance_uuid": "22222222-2222-2222-2222-222222222222",
      "source_compute": "compute-1",
      "dest_compute": "compute-3",
      "status": "completed",
      "migration_type": "live-migration",
      "created_at": "2026-09-01T09:00:00.000000"
    }
  ]
}`

// TestRunServerMigrationList_Progress covers --progress: os-migrations carries
// no byte counters, so the in-flight rows are decorated from
// /servers/{id}/migrations. The terminal row must not be queried at all (the
// endpoint lists in-progress live migrations only) and must render blank
// counters rather than zeros, which would read as "no data moved".
func TestRunServerMigrationList_Progress(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/os-migrations", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(migrationListProgressBody))
	})
	var progressCalls []string
	fakeServer.Mux.HandleFunc("/servers/11111111-1111-1111-1111-111111111111/migrations",
		func(w http.ResponseWriter, r *http.Request) {
			progressCalls = append(progressCalls, r.URL.Path)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"migrations":[{"id":7,"status":"running",
			  "memory_total_bytes":8589934592,"memory_processed_bytes":6442450944,
			  "memory_remaining_bytes":2147483648,"disk_total_bytes":0,
			  "disk_processed_bytes":0,"disk_remaining_bytes":0}]}`))
		})
	fakeServer.Mux.HandleFunc("/servers/22222222-2222-2222-2222-222222222222/migrations",
		func(w http.ResponseWriter, r *http.Request) {
			progressCalls = append(progressCalls, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		})

	o := &output.Options{Format: output.FormatCSV}
	var buf bytes.Buffer
	f := &migrationListFlags{progress: true}
	if err := runServerMigrationList(context.Background(), computeClient(fakeServer, "2.93"), o, f, &buf); err != nil {
		t.Fatalf("runServerMigrationList: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"Memory Total Bytes", "Memory Remaining Bytes", "Disk Remaining Bytes",
		"8589934592", "6442450944", "2147483648",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("progress output missing %q\n---\n%s", want, out)
		}
	}
	if len(progressCalls) != 1 || !strings.Contains(progressCalls[0], "11111111") {
		t.Errorf("progress calls = %v, want only the running migration's server", progressCalls)
	}
	// The completed migration's row ends in the six empty counter cells.
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if !strings.HasPrefix(line, "8,") {
			continue
		}
		if !strings.HasSuffix(strings.TrimRight(line, "\r"), ",,,,,,") {
			t.Errorf("completed row = %q, want blank counters", line)
		}
	}
}

// Without --progress the listing must issue exactly one request and carry no
// counter columns — the flag is opt-in because it costs a call per server.
func TestRunServerMigrationList_NoProgressByDefault(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var calls int
	fakeServer.Mux.HandleFunc("/os-migrations", func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(migrationListProgressBody))
	})

	o := &output.Options{Format: output.FormatCSV}
	var buf bytes.Buffer
	if err := runServerMigrationList(context.Background(), computeClient(fakeServer, "2.93"), o,
		&migrationListFlags{}, &buf); err != nil {
		t.Fatalf("runServerMigrationList: %v", err)
	}
	if calls != 1 {
		t.Errorf("os-migrations requests = %d, want 1", calls)
	}
	if strings.Contains(buf.String(), "Memory Total Bytes") {
		t.Errorf("default listing unexpectedly carries the counters\n---\n%s", buf.String())
	}
}

package volume

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"
)

const (
	writeSnapshotID = "11111111-1111-1111-1111-111111111111"
	writeBackupID   = "22222222-2222-2222-2222-222222222222"
)

// serveSnapshotWithMetadata answers the lookup, the GET and the metadata PUT for
// one snapshot, capturing the PUT body.
func serveSnapshotWithMetadata(t *testing.T, fakeServer th.FakeServer, metadata string) *map[string]any {
	t.Helper()
	var body map[string]any
	fakeServer.Mux.HandleFunc("/snapshots", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"snapshots": [{"id": "` + writeSnapshotID + `", "name": "snap-1"}]}`))
	})
	fakeServer.Mux.HandleFunc("/snapshots/"+writeSnapshotID, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"snapshot": {"id": "` + writeSnapshotID + `", "name": "snap-1", "metadata": ` + metadata + `}}`))
	})
	fakeServer.Mux.HandleFunc("/snapshots/"+writeSnapshotID+"/metadata", func(w http.ResponseWriter, r *http.Request) {
		th.AssertEquals(t, "PUT", r.Method)
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"metadata": {}}`))
	})
	return &body
}

func TestRunSnapshotSet_PropertyMergesWithExisting(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	body := serveSnapshotWithMetadata(t, fakeServer, `{"keep": "yes", "tier": "cold"}`)

	f := &snapshotSetFlags{property: []string{"tier=hot"}}
	if err := runSnapshotSet(context.Background(), volumeClient(fakeServer, "latest"), writeSnapshotID, f, false, false); err != nil {
		t.Fatalf("runSnapshotSet returned error: %v", err)
	}

	// Cinder's metadata endpoint replaces the whole map, so an unrelated key
	// must be carried through or setting one property would delete the rest.
	metadata := (*body)["metadata"].(map[string]any)
	th.AssertEquals(t, "yes", metadata["keep"])
	th.AssertEquals(t, "hot", metadata["tier"])
}

func TestRunSnapshotSet_NoPropertyClearsBeforeApplying(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	body := serveSnapshotWithMetadata(t, fakeServer, `{"keep": "yes", "tier": "cold"}`)

	f := &snapshotSetFlags{property: []string{"tier=hot"}, noProperty: true}
	if err := runSnapshotSet(context.Background(), volumeClient(fakeServer, "latest"), writeSnapshotID, f, false, false); err != nil {
		t.Fatalf("runSnapshotSet returned error: %v", err)
	}

	metadata := (*body)["metadata"].(map[string]any)
	th.AssertEquals(t, 1, len(metadata))
	th.AssertEquals(t, "hot", metadata["tier"])
}

func TestRunSnapshotUnset_RemovesOnlyTheNamedKeys(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	body := serveSnapshotWithMetadata(t, fakeServer, `{"keep": "yes", "drop": "me"}`)

	err := runSnapshotUnset(context.Background(), volumeClient(fakeServer, "latest"), writeSnapshotID, []string{"drop"})
	if err != nil {
		t.Fatalf("runSnapshotUnset returned error: %v", err)
	}

	metadata := (*body)["metadata"].(map[string]any)
	th.AssertEquals(t, 1, len(metadata))
	th.AssertEquals(t, "yes", metadata["keep"])
}

func TestRunSnapshotSet_NameOnlyDoesNotTouchMetadata(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/snapshots", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"snapshots": [{"id": "` + writeSnapshotID + `", "name": "snap-1"}]}`))
	})
	var body map[string]any
	fakeServer.Mux.HandleFunc("/snapshots/"+writeSnapshotID, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decoding request body: %v", err)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"snapshot": {"id": "` + writeSnapshotID + `", "name": "renamed"}}`))
	})
	// No /metadata handler: with only --name given, that endpoint must not be
	// touched at all.

	f := &snapshotSetFlags{name: "renamed"}
	if err := runSnapshotSet(context.Background(), volumeClient(fakeServer, "latest"), writeSnapshotID, f, true, false); err != nil {
		t.Fatalf("runSnapshotSet returned error: %v", err)
	}
	snapshot := body["snapshot"].(map[string]any)
	th.AssertEquals(t, "renamed", snapshot["name"])
	if _, present := snapshot["description"]; present {
		t.Errorf("description sent although the flag was not set: %#v", snapshot)
	}
}

func TestRunBackupSet_MergesMetadataIntoTheSameUpdate(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/backups", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"backups": [{"id": "` + writeBackupID + `", "name": "backup-1"}]}`))
	})
	var body map[string]any
	fakeServer.Mux.HandleFunc("/backups/"+writeBackupID, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decoding request body: %v", err)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"backup": {"id": "` + writeBackupID + `", "name": "backup-1", "metadata": {"keep": "yes"}}}`))
	})

	f := &backupSetFlags{property: []string{"tier=hot"}}
	if err := runBackupSet(context.Background(), volumeClient(fakeServer, "latest"), writeBackupID, f, false, false); err != nil {
		t.Fatalf("runBackupSet returned error: %v", err)
	}

	// Unlike snapshots, cinder folds backup metadata into the same request as
	// name and description — but the map still replaces wholesale.
	metadata := body["backup"].(map[string]any)["metadata"].(map[string]any)
	th.AssertEquals(t, "yes", metadata["keep"])
	th.AssertEquals(t, "hot", metadata["tier"])
}

func TestRunBackupSet_HandlesABackupWithNoMetadata(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/backups", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"backups": [{"id": "` + writeBackupID + `", "name": "backup-1"}]}`))
	})
	var body map[string]any
	fakeServer.Mux.HandleFunc("/backups/"+writeBackupID, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decoding request body: %v", err)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		// gophercloud models metadata as *map[string]string, so an absent key
		// arrives as a nil pointer rather than an empty map.
		_, _ = w.Write([]byte(`{"backup": {"id": "` + writeBackupID + `", "name": "backup-1"}}`))
	})

	f := &backupSetFlags{property: []string{"tier=hot"}}
	if err := runBackupSet(context.Background(), volumeClient(fakeServer, "latest"), writeBackupID, f, false, false); err != nil {
		t.Fatalf("runBackupSet returned error: %v", err)
	}
	metadata := body["backup"].(map[string]any)["metadata"].(map[string]any)
	th.AssertEquals(t, "hot", metadata["tier"])
}

func TestParseProperties(t *testing.T) {
	m, err := parseProperties([]string{"a=1", "b=x=y"})
	if err != nil {
		t.Fatalf("parseProperties returned error: %v", err)
	}
	th.AssertEquals(t, "1", m["a"])
	th.AssertEquals(t, "x=y", m["b"])

	if _, err := parseProperties([]string{"noequals"}); err == nil {
		t.Error("expected an error for a value with no '='")
	}
}

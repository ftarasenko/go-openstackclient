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

const backupListBody = `{
  "backups": [
    {"id": "b1111111-1111-1111-1111-111111111111", "name": "bk-a", "description": "daily", "status": "available", "size": 10, "volume_id": "v1"},
    {"id": "b2222222-2222-2222-2222-222222222222", "name": "bk-b", "description": "", "status": "creating", "size": 20, "volume_id": "v2"}
  ]
}`

const backupGetBody = `{
  "backup": {
    "id": "b1111111-1111-1111-1111-111111111111",
    "name": "bk-a",
    "description": "daily",
    "status": "available",
    "size": 10,
    "volume_id": "v1",
    "is_incremental": false,
    "has_dependent_backups": false,
    "container": "backups"
  }
}`

func TestRunBackupList_RequestAndTableOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	// The detail view: GET /backups is cinder's summary, which carries no
	// description, status or size.
	fakeServer.Mux.HandleFunc("/backups/detail", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		assertVolumeMicroversion(t, r, "3.59")
		th.TestFormValues(t, r, map[string]string{
			"all_tenants": "true",
			"name":        "bk-a",
			"status":      "available",
			"volume_id":   "v1",
		})
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(backupListBody))
	})

	client := volumeClient(fakeServer, "3.59")
	o := &output.Options{Format: output.FormatTable}
	f := &backupListFlags{allProjects: true, name: "bk-a", status: "available", volume: "v1"}

	var buf bytes.Buffer
	if err := runBackupList(context.Background(), client, o, f, &buf); err != nil {
		t.Fatalf("runBackupList returned error: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	out := buf.String()
	for _, want := range []string{
		"ID", "Name", "Description", "Status", "Size",
		"bk-a", "bk-b", "available", "creating", "daily",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("backup list output missing %q\n---\n%s", want, out)
		}
	}
}

func TestRunBackupShow_ByID(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const id = "b1111111-1111-1111-1111-111111111111"
	fakeServer.Mux.HandleFunc("/backups/"+id, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		assertVolumeMicroversion(t, r, "3.59")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(backupGetBody))
	})

	client := volumeClient(fakeServer, "3.59")
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runBackupShow(context.Background(), client, o, id, &buf); err != nil {
		t.Fatalf("runBackupShow returned error: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"id", "name", "bk-a", "daily", "container", "backups", id} {
		if !strings.Contains(out, want) {
			t.Errorf("backup show output missing %q\n---\n%s", want, out)
		}
	}
}

func TestRunBackupCreate_RequestBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const volID = "v1111111-1111-1111-1111-111111111111"
	// resolveVolumeID short-circuits on a successful GET of the volume.
	fakeServer.Mux.HandleFunc("/volumes/"+volID, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"volume":{"id":"` + volID + `","name":"vol-a"}}`))
	})

	var gotMethod string
	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/backups", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		assertVolumeMicroversion(t, r, "3.59")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"backup":{"id":"b9","name":"bk-new","status":"creating","volume_id":"` + volID + `"}}`))
	})

	client := volumeClient(fakeServer, "3.59")
	o := &output.Options{Format: output.FormatJSON}
	f := &backupCreateFlags{name: "bk-new", description: "d", incremental: true}
	var buf bytes.Buffer
	if err := runBackupCreate(context.Background(), client, o, volID, f, &buf); err != nil {
		t.Fatalf("runBackupCreate returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	bk, ok := gotBody["backup"].(map[string]any)
	if !ok {
		t.Fatalf("body missing backup object: %#v", gotBody)
	}
	if bk["name"] != "bk-new" || bk["volume_id"] != volID || bk["description"] != "d" || bk["incremental"] != true {
		t.Errorf("unexpected create body: %#v", bk)
	}
	if !strings.Contains(buf.String(), "bk-new") {
		t.Errorf("output missing created backup:\n%s", buf.String())
	}
}

func TestRunBackupDelete_ByID(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const id = "b1111111-1111-1111-1111-111111111111"
	var gotDelete bool
	fakeServer.Mux.HandleFunc("/backups/"+id, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet: // resolver short-circuit
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(backupGetBody))
		case http.MethodDelete:
			gotDelete = true
			w.WriteHeader(http.StatusAccepted)
		default:
			t.Errorf("unexpected method %q", r.Method)
		}
	})

	client := volumeClient(fakeServer, "3.59")
	var buf bytes.Buffer
	if err := runBackupDelete(context.Background(), client, []string{id}, &buf); err != nil {
		t.Fatalf("runBackupDelete returned error: %v", err)
	}
	if !gotDelete {
		t.Error("expected a DELETE on /backups/<id>")
	}
	if !strings.Contains(buf.String(), "Deleted backup: "+id) {
		t.Errorf("delete output missing confirmation:\n%s", buf.String())
	}
}

func TestRunBackupRestore_ToExistingVolume(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const backupID = "b1111111-1111-1111-1111-111111111111"
	const volID = "v1111111-1111-1111-1111-111111111111"

	fakeServer.Mux.HandleFunc("/backups/"+backupID, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(backupGetBody))
	})
	fakeServer.Mux.HandleFunc("/volumes/"+volID, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"volume":{"id":"` + volID + `","name":"vol-a"}}`))
	})

	var gotMethod string
	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/backups/"+backupID+"/restore", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		assertVolumeMicroversion(t, r, "3.59")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"restore":{"backup_id":"` + backupID + `","volume_id":"` + volID + `","volume_name":"vol-a"}}`))
	})

	client := volumeClient(fakeServer, "3.59")
	o := &output.Options{Format: output.FormatTable}
	f := &backupRestoreFlags{volume: volID}
	var buf bytes.Buffer
	if err := runBackupRestore(context.Background(), client, o, backupID, f, &buf); err != nil {
		t.Fatalf("runBackupRestore returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	restore, ok := gotBody["restore"].(map[string]any)
	if !ok || restore["volume_id"] != volID {
		t.Errorf("restore body = %#v, want restore.volume_id=%s", gotBody, volID)
	}
	out := buf.String()
	for _, want := range []string{"backup_id", "volume_id", "volume_name", backupID, volID, "vol-a"} {
		if !strings.Contains(out, want) {
			t.Errorf("restore output missing %q\n---\n%s", want, out)
		}
	}
}

// The listing carries upstream's Incremental and Created At by default, and
// --long adds Availability Zone, Volume and Container. Volume is the volume's
// name in the table and its ID in json.
func TestRunBackupList_UpstreamColumns(t *testing.T) {
	const body = `{"backups": [
	  {"id": "b1", "name": "bk-a", "status": "available", "size": 10, "volume_id": "v1",
	   "is_incremental": true, "created_at": "2026-01-02T03:04:05.000000",
	   "availability_zone": "nova", "container": "volumebackups"},
	  {"id": "b2", "name": "bk-b", "status": "available", "size": 10, "volume_id": "v-gone",
	   "is_incremental": false, "created_at": "2026-01-03T03:04:05.000000"}
	]}`
	for _, tc := range []struct {
		name   string
		format string
		long   bool
		want   []string
		absent []string
	}{
		{
			name: "default", format: output.FormatCSV,
			want: []string{
				"ID,Name,Description,Status,Size,Incremental,Created At\n",
				"b1,bk-a,,available,10,true,2026-01-02T03:04:05+00:00\n",
			},
			absent: []string{"Container"},
		},
		{
			name: "long table names the volume", format: output.FormatTable, long: true,
			want: []string{"Availability Zone", "Volume", "Container", "nova", "data-vol", "v-gone", "volumebackups"},
		},
		{
			name: "long json keeps the volume id", format: output.FormatJSON, long: true,
			want: []string{`"Volume": "v1"`, `"Availability Zone": "nova"`, `"Availability Zone": null`, `"Incremental": true`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()
			fakeServer.Mux.HandleFunc("/backups/detail", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
			})
			fakeServer.Mux.HandleFunc("/volumes/detail", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"volumes": [{"id": "v1", "name": "data-vol"}]}`))
			})
			o := &output.Options{Format: tc.format}
			var buf bytes.Buffer
			if err := runBackupList(context.Background(), volumeClient(fakeServer, "3.59"), o,
				&backupListFlags{long: tc.long}, &buf); err != nil {
				t.Fatalf("runBackupList: %v", err)
			}
			for _, want := range tc.want {
				if !strings.Contains(buf.String(), want) {
					t.Errorf("output missing %q\n---\n%s", want, buf.String())
				}
			}
			for _, absent := range tc.absent {
				if strings.Contains(buf.String(), absent) {
					t.Errorf("output unexpectedly carries %q\n---\n%s", absent, buf.String())
				}
			}
		})
	}
}

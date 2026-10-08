package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

const volumeAttachmentsBody = `{"volumeAttachments": [{
  "id": "` + vol9UUID + `",
  "device": "/dev/vdb",
  "serverId": "` + serverUUID + `",
  "volumeId": "` + vol9UUID + `",
  "tag": "data",
  "delete_on_termination": true,
  "attachment_id": "aaaaaaaa-0000-4000-8000-000000000001",
  "bdm_uuid": "bbbbbbbb-0000-4000-8000-000000000002"
}]}`

func TestRunServerVolumeList_ColumnsFollowMicroversion(t *testing.T) {
	tests := []struct {
		mv         string
		wantCols   []string
		wantDelete any
	}{
		{
			mv:       "2.1",
			wantCols: []string{"ID", "Device", "Server ID", "Volume ID"},
		},
		{
			mv:         "2.79",
			wantCols:   []string{"ID", "Device", "Server ID", "Volume ID", "Tag", "Delete On Termination?"},
			wantDelete: true,
		},
		{
			mv: "latest",
			wantCols: []string{"Device", "Server ID", "Volume ID", "Tag", "Delete On Termination?",
				"Attachment ID", "BlockDeviceMapping UUID"},
			wantDelete: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.mv, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()
			fakeServer.Mux.HandleFunc("/servers/"+serverUUID+"/os-volume_attachments", func(w http.ResponseWriter, r *http.Request) {
				th.TestMethod(t, r, http.MethodGet)
				assertNovaMicroversion(t, r, tc.mv)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(volumeAttachmentsBody))
			})
			o := &output.Options{Format: "json"}
			var buf bytes.Buffer
			if err := runServerVolumeList(context.Background(), computeClient(fakeServer, tc.mv), o, serverUUID, &buf); err != nil {
				t.Fatalf("runServerVolumeList: %v", err)
			}
			var rows []map[string]any
			if err := json.Unmarshal(buf.Bytes(), &rows); err != nil {
				t.Fatalf("decoding %q: %v", buf.String(), err)
			}
			if len(rows) != 1 {
				t.Fatalf("rows = %d, want 1", len(rows))
			}
			if len(rows[0]) != len(tc.wantCols) {
				t.Errorf("columns = %v, want %v", rows[0], tc.wantCols)
			}
			for _, c := range tc.wantCols {
				if _, ok := rows[0][c]; !ok {
					t.Errorf("column %q missing from %v", c, rows[0])
				}
			}
			if tc.wantDelete != nil && rows[0]["Delete On Termination?"] != tc.wantDelete {
				t.Errorf("Delete On Termination? = %v, want %v", rows[0]["Delete On Termination?"], tc.wantDelete)
			}
			if tc.mv == "latest" && rows[0]["BlockDeviceMapping UUID"] != "bbbbbbbb-0000-4000-8000-000000000002" {
				t.Errorf("BlockDeviceMapping UUID = %v", rows[0]["BlockDeviceMapping UUID"])
			}
		})
	}
}

func TestRunServerVolumeSet_RequestBody(t *testing.T) {
	for _, del := range []bool{true, false} {
		fakeServer := th.SetupHTTP()
		calls := 0
		fakeServer.Mux.HandleFunc("/servers/"+serverUUID+"/os-volume_attachments/"+vol9UUID, func(w http.ResponseWriter, r *http.Request) {
			calls++
			th.TestMethod(t, r, http.MethodPut)
			assertNovaMicroversion(t, r, "2.85")
			want := `{"volumeAttachment": {"volumeId": "` + vol9UUID + `", "delete_on_termination": false}}`
			if del {
				want = strings.Replace(want, "false", "true", 1)
			}
			th.TestJSONRequest(t, r, want)
			w.WriteHeader(http.StatusAccepted)
		})
		handleVolumeByName(t, fakeServer, "vol-9", vol9UUID)
		if err := runServerVolumeSet(context.Background(), computeClient(fakeServer, "2.85"), volumeClient(fakeServer),
			serverUUID, "vol-9", &del); err != nil {
			t.Fatalf("runServerVolumeSet(%v): %v", del, err)
		}
		if calls != 1 {
			t.Errorf("PUT calls = %d, want 1", calls)
		}
		fakeServer.Teardown()
	}
}

// With neither flag, set is a no-op, like upstream: nothing reaches nova.
func TestRunServerVolumeSet_NoFlagsNoRequest(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/", func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s %s", r.Method, r.URL)
	})
	if err := runServerVolumeSet(context.Background(), computeClient(fakeServer, "latest"), volumeClient(fakeServer),
		serverUUID, vol9UUID, nil); err != nil {
		t.Fatalf("runServerVolumeSet: %v", err)
	}
}

func TestRunServerVolumeSet_RefusesBelow285(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/", func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s %s", r.Method, r.URL)
	})
	del := true
	err := runServerVolumeSet(context.Background(), computeClient(fakeServer, "2.84"), volumeClient(fakeServer),
		serverUUID, vol9UUID, &del)
	if err == nil || !strings.Contains(err.Error(), "2.85") {
		t.Fatalf("err = %v, want the 2.85 requirement", err)
	}
}

// An explicit --disable-delete-on-termination has to reach nova as false;
// gophercloud's CreateOpts would drop it as omitempty.
func TestRunServerAddVolume_DeleteOnTerminationAndTag(t *testing.T) {
	tests := []struct {
		name string
		del  *bool
		want string
	}{
		{"enable", new(true), `"delete_on_termination": true`},
		{"disable", new(false), `"delete_on_termination": false`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()
			fakeServer.Mux.HandleFunc("/servers/"+serverUUID+"/os-volume_attachments", func(w http.ResponseWriter, r *http.Request) {
				th.TestMethod(t, r, http.MethodPost)
				th.TestJSONRequest(t, r, `{"volumeAttachment": {"volumeId": "`+vol9UUID+`", "tag": "data", `+tc.want+`}}`)
				w.WriteHeader(http.StatusAccepted)
			})
			var buf bytes.Buffer
			if err := runServerAddVolume(context.Background(), computeClient(fakeServer, "2.79"), volumeClient(fakeServer),
				serverUUID, vol9UUID, volumeAttachFlags{tag: "data", deleteOnTermination: tc.del}, &buf); err != nil {
				t.Fatalf("runServerAddVolume: %v", err)
			}
		})
	}
}

func TestVolumeAttachBody_RefusesBelowMicroversion(t *testing.T) {
	if _, err := volumeAttachBody(computeClientForVersion("2.48"), vol9UUID, "", "data", nil); err == nil ||
		!strings.Contains(err.Error(), "2.49") {
		t.Errorf("--tag at 2.48: err = %v, want the 2.49 requirement", err)
	}
	if _, err := volumeAttachBody(computeClientForVersion("2.78"), vol9UUID, "", "", new(false)); err == nil ||
		!strings.Contains(err.Error(), "2.79") {
		t.Errorf("--disable-delete-on-termination at 2.78: err = %v, want the 2.79 requirement", err)
	}
}

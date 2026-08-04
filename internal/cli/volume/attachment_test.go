package volume

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	th "github.com/gophercloud/gophercloud/v2/testhelper"
	fakeclient "github.com/gophercloud/gophercloud/v2/testhelper/client"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

const attachmentID = "aaaaaaaa-1111-1111-1111-111111111111"

// offlineVolumeClient is enough for the validation-only tests: they must fail
// before any request is issued, so the client needs a service type and
// microversion but no endpoint.
func offlineVolumeClient(microversion string) *gophercloud.ServiceClient {
	return &gophercloud.ServiceClient{Type: "block-storage", Microversion: microversion}
}

const attachmentListBody = `{
  "attachments": [
    {
      "id": "aaaaaaaa-1111-1111-1111-111111111111",
      "volume_id": "11111111-1111-1111-1111-111111111111",
      "instance": "55555555-5555-5555-5555-555555555555",
      "status": "attached",
      "attach_mode": "rw",
      "attached_at": "2026-08-01T10:00:00.000000"
    },
    {
      "id": "bbbbbbbb-2222-2222-2222-222222222222",
      "volume_id": "22222222-2222-2222-2222-222222222222",
      "instance": "66666666-6666-6666-6666-666666666666",
      "status": "reserved"
    }
  ]
}`

const attachmentGetBody = `{
  "attachment": {
    "id": "aaaaaaaa-1111-1111-1111-111111111111",
    "volume_id": "11111111-1111-1111-1111-111111111111",
    "instance": "55555555-5555-5555-5555-555555555555",
    "status": "attached",
    "attach_mode": "rw",
    "attached_at": "2026-08-01T10:00:00.000000",
    "connection_info": {"driver_volume_type": "iscsi", "target_portal": "10.0.0.1:3260"}
  }
}`

func TestRunAttachmentList_RequestFiltersAndTableOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	fakeServer.Mux.HandleFunc("/attachments/detail", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		assertVolumeMicroversion(t, r, "3.59")
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		th.TestFormValues(t, r, map[string]string{
			"all_tenants": "true",
			"project_id":  "p-1",
			"status":      "attached",
		})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(attachmentListBody))
	})

	client := volumeClient(fakeServer, "3.59")
	o := &output.Options{Format: output.FormatTable}
	f := &attachmentListFlags{allProjects: true, project: "p-1", status: "attached"}

	var buf bytes.Buffer
	if err := runAttachmentList(context.Background(), client, o, f, &buf); err != nil {
		t.Fatalf("runAttachmentList returned error: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("request method = %q, want GET", gotMethod)
	}
	out := buf.String()
	for _, want := range []string{
		"ID", "Volume ID", "Server ID", "Status",
		attachmentID, "55555555-5555-5555-5555-555555555555", "attached", "reserved",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("attachment list output missing %q\n---\n%s", want, out)
		}
	}
}

// TestRunAttachmentList_VolumeFilterResolvesName covers --volume-id accepting a
// name: it is resolved through cinder before becoming the volume_id query filter.
func TestRunAttachmentList_VolumeFilterResolvesName(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const volID = "11111111-1111-1111-1111-111111111111"
	// A GET keyed by the name 404s, forcing the name-filtered list path.
	fakeServer.Mux.HandleFunc("/volumes/vol-a", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	fakeServer.Mux.HandleFunc("/volumes/detail", func(w http.ResponseWriter, r *http.Request) {
		th.TestFormValues(t, r, map[string]string{"name": "vol-a"})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(volumeListBody))
	})
	var gotVolumeID string
	fakeServer.Mux.HandleFunc("/attachments/detail", func(w http.ResponseWriter, r *http.Request) {
		gotVolumeID = r.URL.Query().Get("volume_id")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(attachmentListBody))
	})

	client := volumeClient(fakeServer, "latest")
	o := &output.Options{Format: output.FormatValue}
	f := &attachmentListFlags{volume: "vol-a"}
	if err := runAttachmentList(context.Background(), client, o, f, io.Discard); err != nil {
		t.Fatalf("runAttachmentList returned error: %v", err)
	}
	if gotVolumeID != volID {
		t.Errorf("volume_id filter = %q, want the resolved id %q", gotVolumeID, volID)
	}
}

func TestRunAttachmentList_LimitTruncates(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/attachments/detail", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(attachmentListBody))
	})

	client := volumeClient(fakeServer, "latest")
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	if err := runAttachmentList(context.Background(), client, o, &attachmentListFlags{limit: 1}, &buf); err != nil {
		t.Fatalf("runAttachmentList returned error: %v", err)
	}
	if lines := strings.Count(strings.TrimSpace(buf.String()), "\n") + 1; lines != 1 {
		t.Errorf("--limit 1 produced %d rows, want 1:\n%s", lines, buf.String())
	}
}

// TestRunAttachment_MicroversionGate keeps every attachment verb from issuing a
// request cinder would 404: the resource needs 3.27 and os-complete needs 3.44.
func TestRunAttachment_MicroversionGate(t *testing.T) {
	// Validation runs before any network use, so a nil-endpoint client is fine.
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s %s below the required microversion", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	})
	o := &output.Options{Format: output.FormatTable}

	old := volumeClient(fakeServer, "3.26")
	for name, run := range map[string]func() error{
		"list": func() error {
			return runAttachmentList(context.Background(), old, o, &attachmentListFlags{}, io.Discard)
		},
		"show": func() error { return runAttachmentShow(context.Background(), old, o, attachmentID, io.Discard) },
		"delete": func() error {
			return runAttachmentDelete(context.Background(), old, []string{attachmentID}, io.Discard)
		},
		"set": func() error {
			return runAttachmentSet(context.Background(), old, o, attachmentID,
				&attachmentConnectorFlags{initiator: "iqn.2026-08.local:1"}, io.Discard)
		},
		"create": func() error {
			return runAttachmentCreate(context.Background(), old, o, "vol", "srv", &attachmentCreateFlags{}, io.Discard)
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := run()
			if err == nil {
				t.Fatal("expected a microversion error at 3.26, got nil")
			}
			if !strings.Contains(err.Error(), "3.27") {
				t.Errorf("error should name microversion 3.27, got: %v", err)
			}
		})
	}

	// os-complete lands later than the resource itself.
	t.Run("complete", func(t *testing.T) {
		err := runAttachmentComplete(context.Background(), volumeClient(fakeServer, "3.43"), attachmentID, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "3.44") {
			t.Fatalf("expected a 3.44 microversion error, got: %v", err)
		}
	})

	// --mode rides the top-level key only from 3.54.
	t.Run("mode", func(t *testing.T) {
		f := &attachmentCreateFlags{mode: "ro"}
		err := runAttachmentCreate(context.Background(), volumeClient(fakeServer, "3.53"), o, "vol", "srv", f, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "3.54") {
			t.Fatalf("expected a 3.54 microversion error, got: %v", err)
		}
	})
}

func TestRunAttachmentShow_FieldsAndConnectionInfo(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/attachments/"+attachmentID, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		assertVolumeMicroversion(t, r, "3.59")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(attachmentGetBody))
	})

	client := volumeClient(fakeServer, "3.59")
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runAttachmentShow(context.Background(), client, o, attachmentID, &buf); err != nil {
		t.Fatalf("runAttachmentShow returned error: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"id", "volume_id", "instance", "attach_mode", "connection_info",
		attachmentID, "rw", "iscsi", "2026-08-01T10:00:00Z",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("attachment show output missing %q\n---\n%s", want, out)
		}
	}
	// A reserved/live attachment has no detached_at; the cell must stay empty
	// rather than printing Go's zero time.
	if strings.Contains(out, "0001-01-01") {
		t.Errorf("zero detached_at should render empty:\n%s", out)
	}
}

// TestRunAttachmentCreate_ReserveOmitsConnector covers the reserve step: without
// --connect no connector is sent, which is what makes cinder reserve rather than
// connect the volume.
func TestRunAttachmentCreate_ReserveOmitsConnector(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const volID = "11111111-1111-1111-1111-111111111111"
	const serverID = "55555555-5555-5555-5555-555555555555"
	fakeServer.Mux.HandleFunc("/volumes/"+volID, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(volumeGetBody))
	})
	var got map[string]any
	var gotMethod string
	fakeServer.Mux.HandleFunc("/attachments", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		assertVolumeMicroversion(t, r, "3.59")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(attachmentGetBody))
	})

	client := volumeClient(fakeServer, "3.59")
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	err := runAttachmentCreate(context.Background(), client, o, volID, serverID, &attachmentCreateFlags{}, &buf)
	if err != nil {
		t.Fatalf("runAttachmentCreate returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("request method = %q, want POST", gotMethod)
	}
	at, ok := got["attachment"].(map[string]any)
	if !ok {
		t.Fatalf("request body = %#v, want an attachment object", got)
	}
	if at["volume_uuid"] != volID || at["instance_uuid"] != serverID {
		t.Errorf("create body = %#v, want volume_uuid=%s instance_uuid=%s", at, volID, serverID)
	}
	if _, present := at["connector"]; present {
		t.Errorf("a reserve must not send a connector: %#v", at)
	}
	if _, present := at["mode"]; present {
		t.Errorf("mode must be omitted when --mode is unset: %#v", at)
	}
	if !strings.Contains(buf.String(), attachmentID) {
		t.Errorf("create output missing the new attachment id:\n%s", buf.String())
	}
}

// TestRunAttachmentCreate_ConnectSendsConnectorAndMode covers the one-shot path:
// --connect turns the connector flags into cinder's connector map, and --mode
// rides the top-level key at 3.54+.
func TestRunAttachmentCreate_ConnectSendsConnectorAndMode(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const volID = "11111111-1111-1111-1111-111111111111"
	fakeServer.Mux.HandleFunc("/volumes/"+volID, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(volumeGetBody))
	})
	var got map[string]any
	fakeServer.Mux.HandleFunc("/attachments", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(attachmentGetBody))
	})

	client := volumeClient(fakeServer, "latest")
	o := &output.Options{Format: output.FormatValue}
	f := &attachmentCreateFlags{
		connect: true,
		mode:    "ro",
		connector: attachmentConnectorFlags{
			initiator: "iqn.2026-08.local:node1",
			ip:        "10.0.0.7",
			host:      "node1",
			platform:  "x86_64",
			osType:    "linux2",
			multipath: true,
		},
	}
	if err := runAttachmentCreate(context.Background(), client, o, volID, "srv-uuid", f, io.Discard); err != nil {
		t.Fatalf("runAttachmentCreate returned error: %v", err)
	}
	at, ok := got["attachment"].(map[string]any)
	if !ok {
		t.Fatalf("request body = %#v, want an attachment object", got)
	}
	if at["mode"] != "ro" {
		t.Errorf("create body = %#v, want mode=ro", at)
	}
	conn, ok := at["connector"].(map[string]any)
	if !ok {
		t.Fatalf("create body missing connector: %#v", at)
	}
	for k, want := range map[string]any{
		"initiator": "iqn.2026-08.local:node1",
		"ip":        "10.0.0.7",
		"host":      "node1",
		"platform":  "x86_64",
		"os_type":   "linux2",
		"multipath": true,
	} {
		if conn[k] != want {
			t.Errorf("connector[%q] = %#v, want %#v", k, conn[k], want)
		}
	}
	// Unset string fields are dropped rather than sent as null.
	if _, present := conn["mountpoint"]; present {
		t.Errorf("unset connector fields must be omitted: %#v", conn)
	}
}

func TestRunAttachmentCreate_RejectsConnectorWithoutConnect(t *testing.T) {
	client := offlineVolumeClient("latest")
	o := &output.Options{Format: output.FormatTable}
	f := &attachmentCreateFlags{connector: attachmentConnectorFlags{initiator: "iqn.2026-08.local:node1"}}
	err := runAttachmentCreate(context.Background(), client, o, "vol", "srv", f, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "--connect") {
		t.Fatalf("expected an error naming --connect, got: %v", err)
	}
}

func TestRunAttachmentCreate_RejectsBadMode(t *testing.T) {
	client := offlineVolumeClient("latest")
	o := &output.Options{Format: output.FormatTable}
	f := &attachmentCreateFlags{mode: "rx"}
	err := runAttachmentCreate(context.Background(), client, o, "vol", "srv", f, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "--mode") {
		t.Fatalf("expected an error naming --mode, got: %v", err)
	}
}

func TestRunAttachmentSet_PutsConnector(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var got map[string]any
	var gotMethod string
	fakeServer.Mux.HandleFunc("/attachments/"+attachmentID, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		assertVolumeMicroversion(t, r, "3.59")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(attachmentGetBody))
	})

	client := volumeClient(fakeServer, "3.59")
	o := &output.Options{Format: output.FormatValue}
	f := &attachmentConnectorFlags{initiator: "iqn.2026-08.local:node1", mountpoint: "/dev/vdb"}
	var buf bytes.Buffer
	if err := runAttachmentSet(context.Background(), client, o, attachmentID, f, &buf); err != nil {
		t.Fatalf("runAttachmentSet returned error: %v", err)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("request method = %q, want PUT", gotMethod)
	}
	at, ok := got["attachment"].(map[string]any)
	if !ok {
		t.Fatalf("request body = %#v, want an attachment object", got)
	}
	conn, ok := at["connector"].(map[string]any)
	if !ok {
		t.Fatalf("update body missing connector: %#v", at)
	}
	if conn["initiator"] != "iqn.2026-08.local:node1" || conn["mountpoint"] != "/dev/vdb" {
		t.Errorf("connector = %#v, want the supplied initiator and mountpoint", conn)
	}
	if !strings.Contains(buf.String(), attachmentID) {
		t.Errorf("set output missing the attachment id:\n%s", buf.String())
	}
}

func TestRunAttachmentSet_NothingToSet(t *testing.T) {
	client := offlineVolumeClient("latest")
	o := &output.Options{Format: output.FormatTable}
	// --multipath alone is not a connector: it defaults to false and cannot be
	// told apart from unset.
	f := &attachmentConnectorFlags{multipath: true}
	if err := runAttachmentSet(context.Background(), client, o, attachmentID, f, io.Discard); err == nil {
		t.Fatal("expected an error when no connector flag is provided, got nil")
	}
}

func TestRunAttachmentDelete_AcceptsSeveralIDs(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const second = "bbbbbbbb-2222-2222-2222-222222222222"
	deleted := map[string]bool{}
	for _, id := range []string{attachmentID, second} {
		id := id
		fakeServer.Mux.HandleFunc("/attachments/"+id, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodDelete {
				t.Errorf("method = %q, want DELETE", r.Method)
			}
			assertVolumeMicroversion(t, r, "3.59")
			deleted[id] = true
			w.WriteHeader(http.StatusOK)
		})
	}

	client := volumeClient(fakeServer, "3.59")
	var buf bytes.Buffer
	if err := runAttachmentDelete(context.Background(), client, []string{attachmentID, second}, &buf); err != nil {
		t.Fatalf("runAttachmentDelete returned error: %v", err)
	}
	if !deleted[attachmentID] || !deleted[second] {
		t.Errorf("expected a DELETE for both attachments, got %v", deleted)
	}
	for _, id := range []string{attachmentID, second} {
		if !strings.Contains(buf.String(), "Deleted volume attachment: "+id) {
			t.Errorf("delete output missing confirmation for %s:\n%s", id, buf.String())
		}
	}
}

func TestRunAttachmentComplete_PostsOsComplete(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var got map[string]any
	var gotMethod string
	fakeServer.Mux.HandleFunc("/attachments/"+attachmentID+"/action", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		assertVolumeMicroversion(t, r, "3.59")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		w.WriteHeader(http.StatusNoContent)
	})

	client := volumeClient(fakeServer, "3.59")
	var buf bytes.Buffer
	if err := runAttachmentComplete(context.Background(), client, attachmentID, &buf); err != nil {
		t.Fatalf("runAttachmentComplete returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("request method = %q, want POST", gotMethod)
	}
	if _, present := got["os-complete"]; !present {
		t.Errorf("action body = %#v, want an os-complete key", got)
	}
	if !strings.Contains(buf.String(), "Completed volume attachment: "+attachmentID) {
		t.Errorf("complete output missing confirmation:\n%s", buf.String())
	}
}

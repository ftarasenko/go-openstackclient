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
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// assertVolumeMicroversion checks cinder emits the volume microversion via both
// the generic and volume-specific headers, mirroring the list test above.
func assertVolumeMicroversion(t *testing.T, r *http.Request, mv string) {
	t.Helper()
	if got := r.Header.Get("X-OpenStack-Volume-API-Version"); got != mv {
		t.Errorf("X-OpenStack-Volume-API-Version = %q, want %q", got, mv)
	}
	if got := r.Header.Get("OpenStack-API-Version"); got != "volume "+mv {
		t.Errorf("OpenStack-API-Version = %q, want %q", got, "volume "+mv)
	}
}

// volumeClient returns a service client wired to the mock server with the
// cinder service type + microversion, mirroring auth.Client.Volume().
func volumeClient(fakeServer th.FakeServer, microversion string) *gophercloud.ServiceClient {
	sc := fakeclient.ServiceClient(fakeServer)
	sc.Type = "block-storage"
	sc.Microversion = microversion
	return sc
}

const volumeListBody = `{
  "volumes": [
    {
      "id": "11111111-1111-1111-1111-111111111111",
      "name": "vol-a",
      "status": "available",
      "size": 10,
      "volume_type": "ssd",
      "bootable": "false",
      "availability_zone": "nova",
      "attachments": []
    },
    {
      "id": "22222222-2222-2222-2222-222222222222",
      "name": "vol-b",
      "status": "in-use",
      "size": 20,
      "volume_type": "hdd",
      "bootable": "true",
      "availability_zone": "nova",
      "attachments": [
        {"server_id": "srv-1", "device": "/dev/vdb"}
      ]
    }
  ]
}`

func TestRunVolumeList_RequestAndTableOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotVolumeVersion, gotAPIVersion string
	fakeServer.Mux.HandleFunc("/volumes/detail", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotVolumeVersion = r.Header.Get("X-OpenStack-Volume-API-Version")
		gotAPIVersion = r.Header.Get("OpenStack-API-Version")
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(volumeListBody))
	})

	client := volumeClient(fakeServer, "3.59")
	o := &output.Options{Format: output.FormatTable}

	var buf bytes.Buffer
	if err := runVolumeList(context.Background(), client, o, &volumeListFlags{}, &buf); err != nil {
		t.Fatalf("runVolumeList returned error: %v", err)
	}

	if gotMethod != http.MethodGet {
		t.Errorf("request method = %q, want GET", gotMethod)
	}
	// gophercloud emits the volume microversion via both headers for cinder.
	if gotVolumeVersion != "3.59" {
		t.Errorf("X-OpenStack-Volume-API-Version = %q, want 3.59", gotVolumeVersion)
	}
	if gotAPIVersion != "volume 3.59" {
		t.Errorf("OpenStack-API-Version = %q, want %q", gotAPIVersion, "volume 3.59")
	}

	out := buf.String()
	for _, want := range []string{
		"ID", "Name", "Status", "Size", "Attached to",
		"vol-a", "vol-b", "available", "in-use",
		"11111111-1111-1111-1111-111111111111",
		"srv-1 on /dev/vdb",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("table output missing %q\n---\n%s", want, out)
		}
	}
	// --long columns should not appear by default.
	if strings.Contains(out, "Bootable") {
		t.Errorf("default output should not contain --long columns:\n%s", out)
	}
}

func TestRunVolumeList_FiltersAndValueFormat(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/volumes/detail", func(w http.ResponseWriter, r *http.Request) {
		th.TestFormValues(t, r, map[string]string{
			"all_tenants": "true",
			"name":        "vol-a",
			"status":      "available",
		})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(volumeListBody))
	})

	client := volumeClient(fakeServer, "latest")
	o := &output.Options{Format: output.FormatValue}
	f := &volumeListFlags{allProjects: true, name: "vol-a", status: "available"}

	var buf bytes.Buffer
	if err := runVolumeList(context.Background(), client, o, f, &buf); err != nil {
		t.Fatalf("runVolumeList returned error: %v", err)
	}
	if strings.Contains(buf.String(), "ID") {
		t.Errorf("value format must not include headers:\n%s", buf.String())
	}
}

func TestRunVolumeCreate_RequestBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/volumes", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Fatalf("decoding request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"volume": {"id": "33333333-3333-3333-3333-333333333333", "name": "newvol", "size": 1, "status": "creating"}}`))
	})

	client := volumeClient(fakeServer, "latest")
	o := &output.Options{Format: output.FormatJSON}
	f := &volumeCreateFlags{size: 1, description: "d", volumeType: "ssd", property: []string{"k=v"}}

	var buf bytes.Buffer
	if err := runVolumeCreate(context.Background(), client, o, "newvol", f, &buf); err != nil {
		t.Fatalf("runVolumeCreate returned error: %v", err)
	}

	vol, ok := gotBody["volume"].(map[string]any)
	if !ok {
		t.Fatalf("request body missing top-level \"volume\" object: %#v", gotBody)
	}
	if vol["name"] != "newvol" {
		t.Errorf("body name = %v, want newvol", vol["name"])
	}
	if vol["size"] != float64(1) {
		t.Errorf("body size = %v, want 1", vol["size"])
	}
	if vol["volume_type"] != "ssd" {
		t.Errorf("body volume_type = %v, want ssd", vol["volume_type"])
	}
	if vol["description"] != "d" {
		t.Errorf("body description = %v, want d", vol["description"])
	}
	meta, ok := vol["metadata"].(map[string]any)
	if !ok || meta["k"] != "v" {
		t.Errorf("body metadata = %v, want {k:v}", vol["metadata"])
	}
	// Output rendered the created volume.
	if !strings.Contains(buf.String(), "33333333-3333-3333-3333-333333333333") {
		t.Errorf("output missing created volume id:\n%s", buf.String())
	}
}

func TestRunVolumeCreate_RejectsNonPositiveSize(t *testing.T) {
	// Size is validated before any network use, so a nil client is fine here.
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runVolumeCreate(context.Background(), nil, o, "x", &volumeCreateFlags{size: 0}, &buf)
	if err == nil {
		t.Fatal("expected error for --size 0, got nil")
	}
}

func TestRunVolumeUnset_ClearsLastKey(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const id = "44444444-4444-4444-4444-444444444444"
	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/volumes/"+id, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"volume": {"id": "` + id + `", "name": "vol", "metadata": {"k": "v"}}}`))
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(body, &gotBody); err != nil {
				t.Fatalf("decoding request body: %v", err)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"volume": {"id": "` + id + `", "name": "vol", "metadata": {}}}`))
		default:
			t.Errorf("unexpected method %q", r.Method)
		}
	})

	client := volumeClient(fakeServer, "latest")
	f := &volumeUnsetFlags{property: []string{"k"}}
	if err := runVolumeUnset(context.Background(), client, id, f); err != nil {
		t.Fatalf("runVolumeUnset returned error: %v", err)
	}

	vol, ok := gotBody["volume"].(map[string]any)
	if !ok {
		t.Fatalf("PUT body missing top-level \"volume\" object: %#v", gotBody)
	}
	meta, ok := vol["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("PUT body must include \"metadata\" key (empty object), got: %#v", vol)
	}
	if len(meta) != 0 {
		t.Errorf("metadata = %#v, want empty object", meta)
	}
}

const serviceListBody = `{
  "services": [
    {
      "binary": "cinder-volume",
      "host": "host1@lvm",
      "zone": "nova",
      "status": "enabled",
      "state": "up",
      "updated_at": "2026-07-11T10:00:00.000000"
    },
    {
      "binary": "cinder-scheduler",
      "host": "host1",
      "zone": "nova",
      "status": "enabled",
      "state": "up",
      "updated_at": "2026-07-11T10:00:00.000000"
    }
  ]
}`

func TestRunServiceList_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	fakeServer.Mux.HandleFunc("/os-services", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		th.TestFormValues(t, r, map[string]string{
			"host":   "host1@lvm",
			"binary": "cinder-volume",
		})
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(serviceListBody))
	})

	client := volumeClient(fakeServer, "latest")
	o := &output.Options{Format: output.FormatTable}
	f := &serviceListFlags{host: "host1@lvm", service: "cinder-volume"}

	var buf bytes.Buffer
	if err := runServiceList(context.Background(), client, o, f, &buf); err != nil {
		t.Fatalf("runServiceList returned error: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("request method = %q, want GET", gotMethod)
	}
	out := buf.String()
	for _, want := range []string{
		"Binary", "Host", "Zone", "Status", "State",
		"cinder-volume", "cinder-scheduler", "host1@lvm", "up",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("service list output missing %q\n---\n%s", want, out)
		}
	}
}

const volumeGetBody = `{
  "volume": {
    "id": "11111111-1111-1111-1111-111111111111",
    "name": "vol-a",
    "description": "primary",
    "status": "available",
    "size": 10,
    "volume_type": "ssd",
    "bootable": "false",
    "availability_zone": "nova",
    "attachments": [],
    "metadata": {"k": "v"}
  }
}`

func TestRunVolumeShow_ByID(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const id = "11111111-1111-1111-1111-111111111111"
	var gets int
	fakeServer.Mux.HandleFunc("/volumes/"+id, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		assertVolumeMicroversion(t, r, "3.59")
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		gets++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(volumeGetBody))
	})

	client := volumeClient(fakeServer, "3.59")
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runVolumeShow(context.Background(), client, o, id, &buf); err != nil {
		t.Fatalf("runVolumeShow returned error: %v", err)
	}
	// Resolver GET short-circuits, then the show GET: the /volumes/<id> path is
	// the only endpoint hit, so no name-filtered list is issued.
	if gets < 2 {
		t.Errorf("expected >=2 GETs on /volumes/%s (resolve + show), got %d", id, gets)
	}
	out := buf.String()
	for _, want := range []string{"id", "name", "vol-a", "available", "primary", id} {
		if !strings.Contains(out, want) {
			t.Errorf("show output missing %q\n---\n%s", want, out)
		}
	}
}

func TestRunVolumeShow_ByName(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const id = "11111111-1111-1111-1111-111111111111"
	// A GET keyed by the *name* 404s, forcing the name-filtered list path.
	fakeServer.Mux.HandleFunc("/volumes/vol-a", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	var listed bool
	fakeServer.Mux.HandleFunc("/volumes/detail", func(w http.ResponseWriter, r *http.Request) {
		listed = true
		th.TestFormValues(t, r, map[string]string{"name": "vol-a"})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(volumeListBody))
	})
	fakeServer.Mux.HandleFunc("/volumes/"+id, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(volumeGetBody))
	})

	client := volumeClient(fakeServer, "latest")
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	if err := runVolumeShow(context.Background(), client, o, "vol-a", &buf); err != nil {
		t.Fatalf("runVolumeShow returned error: %v", err)
	}
	if !listed {
		t.Error("expected a name-filtered list on /volumes/detail")
	}
	if !strings.Contains(buf.String(), id) {
		t.Errorf("show output missing resolved id:\n%s", buf.String())
	}
}

func TestRunVolumeDelete_ByID(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const id = "11111111-1111-1111-1111-111111111111"
	var gotDelete bool
	fakeServer.Mux.HandleFunc("/volumes/"+id, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet: // resolver short-circuit
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(volumeGetBody))
		case http.MethodDelete:
			gotDelete = true
			assertVolumeMicroversion(t, r, "3.59")
			w.WriteHeader(http.StatusAccepted)
		default:
			t.Errorf("unexpected method %q", r.Method)
		}
	})

	client := volumeClient(fakeServer, "3.59")
	var buf bytes.Buffer
	if err := runVolumeDelete(context.Background(), client, []string{id}, &buf); err != nil {
		t.Fatalf("runVolumeDelete returned error: %v", err)
	}
	if !gotDelete {
		t.Error("expected a DELETE on /volumes/<id>")
	}
	if !strings.Contains(buf.String(), "Deleted volume: "+id) {
		t.Errorf("delete output missing confirmation:\n%s", buf.String())
	}
}

// volumeSetCmd builds a cobra command carrying the "volume set" flags, marking
// the requested flags as Changed via Set so runVolumeSet's Changed() checks fire.
func volumeSetCmd(t *testing.T, f *volumeSetFlags, set map[string]string) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "set"}
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "")
	fl.StringVar(&f.description, "description", "", "")
	fl.StringArrayVar(&f.property, "property", nil, "")
	fl.IntVar(&f.size, "size", 0, "")
	for k, v := range set {
		if err := fl.Set(k, v); err != nil {
			t.Fatalf("setting flag %q: %v", k, err)
		}
	}
	return cmd
}

func TestRunVolumeSet_RenameAndExtend(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const id = "11111111-1111-1111-1111-111111111111"
	var gotExtend, gotUpdate map[string]any
	var gotActionMethod, gotUpdateMethod string
	fakeServer.Mux.HandleFunc("/volumes/"+id+"/action", func(w http.ResponseWriter, r *http.Request) {
		gotActionMethod = r.Method
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotExtend)
		w.WriteHeader(http.StatusAccepted)
	})
	fakeServer.Mux.HandleFunc("/volumes/"+id, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet: // resolver short-circuit
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(volumeGetBody))
		case http.MethodPut:
			gotUpdateMethod = r.Method
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &gotUpdate)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(volumeGetBody))
		default:
			t.Errorf("unexpected method %q", r.Method)
		}
	})

	client := volumeClient(fakeServer, "3.59")
	f := &volumeSetFlags{}
	cmd := volumeSetCmd(t, f, map[string]string{"name": "renamed", "size": "20"})
	if err := runVolumeSet(context.Background(), client, id, f, cmd); err != nil {
		t.Fatalf("runVolumeSet returned error: %v", err)
	}

	if gotActionMethod != http.MethodPost {
		t.Errorf("extend method = %q, want POST", gotActionMethod)
	}
	ext, ok := gotExtend["os-extend"].(map[string]any)
	if !ok || ext["new_size"] != float64(20) {
		t.Errorf("extend body = %#v, want os-extend.new_size=20", gotExtend)
	}
	if gotUpdateMethod != http.MethodPut {
		t.Errorf("update method = %q, want PUT", gotUpdateMethod)
	}
	vol, ok := gotUpdate["volume"].(map[string]any)
	if !ok || vol["name"] != "renamed" {
		t.Errorf("update body = %#v, want volume.name=renamed", gotUpdate)
	}
}

func TestRunVolumeSet_PropertyMergesMetadata(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const id = "11111111-1111-1111-1111-111111111111"
	var gotUpdate map[string]any
	fakeServer.Mux.HandleFunc("/volumes/"+id, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			// Both the resolver and the metadata-merge read hit this; return the
			// existing metadata {k:v}.
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(volumeGetBody))
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &gotUpdate)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(volumeGetBody))
		default:
			t.Errorf("unexpected method %q", r.Method)
		}
	})

	client := volumeClient(fakeServer, "latest")
	f := &volumeSetFlags{}
	cmd := volumeSetCmd(t, f, map[string]string{"property": "new=1"})
	if err := runVolumeSet(context.Background(), client, id, f, cmd); err != nil {
		t.Fatalf("runVolumeSet returned error: %v", err)
	}
	vol, ok := gotUpdate["volume"].(map[string]any)
	if !ok {
		t.Fatalf("PUT body missing volume: %#v", gotUpdate)
	}
	meta, ok := vol["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("PUT body missing metadata: %#v", vol)
	}
	// Existing key preserved, new key merged in.
	if meta["k"] != "v" || meta["new"] != "1" {
		t.Errorf("merged metadata = %#v, want {k:v, new:1}", meta)
	}
}

func TestRunVolumeSet_NothingToSet(t *testing.T) {
	// Validation happens before any network use, so a nil client is fine.
	f := &volumeSetFlags{}
	cmd := volumeSetCmd(t, f, nil)
	err := runVolumeSet(context.Background(), nil, "x", f, cmd)
	if err == nil {
		t.Fatal("expected error when no set flags are provided, got nil")
	}
}

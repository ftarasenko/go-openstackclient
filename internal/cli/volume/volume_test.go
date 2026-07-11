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

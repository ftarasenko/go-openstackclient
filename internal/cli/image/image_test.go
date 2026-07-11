package image

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

// imageClient returns a service client wired to the mock server with the glance
// service type, mirroring how auth.Client.Image does (no microversion header).
func imageClient(fakeServer th.FakeServer) *gophercloud.ServiceClient {
	sc := fakeclient.ServiceClient(fakeServer)
	sc.Type = "image"
	return sc
}

const imageListBody = `{
  "images": [
    {
      "id": "11111111-1111-1111-1111-111111111111",
      "name": "cirros",
      "status": "active",
      "visibility": "public",
      "protected": false,
      "disk_format": "qcow2",
      "container_format": "bare",
      "size": 13287936,
      "owner": "proj-a"
    },
    {
      "id": "22222222-2222-2222-2222-222222222222",
      "name": "ubuntu",
      "status": "active",
      "visibility": "private",
      "protected": true,
      "disk_format": "raw",
      "container_format": "bare",
      "size": 2361393152,
      "owner": "proj-b"
    }
  ]
}`

func TestRunImageList_RequestAndTableOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotPath string
	fakeServer.Mux.HandleFunc("/images", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(imageListBody))
	})

	client := imageClient(fakeServer)
	o := &output.Options{Format: output.FormatTable}

	var buf bytes.Buffer
	if err := runImageList(context.Background(), client, o, &imageListFlags{}, &buf); err != nil {
		t.Fatalf("runImageList returned error: %v", err)
	}

	if gotMethod != http.MethodGet {
		t.Errorf("request method = %q, want GET", gotMethod)
	}
	if gotPath != "/images" {
		t.Errorf("request path = %q, want /images", gotPath)
	}

	out := buf.String()
	for _, want := range []string{
		"ID", "Name", "Status",
		"cirros", "ubuntu",
		"11111111-1111-1111-1111-111111111111",
		"active",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("table output missing %q\n---\n%s", want, out)
		}
	}
	// --long-only columns should not appear by default.
	if strings.Contains(out, "Container Format") {
		t.Errorf("default output should not contain --long columns:\n%s", out)
	}
}

func TestRunImageList_VisibilityFilter(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/images", func(w http.ResponseWriter, r *http.Request) {
		th.TestFormValues(t, r, map[string]string{
			"visibility": "private",
			"name":       "ubuntu",
		})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"images": []}`))
	})

	client := imageClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}
	f := &imageListFlags{private: true, name: "ubuntu"}

	var buf bytes.Buffer
	if err := runImageList(context.Background(), client, o, f, &buf); err != nil {
		t.Fatalf("runImageList returned error: %v", err)
	}
}

const imageCreateResponse = `{
  "id": "33333333-3333-3333-3333-333333333333",
  "name": "myimage",
  "status": "queued",
  "visibility": "private",
  "disk_format": "qcow2",
  "container_format": "bare",
  "min_disk": 5,
  "min_ram": 1024
}`

func TestRunImageCreate_RequestBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var body map[string]any
	fakeServer.Mux.HandleFunc("/images", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("decoding request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(imageCreateResponse))
	})

	client := imageClient(fakeServer)
	o := &output.Options{Format: output.FormatTable}
	f := &imageCreateFlags{
		diskFormat:      "qcow2",
		containerFormat: "bare",
		minDisk:         5,
		minRAM:          1024,
		private:         true,
		property:        []string{"hw_disk_bus=scsi"},
	}

	var buf bytes.Buffer
	if err := runImageCreate(context.Background(), client, o, "myimage", f, &buf); err != nil {
		t.Fatalf("runImageCreate returned error: %v", err)
	}

	checks := map[string]any{
		"name":             "myimage",
		"disk_format":      "qcow2",
		"container_format": "bare",
		"visibility":       "private",
		"hw_disk_bus":      "scsi",
	}
	for k, want := range checks {
		if got, ok := body[k]; !ok || got != want {
			t.Errorf("request body[%q] = %v, want %v", k, body[k], want)
		}
	}
	// min_disk / min_ram are JSON numbers.
	if v, _ := body["min_disk"].(float64); v != 5 {
		t.Errorf("request body min_disk = %v, want 5", body["min_disk"])
	}
	if v, _ := body["min_ram"].(float64); v != 1024 {
		t.Errorf("request body min_ram = %v, want 1024", body["min_ram"])
	}

	if !strings.Contains(buf.String(), "myimage") {
		t.Errorf("output missing created image name:\n%s", buf.String())
	}
}

func TestRunImageAddProject_MemberPost(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const imageID = "44444444-4444-4444-4444-444444444444"
	const projectID = "proj-xyz"

	var gotMethod string
	var body map[string]any
	fakeServer.Mux.HandleFunc("/images/"+imageID+"/members", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"image_id": "` + imageID + `",
			"member_id": "` + projectID + `",
			"status": "pending",
			"schema": "/v2/schemas/member"
		}`))
	})

	client := imageClient(fakeServer)
	o := &output.Options{Format: output.FormatTable}

	var buf bytes.Buffer
	if err := runImageAddProject(context.Background(), client, o, imageID, projectID, &buf); err != nil {
		t.Fatalf("runImageAddProject returned error: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("member request method = %q, want POST", gotMethod)
	}
	if got := body["member"]; got != projectID {
		t.Errorf("member request body member = %v, want %q", got, projectID)
	}
	out := buf.String()
	for _, want := range []string{projectID, "pending", imageID} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s", want, out)
		}
	}
}

func TestRunImageSave_ToWriter(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const imageID = "55555555-5555-5555-5555-555555555555"
	const payload = "raw-image-bytes"

	var gotMethod, gotPath string
	fakeServer.Mux.HandleFunc("/images/"+imageID+"/file", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(payload))
	})

	client := imageClient(fakeServer)

	var buf bytes.Buffer
	if err := runImageSave(context.Background(), client, imageID, &imageSaveFlags{}, &buf); err != nil {
		t.Fatalf("runImageSave returned error: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("download method = %q, want GET", gotMethod)
	}
	if gotPath != "/images/"+imageID+"/file" {
		t.Errorf("download path = %q, want /images/%s/file", gotPath, imageID)
	}
	if buf.String() != payload {
		t.Errorf("saved data = %q, want %q", buf.String(), payload)
	}
}

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

const imageShowBody = `{
  "id": "66666666-6666-6666-6666-666666666666",
  "name": "debian",
  "status": "active",
  "visibility": "private",
  "protected": false,
  "disk_format": "qcow2",
  "container_format": "bare",
  "size": 42,
  "min_disk": 2,
  "min_ram": 512,
  "owner": "proj-c",
  "checksum": "deadbeef"
}`

func TestRunImageShow_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const imageID = "66666666-6666-6666-6666-666666666666"

	var gotMethod, gotPath string
	fakeServer.Mux.HandleFunc("/images/"+imageID, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(imageShowBody))
	})

	client := imageClient(fakeServer)
	o := &output.Options{Format: output.FormatTable}

	var buf bytes.Buffer
	if err := runImageShow(context.Background(), client, o, imageID, &buf); err != nil {
		t.Fatalf("runImageShow returned error: %v", err)
	}

	if gotMethod != http.MethodGet {
		t.Errorf("request method = %q, want GET", gotMethod)
	}
	if gotPath != "/images/"+imageID {
		t.Errorf("request path = %q, want /images/%s", gotPath, imageID)
	}

	out := buf.String()
	for _, want := range []string{imageID, "debian", "active", "proj-c", "deadbeef"} {
		if !strings.Contains(out, want) {
			t.Errorf("show output missing %q\n---\n%s", want, out)
		}
	}
}

func TestRunImageDelete_RequestsAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	ids := []string{
		"aaaaaaaa-0000-0000-0000-000000000001",
		"aaaaaaaa-0000-0000-0000-000000000002",
	}

	deleted := map[string]string{} // id -> method
	for _, id := range ids {
		id := id
		fakeServer.Mux.HandleFunc("/images/"+id, func(w http.ResponseWriter, r *http.Request) {
			deleted[id] = r.Method
			th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
			w.WriteHeader(http.StatusNoContent)
		})
	}

	client := imageClient(fakeServer)

	var buf bytes.Buffer
	if err := runImageDelete(context.Background(), client, ids, &buf); err != nil {
		t.Fatalf("runImageDelete returned error: %v", err)
	}

	for _, id := range ids {
		if deleted[id] != http.MethodDelete {
			t.Errorf("delete method for %s = %q, want DELETE", id, deleted[id])
		}
		if !strings.Contains(buf.String(), "Deleted image "+id) {
			t.Errorf("output missing deletion line for %s\n---\n%s", id, buf.String())
		}
	}
}

// decodePatchOps reads a glance JSON-patch request body into a slice of op maps.
func decodePatchOps(t *testing.T, r *http.Request) []map[string]any {
	t.Helper()
	raw, _ := io.ReadAll(r.Body)
	var ops []map[string]any
	if err := json.Unmarshal(raw, &ops); err != nil {
		t.Fatalf("decoding patch body %q: %v", string(raw), err)
	}
	return ops
}

// findOp returns the first patch op with the given JSON-pointer path.
func findOp(ops []map[string]any, path string) map[string]any {
	for _, op := range ops {
		if op["path"] == path {
			return op
		}
	}
	return nil
}

const imageUpdateBody = `{
  "id": "77777777-7777-7777-7777-777777777777",
  "name": "renamed",
  "status": "active",
  "visibility": "public",
  "disk_format": "qcow2",
  "container_format": "bare",
  "min_disk": 20,
  "min_ram": 2048,
  "owner": "proj-d"
}`

func TestRunImageSet_PatchRequest(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const imageID = "77777777-7777-7777-7777-777777777777"

	var gotMethod, gotPath, gotContentType string
	var ops []map[string]any
	fakeServer.Mux.HandleFunc("/images/"+imageID, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		ops = decodePatchOps(t, r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(imageUpdateBody))
	})

	client := imageClient(fakeServer)
	o := &output.Options{Format: output.FormatTable}
	f := &imageSetFlags{
		name:       "renamed",
		minDisk:    20,
		minDiskSet: true,
		minRAM:     2048,
		minRAMSet:  true,
		public:     true,
		property:   []string{"hw_disk_bus=scsi"},
	}

	var buf bytes.Buffer
	if err := runImageSet(context.Background(), client, o, imageID, f, &buf); err != nil {
		t.Fatalf("runImageSet returned error: %v", err)
	}

	if gotMethod != http.MethodPatch {
		t.Errorf("request method = %q, want PATCH", gotMethod)
	}
	if gotPath != "/images/"+imageID {
		t.Errorf("request path = %q, want /images/%s", gotPath, imageID)
	}
	if gotContentType != "application/openstack-images-v2.1-json-patch" {
		t.Errorf("Content-Type = %q, want application/openstack-images-v2.1-json-patch", gotContentType)
	}

	wantReplace := map[string]any{
		"/name":     "renamed",
		"/min_disk": float64(20),
		"/min_ram":  float64(2048),
	}
	for path, want := range wantReplace {
		op := findOp(ops, path)
		if op == nil {
			t.Errorf("patch missing op for path %q; ops=%v", path, ops)
			continue
		}
		if op["op"] != "replace" {
			t.Errorf("patch %q op = %v, want replace", path, op["op"])
		}
		if op["value"] != want {
			t.Errorf("patch %q value = %v, want %v", path, op["value"], want)
		}
	}
	if op := findOp(ops, "/visibility"); op == nil || op["op"] != "replace" || op["value"] != "public" {
		t.Errorf("visibility patch = %v, want replace public", op)
	}
	// Property add uses op "add" and carries the value.
	if op := findOp(ops, "/hw_disk_bus"); op == nil || op["op"] != "add" || op["value"] != "scsi" {
		t.Errorf("property patch = %v, want add scsi", op)
	}

	if !strings.Contains(buf.String(), "renamed") {
		t.Errorf("output missing updated name:\n%s", buf.String())
	}
}

func TestRunImageUnset_PatchRemove(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const imageID = "88888888-8888-8888-8888-888888888888"

	var gotMethod, gotContentType string
	var ops []map[string]any
	fakeServer.Mux.HandleFunc("/images/"+imageID, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotContentType = r.Header.Get("Content-Type")
		ops = decodePatchOps(t, r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"id": "` + imageID + `",
			"name": "img",
			"status": "active",
			"visibility": "private",
			"disk_format": "qcow2",
			"container_format": "bare"
		}`))
	})

	client := imageClient(fakeServer)
	o := &output.Options{Format: output.FormatTable}
	f := &imageUnsetFlags{property: []string{"hw_disk_bus"}}

	var buf bytes.Buffer
	if err := runImageUnset(context.Background(), client, o, imageID, f, &buf); err != nil {
		t.Fatalf("runImageUnset returned error: %v", err)
	}

	if gotMethod != http.MethodPatch {
		t.Errorf("request method = %q, want PATCH", gotMethod)
	}
	if gotContentType != "application/openstack-images-v2.1-json-patch" {
		t.Errorf("Content-Type = %q, want application/openstack-images-v2.1-json-patch", gotContentType)
	}
	op := findOp(ops, "/hw_disk_bus")
	if op == nil {
		t.Fatalf("patch missing remove op; ops=%v", ops)
	}
	if op["op"] != "remove" {
		t.Errorf("op = %v, want remove", op["op"])
	}
	if _, ok := op["value"]; ok {
		t.Errorf("remove op should carry no value; got %v", op)
	}
}

func TestRunImageUnset_NoPropertyErrors(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	client := imageClient(fakeServer) // never contacted; error is returned before any request
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runImageUnset(context.Background(), client, o, "id", &imageUnsetFlags{}, &buf)
	if err == nil {
		t.Fatal("expected error when no --property is given")
	}
}

func TestRunImageRemoveProject_MemberDelete(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const imageID = "99999999-9999-9999-9999-999999999999"
	const projectID = "proj-zzz"

	var gotMethod, gotPath string
	fakeServer.Mux.HandleFunc("/images/"+imageID+"/members/"+projectID, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		w.WriteHeader(http.StatusNoContent)
	})

	client := imageClient(fakeServer)

	var buf bytes.Buffer
	if err := runImageRemoveProject(context.Background(), client, imageID, projectID, &buf); err != nil {
		t.Fatalf("runImageRemoveProject returned error: %v", err)
	}

	if gotMethod != http.MethodDelete {
		t.Errorf("request method = %q, want DELETE", gotMethod)
	}
	if want := "/images/" + imageID + "/members/" + projectID; gotPath != want {
		t.Errorf("request path = %q, want %q", gotPath, want)
	}
	if want := "Removed project " + projectID + " from image " + imageID; !strings.Contains(buf.String(), want) {
		t.Errorf("output missing %q\n---\n%s", want, buf.String())
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

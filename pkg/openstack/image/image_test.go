package image_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/imageimport"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/images"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/tasks"
	th "github.com/gophercloud/gophercloud/v2/testhelper"
	fakeclient "github.com/gophercloud/gophercloud/v2/testhelper/client"

	"github.com/ftarasenko/go-openstackclient/pkg/openstack/image"
)

const imageID = "0b1c2d3e-4f5a-4b6c-8d7e-9f0a1b2c3d4e"

// imageJSON is a minimal glance image document.
func imageJSON(status string) string {
	return `{"id":"` + imageID + `","name":"cirros","status":"` + status + `","visibility":"private",
		"disk_format":"qcow2","container_format":"bare","size":13,"min_disk":0,"min_ram":0,
		"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`
}

// recorder records the requests a glance fake served, in order.
type recorder struct {
	mu   sync.Mutex
	seen []string
}

func (r *recorder) add(req *http.Request) {
	r.mu.Lock()
	r.seen = append(r.seen, req.Method+" "+req.URL.Path)
	r.mu.Unlock()
}

func (r *recorder) all() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.seen, ", ")
}

func TestListImages(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/images", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		th.TestFormValues(t, r, map[string]string{"visibility": "all"})
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"images":[`+imageJSON("active")+`]}`)
	})
	all, err := image.ListImages(context.Background(), fakeclient.ServiceClient(fakeServer), images.ListOpts{Visibility: image.VisibilityAll})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].ID != imageID {
		t.Errorf("got %+v", all)
	}
}

func TestGetImage_WrapsWithTheID(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/images/"+imageID, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	_, err := image.GetImage(context.Background(), fakeclient.ServiceClient(fakeServer), imageID)
	if err == nil || !strings.HasPrefix(err.Error(), "getting image "+imageID+": ") {
		t.Fatalf("err = %v", err)
	}
	if !gophercloud.ResponseCodeIs(err, http.StatusNotFound) {
		t.Error("gophercloud's 404 must stay reachable through the wrap")
	}
}

func TestActivateDeactivate(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	var rec recorder
	for _, action := range []string{"reactivate", "deactivate"} {
		fakeServer.Mux.HandleFunc("/images/"+imageID+"/actions/"+action, func(w http.ResponseWriter, r *http.Request) {
			th.TestMethod(t, r, http.MethodPost)
			th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
			rec.add(r)
			w.WriteHeader(http.StatusNoContent)
		})
	}
	sc := fakeclient.ServiceClient(fakeServer)
	if err := image.ActivateImage(context.Background(), sc, imageID); err != nil {
		t.Fatal(err)
	}
	if err := image.DeactivateImage(context.Background(), sc, imageID); err != nil {
		t.Fatal(err)
	}
	want := "POST /images/" + imageID + "/actions/reactivate, POST /images/" + imageID + "/actions/deactivate"
	if rec.all() != want {
		t.Errorf("requests = %s, want %s", rec.all(), want)
	}
}

func TestUpdateImage_EscapedProperty(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/images/"+imageID, func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPatch)
		th.TestHeader(t, r, "Content-Type", "application/openstack-images-v2.1-json-patch")
		th.TestJSONRequest(t, r, `[{"op":"add","path":"/os~1distro~0x","value":"ubuntu"}]`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, imageJSON("active"))
	})
	ops := images.UpdateOpts{images.UpdateImageProperty{Op: images.AddOp, Name: image.EscapePropertyName("os/distro~x"), Value: "ubuntu"}}
	if _, err := image.UpdateImage(context.Background(), fakeclient.ServiceClient(fakeServer), imageID, ops); err != nil {
		t.Fatal(err)
	}
}

// glance is a fake that creates imageID, accepts its data and advertises the
// given import methods.
func glance(t *testing.T, methods string, uploadStatus int) (*gophercloud.ServiceClient, *recorder) {
	t.Helper()
	fakeServer := th.SetupHTTP()
	t.Cleanup(fakeServer.Teardown)
	rec := &recorder{}
	fakeServer.Mux.HandleFunc("/images", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		rec.add(r)
		if methods != "" {
			w.Header().Set("OpenStack-image-import-methods", methods)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, imageJSON("queued"))
	})
	fakeServer.Mux.HandleFunc("/images/"+imageID, func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, imageJSON("active"))
	})
	for _, part := range []string{"file", "stage"} {
		fakeServer.Mux.HandleFunc("/images/"+imageID+"/"+part, func(w http.ResponseWriter, r *http.Request) {
			th.TestMethod(t, r, http.MethodPut)
			th.TestHeader(t, r, "Content-Type", "application/octet-stream")
			th.TestHeader(t, r, "X-OpenStack-Image-Size", "13")
			if b, _ := io.ReadAll(r.Body); string(b) != "image-bytes\r\n" {
				t.Errorf("body = %q", b)
			}
			rec.add(r)
			w.WriteHeader(uploadStatus)
		})
	}
	fakeServer.Mux.HandleFunc("/images/"+imageID+"/import", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		th.TestJSONRequest(t, r, `{"method":{"name":"glance-direct","uri":""}}`)
		rec.add(r)
		w.WriteHeader(http.StatusAccepted)
	})
	return fakeclient.ServiceClient(fakeServer), rec
}

func data(glanceDirect bool) *image.Data {
	return &image.Data{Body: strings.NewReader("image-bytes\r\n"), Size: 13, GlanceDirect: glanceDirect}
}

func TestCreateImageWithData_Upload(t *testing.T) {
	sc, rec := glance(t, "", http.StatusNoContent)
	img, err := image.CreateImageWithData(context.Background(), sc, images.CreateOpts{Name: "cirros"}, data(false))
	if err != nil {
		t.Fatal(err)
	}
	if img.Status != images.ImageStatusActive {
		t.Errorf("status = %s; the record must be re-read after the upload", img.Status)
	}
	want := "POST /images, PUT /images/" + imageID + "/file, GET /images/" + imageID
	if rec.all() != want {
		t.Errorf("requests = %s\nwant       %s", rec.all(), want)
	}
}

func TestCreateImageWithData_GlanceDirect(t *testing.T) {
	sc, rec := glance(t, "glance-direct,web-download", http.StatusNoContent)
	if _, err := image.CreateImageWithData(context.Background(), sc, images.CreateOpts{Name: "cirros"}, data(true)); err != nil {
		t.Fatal(err)
	}
	want := "POST /images, PUT /images/" + imageID + "/stage, POST /images/" + imageID + "/import, GET /images/" + imageID
	if rec.all() != want {
		t.Errorf("requests = %s\nwant       %s", rec.all(), want)
	}
}

func TestCreateImageWithData_DiscardsAFailedRecord(t *testing.T) {
	sc, rec := glance(t, "web-download", http.StatusNoContent)
	_, err := image.CreateImageWithData(context.Background(), sc, images.CreateOpts{Name: "cirros"}, data(true))
	if !errors.Is(err, image.ErrGlanceDirectUnavailable) {
		t.Fatalf("err = %v, want ErrGlanceDirectUnavailable", err)
	}
	if !strings.HasSuffix(err.Error(), "(the partial image was deleted)") {
		t.Errorf("err = %q", err)
	}
	if want := "POST /images, DELETE /images/" + imageID; rec.all() != want {
		t.Errorf("requests = %s, want %s", rec.all(), want)
	}

	sc, rec = glance(t, "", http.StatusServiceUnavailable)
	_, err = image.CreateImageWithData(context.Background(), sc, images.CreateOpts{Name: "cirros"}, data(false))
	if err == nil || !strings.HasPrefix(err.Error(), "uploading image data for "+imageID+": ") {
		t.Fatalf("err = %v", err)
	}
	if !strings.HasSuffix(rec.all(), "DELETE /images/"+imageID) {
		t.Errorf("requests = %s; a failed upload must delete the record", rec.all())
	}
}

func TestImportImage_MultiStoreKeys(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/images/"+imageID+"/import", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		th.TestJSONRequest(t, r, `{"method":{"name":"web-download","uri":"https://images.example.com/cirros.img"},"stores":["fast","cheap"]}`)
		w.WriteHeader(http.StatusAccepted)
	})
	err := image.ImportImage(context.Background(), fakeclient.ServiceClient(fakeServer), imageID, image.ImportOpts{
		CreateOpts: imageimport.CreateOpts{Name: imageimport.WebDownloadMethod, URI: "https://images.example.com/cirros.img"},
		Stores:     []string{"fast", "cheap"},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestListTasks_TypeFilterAndCap(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	requests := 0
	fakeServer.Mux.HandleFunc("/tasks", func(w http.ResponseWriter, r *http.Request) {
		requests++
		th.TestFormValues(t, r, map[string]string{"type": "import", "status": "success", "limit": "1"})
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"tasks":[{"id":"t-1","type":"import","status":"success"}],
			"next":"/v2/tasks?marker=t-1&type=import&status=success&limit=1"}`)
	})
	got, err := image.ListTasks(context.Background(), fakeclient.ServiceClient(fakeServer), image.TaskListOpts{
		ListOpts: tasks.ListOpts{Status: tasks.TaskStatusSuccess, Limit: 1},
		Type:     "import",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || requests != 1 {
		t.Errorf("got %d tasks in %d requests; the limit caps both", len(got), requests)
	}
}

func TestListStores_QuotedBooleans(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/info/stores/detail", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"stores":[{"id":"fast","default":"true","properties":{"pool":"images"}},
			{"id":"web","read-only":true}]}`)
	})
	stores, err := image.ListStores(context.Background(), fakeclient.ServiceClient(fakeServer), true)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(stores)
	want := `[{"ID":"fast","Description":"","Default":true,"ReadOnly":false,"Properties":{"pool":"images"}},` +
		`{"ID":"web","Description":"","Default":false,"ReadOnly":true,"Properties":null}]`
	if string(b) != want {
		t.Errorf("stores = %s\nwant     %s", b, want)
	}
}

func TestListStores_NoMultiStore(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/info/stores", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	_, err := image.ListStores(context.Background(), fakeclient.ServiceClient(fakeServer), false)
	if err == nil || !strings.HasPrefix(err.Error(), "this glance has no multi-store support enabled: ") {
		t.Errorf("err = %v", err)
	}
}

func TestMembers(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	const project = "5f0f658d5bc34e1fb3fb0862a79489a4"
	fakeServer.Mux.HandleFunc("/images/"+imageID+"/members", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			th.TestJSONRequest(t, r, `{"member":"`+project+`"}`)
		}
		_, _ = io.WriteString(w, `{"image_id":"`+imageID+`","member_id":"`+project+`","status":"pending",
			"members":[{"image_id":"`+imageID+`","member_id":"`+project+`","status":"pending"}]}`)
	})
	fakeServer.Mux.HandleFunc("/images/"+imageID+"/members/"+project, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		th.TestJSONRequest(t, r, `{"status":"accepted"}`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"image_id":"`+imageID+`","member_id":"`+project+`","status":"accepted"}`)
	})
	ctx, sc := context.Background(), fakeclient.ServiceClient(fakeServer)
	if _, err := image.AddMember(ctx, sc, imageID, project); err != nil {
		t.Fatal(err)
	}
	if all, err := image.ListMembers(ctx, sc, imageID); err != nil || len(all) != 1 {
		t.Fatalf("ListMembers = %v, %v", all, err)
	}
	if m, err := image.UpdateMember(ctx, sc, imageID, project, "accepted"); err != nil || m.Status != "accepted" {
		t.Fatalf("UpdateMember = %v, %v", m, err)
	}
	if err := image.RemoveMember(ctx, sc, imageID, project); err != nil {
		t.Fatal(err)
	}
}

func TestDownloadImage_Streams(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/images/"+imageID+"/file", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = io.WriteString(w, "image-bytes")
	})
	body, err := image.DownloadImage(context.Background(), fakeclient.ServiceClient(fakeServer), imageID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = body.Close() }()
	if b, _ := io.ReadAll(body); string(b) != "image-bytes" {
		t.Errorf("body = %q", b)
	}
}

func TestWaitForStatus_StopsOnATerminalStatusAndOnTheDeadline(t *testing.T) {
	for _, tc := range []struct {
		status, want string
		timeout      time.Duration
	}{
		{"killed", "waiting for image " + imageID + " to be active: it is killed", time.Minute},
		{"importing", "waiting for image " + imageID + " to be active (still importing): context deadline exceeded", 20 * time.Millisecond},
	} {
		fakeServer := th.SetupHTTP()
		fakeServer.Mux.HandleFunc("/images/"+imageID, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, imageJSON(tc.status))
		})
		ctx, cancel := context.WithTimeout(context.Background(), tc.timeout)
		_, err := image.WaitForStatus(ctx, fakeclient.ServiceClient(fakeServer), imageID, images.ImageStatusActive, time.Millisecond)
		cancel()
		fakeServer.Teardown()
		if err == nil || err.Error() != tc.want {
			t.Errorf("%s: err = %v, want %q", tc.status, err, tc.want)
		}
	}
}

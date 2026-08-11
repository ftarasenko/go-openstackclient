package image

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

const taskID = "88888888-8888-8888-8888-888888888888"

func TestRunImageTaskList_TypeFilterReachesTheQuery(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotQuery string
	fakeServer.Mux.HandleFunc("/tasks", func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tasks": [{"id": "` + taskID + `", "type": "import",
		  "status": "success", "owner": "proj-1"}]}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	f := &imageTaskListFlags{status: "success", typ: "import"}
	client := imageClient(fakeServer)
	if err := runImageTaskList(context.Background(), client, o, f, &out); err != nil {
		t.Fatalf("runImageTaskList returned error: %v", err)
	}

	// gophercloud tags ListOpts.Type with `json:` rather than `q:`, so
	// BuildQueryString drops it and --type would silently do nothing.
	if !strings.Contains(gotQuery, "type=import") {
		t.Errorf("query %q is missing the type filter", gotQuery)
	}
	if !strings.Contains(gotQuery, "status=success") {
		t.Errorf("query %q is missing the status filter", gotQuery)
	}
	if !strings.HasPrefix(out.String(), taskID+"\timport\tsuccess\tproj-1") {
		t.Errorf("unexpected output: %q", out.String())
	}
}

func TestRunImageTaskShow_RendersMessageAndResult(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/tasks/"+taskID, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id": "` + taskID + `", "type": "import", "status": "failure",
		  "owner": "proj-1", "message": "image data not found",
		  "input": {"import_from": "http://example.com/img"}, "result": null}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	client := imageClient(fakeServer)
	if err := runImageTaskShow(context.Background(), client, o, taskID, &out); err != nil {
		t.Fatalf("runImageTaskShow returned error: %v", err)
	}
	// The message is the only place glance explains a failed task.
	if !strings.Contains(out.String(), "image data not found") {
		t.Errorf("output is missing the failure message:\n%s", out.String())
	}
}

func TestRunImageStage_PutsBodyToTheStagingEndpoint(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const imageID = "99999999-9999-9999-9999-999999999999"
	fakeServer.Mux.HandleFunc("/images/"+imageID, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id": "` + imageID + `", "name": "cirros", "status": "queued"}`))
	})
	var gotMethod, gotBody string
	fakeServer.Mux.HandleFunc("/images/"+imageID+"/stage", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		w.WriteHeader(http.StatusNoContent)
	})

	var out bytes.Buffer
	client := imageClient(fakeServer)
	err := runImageStage(context.Background(), client, imageID, "", strings.NewReader("disk-bytes"), &out)
	if err != nil {
		t.Fatalf("runImageStage returned error: %v", err)
	}
	th.AssertEquals(t, "PUT", gotMethod)
	th.AssertEquals(t, "disk-bytes", gotBody)
}

func TestRunImageStoresList_DetailSelectsTheOtherEndpoint(t *testing.T) {
	for _, tc := range []struct {
		detail bool
		path   string
	}{
		{false, "/info/stores"},
		{true, "/info/stores/detail"},
	} {
		fakeServer := th.SetupHTTP()

		var gotPath string
		handler := func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"stores": [
			  {"id": "cheap", "description": "spinning disks", "default": true,
			   "properties": {"read-only": false}}
			]}`))
		}
		fakeServer.Mux.HandleFunc("/info/stores", handler)
		fakeServer.Mux.HandleFunc("/info/stores/detail", handler)

		var out bytes.Buffer
		o := &output.Options{Format: "value"}
		client := imageClient(fakeServer)
		if err := runImageStoresList(context.Background(), client, o, tc.detail, &out); err != nil {
			t.Fatalf("runImageStoresList(detail=%v) returned error: %v", tc.detail, err)
		}
		// --detail is admin-only, so it must not be the default path.
		th.AssertEquals(t, tc.path, gotPath)
		if !strings.Contains(out.String(), "cheap") {
			t.Errorf("output is missing the store:\n%s", out.String())
		}
		fakeServer.Teardown()
	}
}

// glance spells these flags as quoted strings, not JSON booleans. A real
// deployment answers:
//
//	{"stores": [{"id": "file", "default": "true"}, {"id": "http", "read-only": "true"}]}
//
// Decoding "default" into a plain bool failed the whole document and took the
// command out with a Go type error.
func TestRunImageStoresList_AcceptsQuotedBooleans(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/info/stores", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"stores": [
		  {"id": "file", "default": "true"},
		  {"id": "http", "read-only": "true"},
		  {"id": "cinder"}
		]}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	client := imageClient(fakeServer)
	if err := runImageStoresList(context.Background(), client, o, false, &out); err != nil {
		t.Fatalf("runImageStoresList returned error: %v", err)
	}
	got := out.String()
	for _, want := range []string{"file", "http", "cinder"} {
		if !strings.Contains(got, want) {
			t.Errorf("output is missing store %q:\n%s", want, got)
		}
	}
	// "file" is the default and is not read-only; "http" is the reverse. The
	// quoted "true" must arrive as a real boolean, not as the string.
	if !strings.Contains(got, "file\t\ttrue\tfalse") {
		t.Errorf("file store did not render default=true read-only=false:\n%s", got)
	}
	if !strings.Contains(got, "http\t\tfalse\ttrue") {
		t.Errorf("http store did not render default=false read-only=true:\n%s", got)
	}
}

func TestRunImageStoresList_404ExplainsMultiStoreIsOff(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	// glance only registers this endpoint when multi-store is configured, so a
	// 404 is a configuration answer rather than a missing resource.
	fakeServer.Mux.HandleFunc("/info/stores", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	client := imageClient(fakeServer)
	err := runImageStoresList(context.Background(), client, o, false, &out)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "multi-store") {
		t.Errorf("error %q does not explain that multi-store is disabled", err)
	}
}

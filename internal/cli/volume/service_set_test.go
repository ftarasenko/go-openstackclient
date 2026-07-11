package volume

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"
)

func TestRunServiceSet_DisableWithReason(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotPath string
	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/os-services/disable-log-reason", func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"host":"h1","binary":"cinder-volume","status":"disabled","disabled_reason":"maint"}`))
	})

	client := volumeClient(fakeServer, "3.0")
	f := &serviceSetFlags{disableReason: "maint"}
	var buf bytes.Buffer
	if err := runServiceSet(context.Background(), client, "h1", "cinder-volume", f, &buf); err != nil {
		t.Fatalf("runServiceSet: %v", err)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
	if gotPath != "/os-services/disable-log-reason" {
		t.Errorf("path = %q, want the disable-log-reason endpoint", gotPath)
	}
	if gotBody["host"] != "h1" || gotBody["binary"] != "cinder-volume" || gotBody["disabled_reason"] != "maint" {
		t.Errorf("unexpected request body: %#v", gotBody)
	}
}

func TestRunServiceSet_EnableHitsEnableEndpoint(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotPath string
	fakeServer.Mux.HandleFunc("/os-services/enable", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"host":"h1","binary":"cinder-volume","status":"enabled"}`))
	})

	client := volumeClient(fakeServer, "3.0")
	f := &serviceSetFlags{enable: true}
	var buf bytes.Buffer
	if err := runServiceSet(context.Background(), client, "h1", "cinder-volume", f, &buf); err != nil {
		t.Fatalf("runServiceSet: %v", err)
	}
	if gotPath != "/os-services/enable" {
		t.Errorf("path = %q, want /os-services/enable", gotPath)
	}
}

package baremetal

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

func TestRunNodeBootDeviceSet_RequestBodyAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotIronicVersion string
	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/nodes/node-a/management/boot_device", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotIronicVersion = r.Header.Get("X-OpenStack-Ironic-API-Version")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusNoContent)
	})

	client := baremetalClient(fakeServer, "1.80")

	var buf bytes.Buffer
	if err := runNodeBootDeviceSet(context.Background(), client, "node-a", "pxe", true, &buf); err != nil {
		t.Fatalf("runNodeBootDeviceSet returned error: %v", err)
	}

	if gotMethod != http.MethodPut {
		t.Errorf("request method = %q, want PUT", gotMethod)
	}
	if gotIronicVersion != "1.80" {
		t.Errorf("X-OpenStack-Ironic-API-Version = %q, want 1.80", gotIronicVersion)
	}
	if gotBody["boot_device"] != "pxe" {
		t.Errorf("body boot_device = %v, want pxe", gotBody["boot_device"])
	}
	if gotBody["persistent"] != true {
		t.Errorf("body persistent = %v, want true", gotBody["persistent"])
	}
	if !strings.Contains(buf.String(), "Set boot device of node node-a to pxe") {
		t.Errorf("unexpected output: %q", buf.String())
	}
}

func TestRunNodeBootDeviceShow_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	fakeServer.Mux.HandleFunc("/nodes/node-a/management/boot_device", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"boot_device": "disk", "persistent": true}`))
	})

	client := baremetalClient(fakeServer, "1.80")
	o := &output.Options{Format: output.FormatValue}

	var buf bytes.Buffer
	if err := runNodeBootDeviceShow(context.Background(), client, o, "node-a", &buf); err != nil {
		t.Fatalf("runNodeBootDeviceShow returned error: %v", err)
	}

	if gotMethod != http.MethodGet {
		t.Errorf("request method = %q, want GET", gotMethod)
	}
	out := buf.String()
	if !strings.Contains(out, "disk") {
		t.Errorf("boot device show output missing %q\n---\n%s", "disk", out)
	}
}

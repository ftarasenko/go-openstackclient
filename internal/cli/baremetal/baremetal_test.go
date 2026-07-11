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
	fakeclient "github.com/gophercloud/gophercloud/v2/testhelper/client"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

const portListBody = `{
  "ports": [
    {
      "uuid": "aaaaaaaa-0000-0000-0000-000000000001",
      "address": "11:22:33:44:55:66",
      "node_uuid": "11111111-1111-1111-1111-111111111111",
      "pxe_enabled": true,
      "physical_network": "physnet1"
    },
    {
      "uuid": "aaaaaaaa-0000-0000-0000-000000000002",
      "address": "aa:bb:cc:dd:ee:ff",
      "node_uuid": "22222222-2222-2222-2222-222222222222",
      "pxe_enabled": false,
      "physical_network": ""
    }
  ]
}`

func TestRunPortList_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotIronicVersion string
	fakeServer.Mux.HandleFunc("/ports", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotIronicVersion = r.Header.Get("X-OpenStack-Ironic-API-Version")
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(portListBody))
	})

	client := baremetalClient(fakeServer, "1.80")
	o := &output.Options{Format: output.FormatTable}

	var buf bytes.Buffer
	if err := runPortList(context.Background(), client, o, &portListFlags{}, &buf); err != nil {
		t.Fatalf("runPortList returned error: %v", err)
	}

	if gotMethod != http.MethodGet {
		t.Errorf("request method = %q, want GET", gotMethod)
	}
	if gotIronicVersion != "1.80" {
		t.Errorf("X-OpenStack-Ironic-API-Version = %q, want 1.80", gotIronicVersion)
	}

	out := buf.String()
	for _, want := range []string{
		"UUID", "Address",
		"aaaaaaaa-0000-0000-0000-000000000001", "11:22:33:44:55:66",
		"aa:bb:cc:dd:ee:ff",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("port table output missing %q\n---\n%s", want, out)
		}
	}
	// --long columns should not appear by default.
	if strings.Contains(out, "Physical Network") {
		t.Errorf("default output should not contain --long columns:\n%s", out)
	}
}

const nodeShowBody = `{
  "uuid": "11111111-1111-1111-1111-111111111111",
  "name": "cmp-039",
  "power_state": "power on",
  "provision_state": "manageable",
  "target_provision_state": "",
  "maintenance": false,
  "driver": "ipmi",
  "resource_class": "baremetal"
}`

func TestRunNodeShow_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	fakeServer.Mux.HandleFunc("/nodes/cmp-039", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(nodeShowBody))
	})

	client := baremetalClient(fakeServer, "1.80")
	o := &output.Options{Format: output.FormatValue}

	var buf bytes.Buffer
	if err := runNodeShow(context.Background(), client, o, "cmp-039", &buf); err != nil {
		t.Fatalf("runNodeShow returned error: %v", err)
	}

	if gotMethod != http.MethodGet {
		t.Errorf("request method = %q, want GET", gotMethod)
	}
	out := buf.String()
	for _, want := range []string{"cmp-039", "manageable", "ipmi", "baremetal"} {
		if !strings.Contains(out, want) {
			t.Errorf("node show output missing %q\n---\n%s", want, out)
		}
	}
}

func TestRunNodeProvision_InspectRequest(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotIronicVersion string
	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/nodes/cmp-039/states/provision", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotIronicVersion = r.Header.Get("X-OpenStack-Ironic-API-Version")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusAccepted)
	})

	client := baremetalClient(fakeServer, "1.80")

	var tr provisionTransition
	for _, cand := range provisionTransitions() {
		if cand.verb == "inspect" {
			tr = cand
		}
	}

	var buf bytes.Buffer
	if err := runNodeProvision(context.Background(), client, tr, "cmp-039", false, &buf); err != nil {
		t.Fatalf("runNodeProvision returned error: %v", err)
	}

	if gotMethod != http.MethodPut {
		t.Errorf("request method = %q, want PUT", gotMethod)
	}
	if gotIronicVersion != "1.80" {
		t.Errorf("X-OpenStack-Ironic-API-Version = %q, want 1.80", gotIronicVersion)
	}
	if gotBody["target"] != "inspect" {
		t.Errorf("provision body target = %v, want inspect (body=%v)", gotBody["target"], gotBody)
	}
	if !strings.Contains(buf.String(), "Requested inspect") {
		t.Errorf("unexpected output: %q", buf.String())
	}
}

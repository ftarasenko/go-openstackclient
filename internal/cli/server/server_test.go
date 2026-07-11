package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
	th "github.com/gophercloud/gophercloud/v2/testhelper"
	fakeclient "github.com/gophercloud/gophercloud/v2/testhelper/client"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// computeClient returns a service client wired to the mock server with the nova
// service type + microversion, mirroring how auth.Client.Compute does.
func computeClient(fakeServer th.FakeServer, microversion string) *gophercloud.ServiceClient {
	sc := fakeclient.ServiceClient(fakeServer)
	sc.Type = "compute"
	sc.Microversion = microversion
	return sc
}

const serverListBody = `{
  "servers": [
    {
      "id": "11111111-1111-1111-1111-111111111111",
      "name": "web-1",
      "status": "ACTIVE",
      "addresses": {"private": [{"addr": "10.0.0.5", "version": 4}]},
      "flavor": {"original_name": "m1.small"},
      "image": {"id": "img-123"},
      "OS-EXT-AZ:availability_zone": "nova"
    },
    {
      "id": "22222222-2222-2222-2222-222222222222",
      "name": "web-2",
      "status": "SHUTOFF",
      "addresses": {},
      "flavor": {"original_name": "m1.large"},
      "image": {"id": "img-456"}
    }
  ]
}`

func TestRunServerList_RequestAndTableOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotNovaVersion, gotAPIVersion string
	fakeServer.Mux.HandleFunc("/servers/detail", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotNovaVersion = r.Header.Get("X-OpenStack-Nova-API-Version")
		gotAPIVersion = r.Header.Get("OpenStack-API-Version")
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(serverListBody))
	})

	client := computeClient(fakeServer, "2.79")
	o := &output.Options{Format: output.FormatTable}

	var buf bytes.Buffer
	if err := runServerList(context.Background(), client, o, &serverListFlags{}, &buf); err != nil {
		t.Fatalf("runServerList returned error: %v", err)
	}

	if gotMethod != http.MethodGet {
		t.Errorf("request method = %q, want GET", gotMethod)
	}
	if gotNovaVersion != "2.79" {
		t.Errorf("X-OpenStack-Nova-API-Version = %q, want 2.79", gotNovaVersion)
	}
	if gotAPIVersion != "compute 2.79" {
		t.Errorf("OpenStack-API-Version = %q, want %q", gotAPIVersion, "compute 2.79")
	}

	out := buf.String()
	for _, want := range []string{
		"ID", "Name", "Status", "Networks",
		"web-1", "web-2", "ACTIVE", "SHUTOFF",
		"11111111-1111-1111-1111-111111111111", "private=10.0.0.5",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("table output missing %q\n---\n%s", want, out)
		}
	}
	// --long columns should not appear by default.
	if strings.Contains(out, "m1.small") {
		t.Errorf("default output should not contain --long Flavor column:\n%s", out)
	}
}

func TestRunServerList_AllProjectsFilter(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/servers/detail", func(w http.ResponseWriter, r *http.Request) {
		th.TestFormValues(t, r, map[string]string{
			"all_tenants": "true",
			"name":        "web",
			"status":      "ACTIVE",
		})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"servers": []}`))
	})

	client := computeClient(fakeServer, "latest")
	o := &output.Options{Format: output.FormatValue}
	f := &serverListFlags{allProjects: true, name: "web", status: "ACTIVE"}

	var buf bytes.Buffer
	if err := runServerList(context.Background(), client, o, f, &buf); err != nil {
		t.Fatalf("runServerList returned error: %v", err)
	}
}

func TestRunSimpleAction_StartPostsOsStart(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const id = "11111111-1111-1111-1111-111111111111"
	var gotMethod string
	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/servers/"+id+"/action", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusAccepted)
	})

	client := computeClient(fakeServer, "latest")

	// Pass a UUID so resolveServerID uses it verbatim (no list call needed).
	var buf bytes.Buffer
	start := func(ctx context.Context, c *gophercloud.ServiceClient, sid string) error {
		return servers.Start(ctx, c, sid).ExtractErr()
	}
	if err := runSimpleAction(context.Background(), client, id, "Started", start, &buf); err != nil {
		t.Fatalf("runSimpleAction returned error: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("action method = %q, want POST", gotMethod)
	}
	if _, ok := gotBody["os-start"]; !ok {
		t.Errorf("action body = %v, want key %q", gotBody, "os-start")
	}
	if !strings.Contains(buf.String(), "Started server "+id) {
		t.Errorf("output = %q, want confirmation for %s", buf.String(), id)
	}
}

func TestRunServerAddFloatingIP_PinsLegacyMicroversion(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const id = "11111111-1111-1111-1111-111111111111"
	var gotNovaVersion string
	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/servers/"+id+"/action", func(w http.ResponseWriter, r *http.Request) {
		gotNovaVersion = r.Header.Get("X-OpenStack-Nova-API-Version")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusAccepted)
	})

	// Client negotiates "latest"; the floating-IP action must still pin 2.43.
	client := computeClient(fakeServer, "latest")

	var buf bytes.Buffer
	if err := runServerAddFloatingIP(context.Background(), client, id, "192.0.2.10", "", &buf); err != nil {
		t.Fatalf("runServerAddFloatingIP returned error: %v", err)
	}

	if gotNovaVersion != "2.43" {
		t.Errorf("X-OpenStack-Nova-API-Version = %q, want 2.43", gotNovaVersion)
	}
	if _, ok := gotBody["addFloatingIp"]; !ok {
		t.Errorf("action body = %v, want key %q", gotBody, "addFloatingIp")
	}
	// The caller's client Microversion must be left untouched by the shallow copy.
	if client.Microversion != "latest" {
		t.Errorf("client.Microversion = %q, want unchanged %q", client.Microversion, "latest")
	}
}

func TestRunServerRemoveFloatingIP_PinsLegacyMicroversion(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const id = "11111111-1111-1111-1111-111111111111"
	var gotNovaVersion string
	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/servers/"+id+"/action", func(w http.ResponseWriter, r *http.Request) {
		gotNovaVersion = r.Header.Get("X-OpenStack-Nova-API-Version")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusAccepted)
	})

	client := computeClient(fakeServer, "latest")

	var buf bytes.Buffer
	if err := runServerRemoveFloatingIP(context.Background(), client, id, "192.0.2.10", &buf); err != nil {
		t.Fatalf("runServerRemoveFloatingIP returned error: %v", err)
	}

	if gotNovaVersion != "2.43" {
		t.Errorf("X-OpenStack-Nova-API-Version = %q, want 2.43", gotNovaVersion)
	}
	if _, ok := gotBody["removeFloatingIp"]; !ok {
		t.Errorf("action body = %v, want key %q", gotBody, "removeFloatingIp")
	}
}

const serviceListBody = `{
  "services": [
    {
      "id": "aaaa1111-0000-0000-0000-000000000001",
      "binary": "nova-compute",
      "host": "compute-1",
      "zone": "nova",
      "status": "enabled",
      "state": "up",
      "updated_at": "2026-07-11T00:00:00.000000"
    },
    {
      "id": "aaaa1111-0000-0000-0000-000000000002",
      "binary": "nova-scheduler",
      "host": "controller-1",
      "zone": "internal",
      "status": "disabled",
      "state": "down",
      "disabled_reason": "maintenance",
      "updated_at": "2026-07-11T00:00:00.000000"
    }
  ]
}`

func TestRunComputeServiceList_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	fakeServer.Mux.HandleFunc("/os-services", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(serviceListBody))
	})

	client := computeClient(fakeServer, "2.79")
	o := &output.Options{Format: output.FormatTable}

	var buf bytes.Buffer
	if err := runComputeServiceList(context.Background(), client, o, &serviceListFlags{long: true}, &buf); err != nil {
		t.Fatalf("runComputeServiceList returned error: %v", err)
	}

	if gotMethod != http.MethodGet {
		t.Errorf("request method = %q, want GET", gotMethod)
	}
	out := buf.String()
	for _, want := range []string{
		"Binary", "Host", "Status", "State", "Disabled Reason",
		"nova-compute", "compute-1", "enabled", "up",
		"nova-scheduler", "controller-1", "disabled", "down", "maintenance",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("service list output missing %q\n---\n%s", want, out)
		}
	}
}

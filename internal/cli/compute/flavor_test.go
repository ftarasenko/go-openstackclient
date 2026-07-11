package compute

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

const flavorListBody = `{
  "flavors": [
    {
      "id": "1",
      "name": "m1.tiny",
      "ram": 512,
      "disk": 1,
      "vcpus": 1,
      "OS-FLV-EXT-DATA:ephemeral": 0,
      "swap": "",
      "rxtx_factor": 1.0,
      "os-flavor-access:is_public": true
    },
    {
      "id": "2",
      "name": "m1.small",
      "ram": 2048,
      "disk": 20,
      "vcpus": 1,
      "OS-FLV-EXT-DATA:ephemeral": 0,
      "swap": "",
      "rxtx_factor": 1.0,
      "os-flavor-access:is_public": false
    }
  ]
}`

// computeClient returns a service client wired to the mock server with the nova
// service type + microversion, mirroring how auth.Client.Compute does.
func computeClient(fakeServer th.FakeServer, microversion string) *gophercloud.ServiceClient {
	sc := fakeclient.ServiceClient(fakeServer)
	sc.Type = "compute"
	sc.Microversion = microversion
	return sc
}

func TestRunFlavorList_RequestAndTableOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotAPIVersion string
	fakeServer.Mux.HandleFunc("/flavors/detail", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotAPIVersion = r.Header.Get("OpenStack-API-Version")
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(flavorListBody))
	})

	client := computeClient(fakeServer, "2.61")
	o := &output.Options{Format: output.FormatTable}

	var buf bytes.Buffer
	if err := runFlavorList(context.Background(), client, o, &flavorListFlags{}, &buf); err != nil {
		t.Fatalf("runFlavorList returned error: %v", err)
	}

	if gotMethod != http.MethodGet {
		t.Errorf("request method = %q, want GET", gotMethod)
	}
	// nova emits the generic microversion header keyed on client.Type.
	if gotAPIVersion != "compute 2.61" {
		t.Errorf("OpenStack-API-Version = %q, want %q", gotAPIVersion, "compute 2.61")
	}

	out := buf.String()
	for _, want := range []string{
		"ID", "Name", "RAM", "Disk", "Ephemeral", "VCPUs", "Is Public",
		"m1.tiny", "m1.small", "512", "2048",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("table output missing %q\n---\n%s", want, out)
		}
	}
	// --long columns should NOT appear by default.
	if strings.Contains(out, "RXTX Factor") {
		t.Errorf("default output should not contain --long columns:\n%s", out)
	}
}

func TestRunFlavorList_PublicAccessFilter(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/flavors/detail", func(w http.ResponseWriter, r *http.Request) {
		th.TestFormValues(t, r, map[string]string{"is_public": "None"})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"flavors": []}`))
	})

	client := computeClient(fakeServer, "latest")
	o := &output.Options{Format: output.FormatValue}

	var buf bytes.Buffer
	if err := runFlavorList(context.Background(), client, o, &flavorListFlags{all: true}, &buf); err != nil {
		t.Fatalf("runFlavorList returned error: %v", err)
	}
}

func TestRunFlavorCreate_RequestBodyAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotAPIVersion string
	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/flavors", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotAPIVersion = r.Header.Get("OpenStack-API-Version")
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
  "flavor": {
    "id": "abc",
    "name": "m1.custom",
    "ram": 512,
    "disk": 1,
    "vcpus": 1,
    "OS-FLV-EXT-DATA:ephemeral": 0,
    "swap": "",
    "rxtx_factor": 1.0,
    "os-flavor-access:is_public": true
  }
}`))
	})

	client := computeClient(fakeServer, "2.1")
	o := &output.Options{Format: output.FormatValue}
	f := &flavorCreateFlags{ram: 512, disk: 1, vcpus: 1, public: true}

	var buf bytes.Buffer
	if err := runFlavorCreate(context.Background(), client, o, "m1.custom", f, &buf); err != nil {
		t.Fatalf("runFlavorCreate returned error: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("request method = %q, want POST", gotMethod)
	}
	if gotAPIVersion != "compute 2.1" {
		t.Errorf("OpenStack-API-Version = %q, want %q", gotAPIVersion, "compute 2.1")
	}

	flavorBody, ok := gotBody["flavor"].(map[string]any)
	if !ok {
		t.Fatalf("request body missing 'flavor' object: %#v", gotBody)
	}
	assertJSONNum(t, flavorBody, "ram", 512)
	assertJSONNum(t, flavorBody, "disk", 1)
	assertJSONNum(t, flavorBody, "vcpus", 1)
	if flavorBody["name"] != "m1.custom" {
		t.Errorf("body name = %v, want m1.custom", flavorBody["name"])
	}
	if pub, ok := flavorBody["os-flavor-access:is_public"].(bool); !ok || !pub {
		t.Errorf("body is_public = %v, want true", flavorBody["os-flavor-access:is_public"])
	}

	if !strings.Contains(buf.String(), "m1.custom") {
		t.Errorf("output missing created flavor name:\n%s", buf.String())
	}
}

func assertJSONNum(t *testing.T, m map[string]any, key string, want float64) {
	t.Helper()
	v, ok := m[key].(float64)
	if !ok {
		t.Errorf("body[%q] = %#v, want number %v", key, m[key], want)
		return
	}
	if v != want {
		t.Errorf("body[%q] = %v, want %v", key, v, want)
	}
}

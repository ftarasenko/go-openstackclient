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

const flavorGetBody = `{
  "flavor": {
    "id": "1",
    "name": "m1.tiny",
    "ram": 512,
    "disk": 1,
    "vcpus": 1,
    "OS-FLV-EXT-DATA:ephemeral": 0,
    "swap": "",
    "rxtx_factor": 1.0,
    "os-flavor-access:is_public": true,
    "description": "tiny flavor"
  }
}`

func TestRunFlavorShow_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	// resolveFlavorID always lists /flavors/detail first.
	fakeServer.Mux.HandleFunc("/flavors/detail", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(flavorListBody))
	})

	var gotMethod, gotAPIVersion, gotPath string
	fakeServer.Mux.HandleFunc("/flavors/1", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAPIVersion = r.Header.Get("OpenStack-API-Version")
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(flavorGetBody))
	})

	client := computeClient(fakeServer, "2.61")
	o := &output.Options{Format: output.FormatTable}

	var buf bytes.Buffer
	if err := runFlavorShow(context.Background(), client, o, "1", &buf); err != nil {
		t.Fatalf("runFlavorShow returned error: %v", err)
	}

	if gotMethod != http.MethodGet {
		t.Errorf("request method = %q, want GET", gotMethod)
	}
	if gotPath != "/flavors/1" {
		t.Errorf("request path = %q, want /flavors/1", gotPath)
	}
	if gotAPIVersion != "compute 2.61" {
		t.Errorf("OpenStack-API-Version = %q, want %q", gotAPIVersion, "compute 2.61")
	}

	out := buf.String()
	for _, want := range []string{"m1.tiny", "512", "tiny flavor", "Description"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s", want, out)
		}
	}
}

func TestRunFlavorDelete_RequestMethod(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/flavors/detail", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(flavorListBody))
	})

	var gotMethod, gotAPIVersion, gotPath string
	fakeServer.Mux.HandleFunc("/flavors/2", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAPIVersion = r.Header.Get("OpenStack-API-Version")
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		w.WriteHeader(http.StatusAccepted)
	})

	client := computeClient(fakeServer, "2.1")

	var buf bytes.Buffer
	if err := runFlavorDelete(context.Background(), client, []string{"2"}, &buf); err != nil {
		t.Fatalf("runFlavorDelete returned error: %v", err)
	}

	if gotMethod != http.MethodDelete {
		t.Errorf("request method = %q, want DELETE", gotMethod)
	}
	if gotPath != "/flavors/2" {
		t.Errorf("request path = %q, want /flavors/2", gotPath)
	}
	if gotAPIVersion != "compute 2.1" {
		t.Errorf("OpenStack-API-Version = %q, want %q", gotAPIVersion, "compute 2.1")
	}
}

// TestRunFlavorDelete_AggregatesFailures asserts that a bad ref in the middle
// of the batch does not stop the good refs around it from being deleted, and
// that the returned error names the failing ref.
func TestRunFlavorDelete_AggregatesFailures(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/flavors/detail", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(flavorListBody))
	})

	var deleted []string
	fakeServer.Mux.HandleFunc("/flavors/1", func(w http.ResponseWriter, _ *http.Request) {
		deleted = append(deleted, "1")
		w.WriteHeader(http.StatusAccepted)
	})
	fakeServer.Mux.HandleFunc("/flavors/2", func(w http.ResponseWriter, _ *http.Request) {
		deleted = append(deleted, "2")
		w.WriteHeader(http.StatusAccepted)
	})

	client := computeClient(fakeServer, "2.1")

	var buf bytes.Buffer
	// "9" does not resolve to any flavor; "1" and "2" flank it and must still
	// both be deleted.
	err := runFlavorDelete(context.Background(), client, []string{"1", "9", "2"}, &buf)
	if err == nil {
		t.Fatal("runFlavorDelete returned nil error; want a failure for the unresolvable ref")
	}
	if !strings.Contains(err.Error(), "9") {
		t.Errorf("error missing failed ref %q: %v", "9", err)
	}
	if len(deleted) != 2 || deleted[0] != "1" || deleted[1] != "2" {
		t.Errorf("deleted = %v, want both [1 2] attempted despite the failure between them", deleted)
	}
}

func TestRunFlavorSet_RequestBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/flavors/detail", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(flavorListBody))
	})

	var gotMethod, gotAPIVersion, gotPath string
	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/flavors/1/os-extra_specs", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAPIVersion = r.Header.Get("OpenStack-API-Version")
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"extra_specs": {"hw:cpu_policy": "dedicated"}}`))
	})

	client := computeClient(fakeServer, "2.1")

	var buf bytes.Buffer
	f := &flavorSetFlags{properties: []string{"hw:cpu_policy=dedicated"}}
	if err := runFlavorSet(context.Background(), client, "1", f, "", &buf); err != nil {
		t.Fatalf("runFlavorSet returned error: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("request method = %q, want POST", gotMethod)
	}
	if gotPath != "/flavors/1/os-extra_specs" {
		t.Errorf("request path = %q, want /flavors/1/os-extra_specs", gotPath)
	}
	if gotAPIVersion != "compute 2.1" {
		t.Errorf("OpenStack-API-Version = %q, want %q", gotAPIVersion, "compute 2.1")
	}
	specs, ok := gotBody["extra_specs"].(map[string]any)
	if !ok {
		t.Fatalf("request body missing 'extra_specs' object: %#v", gotBody)
	}
	if specs["hw:cpu_policy"] != "dedicated" {
		t.Errorf("extra_specs[hw:cpu_policy] = %v, want dedicated", specs["hw:cpu_policy"])
	}
}

func TestRunFlavorSet_NoPropertiesSkipsRequest(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	// No handlers registered: any HTTP call would fail the test.
	client := computeClient(fakeServer, "2.1")
	var buf bytes.Buffer
	if err := runFlavorSet(context.Background(), client, "1", &flavorSetFlags{}, "", &buf); err != nil {
		t.Fatalf("runFlavorSet with no props returned error: %v", err)
	}
}

func TestRunFlavorUnset_RequestMethod(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/flavors/detail", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(flavorListBody))
	})

	var gotMethod, gotAPIVersion, gotPath string
	fakeServer.Mux.HandleFunc("/flavors/1/os-extra_specs/hw:cpu_policy", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAPIVersion = r.Header.Get("OpenStack-API-Version")
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		w.WriteHeader(http.StatusOK)
	})

	client := computeClient(fakeServer, "2.1")

	var buf bytes.Buffer
	if err := runFlavorUnset(context.Background(), client, "1", []string{"hw:cpu_policy"}, &buf); err != nil {
		t.Fatalf("runFlavorUnset returned error: %v", err)
	}

	if gotMethod != http.MethodDelete {
		t.Errorf("request method = %q, want DELETE", gotMethod)
	}
	if gotPath != "/flavors/1/os-extra_specs/hw:cpu_policy" {
		t.Errorf("request path = %q, want /flavors/1/os-extra_specs/hw:cpu_policy", gotPath)
	}
	if gotAPIVersion != "compute 2.1" {
		t.Errorf("OpenStack-API-Version = %q, want %q", gotAPIVersion, "compute 2.1")
	}
}

func TestRunFlavorUnset_NoKeysSkipsRequest(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	client := computeClient(fakeServer, "2.1")
	var buf bytes.Buffer
	if err := runFlavorUnset(context.Background(), client, "1", nil, &buf); err != nil {
		t.Fatalf("runFlavorUnset with no keys returned error: %v", err)
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

// flavorPublicOnlyListBody is what nova returns for the default flavor listing
// (is_public defaults to true, even for an admin token): the private m1.small is
// absent. Paired with flavorListBody — served for is_public=None — it exercises
// resolveFlavor's retry across access types.
const flavorPublicOnlyListBody = `{
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
    }
  ]
}`

// TestRunFlavorSet_NoPropertyClearsBeforeSetting asserts the --no-property
// ordering: every existing extra spec is deleted (one DELETE per key, since nova
// has no bulk delete) before --property writes the replacements.
func TestRunFlavorSet_NoPropertyClearsBeforeSetting(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/flavors/detail", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(flavorListBody))
	})

	var calls []string
	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/flavors/1/os-extra_specs", func(w http.ResponseWriter, r *http.Request) {
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			calls = append(calls, "list")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"extra_specs": {"quota:disk_read_iops_sec": "1000", "hw:numa_nodes": "2"}}`))
		case http.MethodPost:
			calls = append(calls, "create")
			body, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(body, &gotBody); err != nil {
				t.Errorf("decoding request body: %v", err)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"extra_specs": {"hw:cpu_policy": "dedicated"}}`))
		default:
			t.Errorf("unexpected method %q on os-extra_specs", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	for _, key := range []string{"hw:numa_nodes", "quota:disk_read_iops_sec"} {
		fakeServer.Mux.HandleFunc("/flavors/1/os-extra_specs/"+key, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodDelete {
				t.Errorf("method on %s = %q, want DELETE", r.URL.Path, r.Method)
			}
			calls = append(calls, "delete "+key)
			w.WriteHeader(http.StatusOK)
		})
	}

	client := computeClient(fakeServer, "2.1")

	f := &flavorSetFlags{noProperty: true, properties: []string{"hw:cpu_policy=dedicated"}}
	var buf bytes.Buffer
	if err := runFlavorSet(context.Background(), client, "1", f, "", &buf); err != nil {
		t.Fatalf("runFlavorSet returned error: %v", err)
	}

	want := []string{"list", "delete hw:numa_nodes", "delete quota:disk_read_iops_sec", "create"}
	if len(calls) != len(want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("calls = %v, want %v", calls, want)
		}
	}
	specs, ok := gotBody["extra_specs"].(map[string]any)
	if !ok {
		t.Fatalf("create body missing 'extra_specs' object: %#v", gotBody)
	}
	if len(specs) != 1 || specs["hw:cpu_policy"] != "dedicated" {
		t.Errorf("extra_specs = %#v, want only hw:cpu_policy=dedicated", specs)
	}
}

// TestRunFlavorSet_NoPropertyAloneClearsAll covers "--no-property" without
// "--property": the specs are removed and nothing is written back.
func TestRunFlavorSet_NoPropertyAloneClearsAll(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/flavors/detail", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(flavorListBody))
	})
	fakeServer.Mux.HandleFunc("/flavors/1/os-extra_specs", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want only the GET that lists the specs", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"extra_specs": {"hw:numa_nodes": "2"}}`))
	})
	var deleted []string
	fakeServer.Mux.HandleFunc("/flavors/1/os-extra_specs/hw:numa_nodes", func(w http.ResponseWriter, _ *http.Request) {
		deleted = append(deleted, "hw:numa_nodes")
		w.WriteHeader(http.StatusOK)
	})

	client := computeClient(fakeServer, "2.1")

	var buf bytes.Buffer
	if err := runFlavorSet(context.Background(), client, "1", &flavorSetFlags{noProperty: true}, "", &buf); err != nil {
		t.Fatalf("runFlavorSet returned error: %v", err)
	}
	if len(deleted) != 1 || deleted[0] != "hw:numa_nodes" {
		t.Errorf("deleted = %v, want [hw:numa_nodes]", deleted)
	}
}

// TestRunFlavorSet_ProjectAccess asserts --project posts addTenantAccess for a
// private flavor, and that resolving that flavor falls back to the all-access
// listing after the default (public-only) view misses it.
func TestRunFlavorSet_ProjectAccess(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var listQueries []string
	fakeServer.Mux.HandleFunc("/flavors/detail", func(w http.ResponseWriter, r *http.Request) {
		isPublic := r.URL.Query().Get("is_public")
		listQueries = append(listQueries, isPublic)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if isPublic == "None" {
			_, _ = w.Write([]byte(flavorListBody))
			return
		}
		_, _ = w.Write([]byte(flavorPublicOnlyListBody))
	})

	var gotMethod, gotPath, gotAPIVersion string
	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/flavors/2/action", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAPIVersion = r.Header.Get("OpenStack-API-Version")
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"flavor_access": [{"flavor_id": "2", "tenant_id": "0f1e2d3c4b5a69788796a5b4c3d2e1f0"}]}`))
	})

	client := computeClient(fakeServer, "2.1")

	f := &flavorSetFlags{project: "engineering"}
	var buf bytes.Buffer
	if err := runFlavorSet(context.Background(), client, "m1.small", f, "0f1e2d3c4b5a69788796a5b4c3d2e1f0", &buf); err != nil {
		t.Fatalf("runFlavorSet returned error: %v", err)
	}

	if len(listQueries) != 2 || listQueries[0] != "" || listQueries[1] != "None" {
		t.Errorf("is_public queries = %v, want the default view then [None]", listQueries)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("request method = %q, want POST", gotMethod)
	}
	if gotPath != "/flavors/2/action" {
		t.Errorf("request path = %q, want /flavors/2/action", gotPath)
	}
	if gotAPIVersion != "compute 2.1" {
		t.Errorf("OpenStack-API-Version = %q, want %q", gotAPIVersion, "compute 2.1")
	}
	access, ok := gotBody["addTenantAccess"].(map[string]any)
	if !ok {
		t.Fatalf("request body missing 'addTenantAccess' object: %#v", gotBody)
	}
	if access["tenant"] != "0f1e2d3c4b5a69788796a5b4c3d2e1f0" {
		t.Errorf("addTenantAccess.tenant = %v, want the resolved project ID", access["tenant"])
	}
}

// TestRunFlavorSet_ProjectAccessRejectsPublicFlavor asserts the pre-flight check:
// nova has no access list for a public flavor, and the error must arrive before
// any request is issued so a mixed "flavor set" cannot half-apply.
func TestRunFlavorSet_ProjectAccessRejectsPublicFlavor(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/flavors/detail", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(flavorListBody))
	})
	// No /flavors/1/action or os-extra_specs handler: either would 404 and fail.

	client := computeClient(fakeServer, "2.1")

	f := &flavorSetFlags{project: "engineering", properties: []string{"hw:cpu_policy=dedicated"}}
	var buf bytes.Buffer
	err := runFlavorSet(context.Background(), client, "m1.tiny", f, "0f1e2d3c4b5a69788796a5b4c3d2e1f0", &buf)
	if err == nil {
		t.Fatal("runFlavorSet returned nil error; want a rejection for a public flavor")
	}
	if !strings.Contains(err.Error(), "public") {
		t.Errorf("error = %v, want it to name the flavor's public visibility", err)
	}
}

// TestRunFlavorSet_Description asserts --description PUTs the flavor itself.
func TestRunFlavorSet_Description(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/flavors/detail", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(flavorListBody))
	})

	var gotMethod, gotPath, gotAPIVersion string
	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/flavors/1", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAPIVersion = r.Header.Get("OpenStack-API-Version")
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"flavor": {"id": "1", "name": "m1.tiny", "description": "smallest flavor"}}`))
	})

	client := computeClient(fakeServer, flavorDescriptionMicroversion)

	f := &flavorSetFlags{description: "smallest flavor", descriptionSet: true}
	var buf bytes.Buffer
	if err := runFlavorSet(context.Background(), client, "1", f, "", &buf); err != nil {
		t.Fatalf("runFlavorSet returned error: %v", err)
	}

	if gotMethod != http.MethodPut {
		t.Errorf("request method = %q, want PUT", gotMethod)
	}
	if gotPath != "/flavors/1" {
		t.Errorf("request path = %q, want /flavors/1", gotPath)
	}
	if gotAPIVersion != "compute "+flavorDescriptionMicroversion {
		t.Errorf("OpenStack-API-Version = %q, want %q", gotAPIVersion, "compute "+flavorDescriptionMicroversion)
	}
	flavorBody, ok := gotBody["flavor"].(map[string]any)
	if !ok {
		t.Fatalf("request body missing 'flavor' object: %#v", gotBody)
	}
	if flavorBody["description"] != "smallest flavor" {
		t.Errorf("flavor.description = %v, want %q", flavorBody["description"], "smallest flavor")
	}
}

// TestRunFlavorSet_DescriptionEmptyClearsIt asserts an explicit empty
// --description sends a null rather than an empty object nova would reject.
func TestRunFlavorSet_DescriptionEmptyClearsIt(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/flavors/detail", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(flavorListBody))
	})

	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/flavors/1", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"flavor": {"id": "1", "name": "m1.tiny", "description": null}}`))
	})

	client := computeClient(fakeServer, "latest")

	f := &flavorSetFlags{description: "", descriptionSet: true}
	var buf bytes.Buffer
	if err := runFlavorSet(context.Background(), client, "1", f, "", &buf); err != nil {
		t.Fatalf("runFlavorSet returned error: %v", err)
	}

	flavorBody, ok := gotBody["flavor"].(map[string]any)
	if !ok {
		t.Fatalf("request body missing 'flavor' object: %#v", gotBody)
	}
	value, present := flavorBody["description"]
	if !present {
		t.Fatalf("flavor body has no 'description' key: %#v", flavorBody)
	}
	if value != nil {
		t.Errorf("flavor.description = %#v, want null", value)
	}
}

// TestRunFlavorSet_DescriptionRequiresMicroversion asserts a client pinned below
// 2.55 is told which flag to raise instead of being sent into nova's 400.
func TestRunFlavorSet_DescriptionRequiresMicroversion(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	// No handlers registered: the check must fire before any request.
	client := computeClient(fakeServer, "2.53")

	f := &flavorSetFlags{description: "smallest flavor", descriptionSet: true}
	var buf bytes.Buffer
	err := runFlavorSet(context.Background(), client, "1", f, "", &buf)
	if err == nil {
		t.Fatal("runFlavorSet returned nil error; want a microversion requirement")
	}
	for _, want := range []string{flavorDescriptionMicroversion, "--os-compute-api-version"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %v does not mention %q", err, want)
		}
	}
}

// TestResolveFlavor_UnknownRefReportsMiss asserts both access views are consulted
// before a reference is declared unresolvable.
func TestResolveFlavor_UnknownRefReportsMiss(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var listQueries []string
	fakeServer.Mux.HandleFunc("/flavors/detail", func(w http.ResponseWriter, r *http.Request) {
		listQueries = append(listQueries, r.URL.Query().Get("is_public"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(flavorPublicOnlyListBody))
	})

	client := computeClient(fakeServer, "2.1")

	_, err := resolveFlavor(context.Background(), client, "m1.nonexistent")
	if err == nil {
		t.Fatal("resolveFlavor returned nil error for an unknown flavor")
	}
	if !strings.Contains(err.Error(), "m1.nonexistent") {
		t.Errorf("error = %v, want it to name the unresolved ref", err)
	}
	if len(listQueries) != 2 || listQueries[0] != "" || listQueries[1] != "None" {
		t.Errorf("is_public queries = %v, want the default view then [None]", listQueries)
	}
}

package placement

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	th "github.com/gophercloud/gophercloud/v2/testhelper"
	fakeclient "github.com/gophercloud/gophercloud/v2/testhelper/client"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// placementClient returns a service client wired to the mock server with the
// placement service type + microversion, mirroring auth.Client.Placement.
func placementClient(fakeServer th.FakeServer, microversion string) *gophercloud.ServiceClient {
	sc := fakeclient.ServiceClient(fakeServer)
	sc.Type = "placement"
	sc.Microversion = microversion
	return sc
}

const providerListBody = `{
  "resource_providers": [
    {
      "uuid": "11111111-1111-1111-1111-111111111111",
      "name": "rp-a",
      "generation": 3,
      "root_provider_uuid": "11111111-1111-1111-1111-111111111111",
      "parent_provider_uuid": ""
    },
    {
      "uuid": "22222222-2222-2222-2222-222222222222",
      "name": "rp-b",
      "generation": 0,
      "root_provider_uuid": "11111111-1111-1111-1111-111111111111",
      "parent_provider_uuid": "11111111-1111-1111-1111-111111111111"
    }
  ]
}`

func TestRunProviderList_RequestAndTableOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotAPIVersion, gotName string
	fakeServer.Mux.HandleFunc("/resource_providers", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotAPIVersion = r.Header.Get("OpenStack-API-Version")
		gotName = r.URL.Query().Get("name")
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(providerListBody))
	})

	client := placementClient(fakeServer, "1.0")
	o := &output.Options{Format: output.FormatTable}

	var buf bytes.Buffer
	f := &providerListFlags{name: "rp-a"}
	if err := runProviderList(context.Background(), client, o, f, &buf); err != nil {
		t.Fatalf("runProviderList returned error: %v", err)
	}

	if gotMethod != http.MethodGet {
		t.Errorf("request method = %q, want GET", gotMethod)
	}
	if gotAPIVersion != "placement 1.0" {
		t.Errorf("OpenStack-API-Version = %q, want %q", gotAPIVersion, "placement 1.0")
	}
	if gotName != "rp-a" {
		t.Errorf("name query = %q, want rp-a", gotName)
	}

	out := buf.String()
	for _, want := range []string{
		"uuid", "name", "generation",
		"rp-a", "rp-b",
		"11111111-1111-1111-1111-111111111111",
		"22222222-2222-2222-2222-222222222222",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("table output missing %q\n---\n%s", want, out)
		}
	}
}

const traitListBody = `{
  "traits": [
    "COMPUTE_NET_ATTACH_INTERFACE",
    "HW_CPU_X86_AVX",
    "HW_CPU_X86_SSE"
  ]
}`

func TestRunTraitList_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotAPIVersion string
	fakeServer.Mux.HandleFunc("/traits", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotAPIVersion = r.Header.Get("OpenStack-API-Version")
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(traitListBody))
	})

	client := placementClient(fakeServer, "1.0")
	o := &output.Options{Format: output.FormatValue}

	var buf bytes.Buffer
	if err := runTraitList(context.Background(), client, o, &traitListFlags{}, false, &buf); err != nil {
		t.Fatalf("runTraitList returned error: %v", err)
	}

	if gotMethod != http.MethodGet {
		t.Errorf("request method = %q, want GET", gotMethod)
	}
	if gotAPIVersion != "placement 1.0" {
		t.Errorf("OpenStack-API-Version = %q, want %q", gotAPIVersion, "placement 1.0")
	}

	out := buf.String()
	for _, want := range []string{"COMPUTE_NET_ATTACH_INTERFACE", "HW_CPU_X86_AVX", "HW_CPU_X86_SSE"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s", want, out)
		}
	}
	// value format: no header row.
	if strings.Contains(out, "name\n") && strings.HasPrefix(out, "name") {
		t.Errorf("value format must not include header:\n%s", out)
	}
}

const providerTraitsBody = `{
  "resource_provider_generation": 1,
  "traits": ["CUSTOM_GOLD", "HW_CPU_X86_AVX"]
}`

func TestRunProviderTraitList_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	id := "11111111-1111-1111-1111-111111111111"
	var gotMethod, gotAPIVersion string
	fakeServer.Mux.HandleFunc("/resource_providers/"+id+"/traits", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotAPIVersion = r.Header.Get("OpenStack-API-Version")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(providerTraitsBody))
	})

	client := placementClient(fakeServer, "1.0")
	o := &output.Options{Format: output.FormatTable}

	var buf bytes.Buffer
	if err := runProviderTraitList(context.Background(), client, o, id, &buf); err != nil {
		t.Fatalf("runProviderTraitList returned error: %v", err)
	}

	if gotMethod != http.MethodGet {
		t.Errorf("request method = %q, want GET", gotMethod)
	}
	if gotAPIVersion != "placement 1.0" {
		t.Errorf("OpenStack-API-Version = %q, want %q", gotAPIVersion, "placement 1.0")
	}
	out := buf.String()
	for _, want := range []string{"CUSTOM_GOLD", "HW_CPU_X86_AVX"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s", want, out)
		}
	}
}

const providerShowBody = `{
  "uuid": "11111111-1111-1111-1111-111111111111",
  "name": "rp-a",
  "generation": 3,
  "root_provider_uuid": "11111111-1111-1111-1111-111111111111",
  "parent_provider_uuid": ""
}`

func TestRunProviderShow_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	id := "11111111-1111-1111-1111-111111111111"
	var gotMethod, gotAPIVersion, gotPath string
	fakeServer.Mux.HandleFunc("/resource_providers/"+id, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotAPIVersion = r.Header.Get("OpenStack-API-Version")
		gotPath = r.URL.Path
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(providerShowBody))
	})

	client := placementClient(fakeServer, "1.0")
	o := &output.Options{Format: output.FormatTable}

	var buf bytes.Buffer
	if err := runProviderShow(context.Background(), client, o, id, &providerShowFlags{}, &buf); err != nil {
		t.Fatalf("runProviderShow returned error: %v", err)
	}

	if gotMethod != http.MethodGet {
		t.Errorf("request method = %q, want GET", gotMethod)
	}
	if gotPath != "/resource_providers/"+id {
		t.Errorf("request path = %q, want %q", gotPath, "/resource_providers/"+id)
	}
	if gotAPIVersion != "placement 1.0" {
		t.Errorf("OpenStack-API-Version = %q, want %q", gotAPIVersion, "placement 1.0")
	}

	out := buf.String()
	for _, want := range []string{
		"uuid", "name", "generation", "root_provider_uuid", "parent_provider_uuid",
		"rp-a", "11111111-1111-1111-1111-111111111111", "3",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s", want, out)
		}
	}
}

func TestRunProviderDelete_Request(t *testing.T) {
	tests := []struct {
		name string
		ids  []string
	}{
		{name: "single", ids: []string{"11111111-1111-1111-1111-111111111111"}},
		{
			name: "multiple",
			ids: []string{
				"11111111-1111-1111-1111-111111111111",
				"22222222-2222-2222-2222-222222222222",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()

			gotMethod := map[string]string{}
			gotAPIVersion := map[string]string{}
			for _, id := range tc.ids {
				id := id
				fakeServer.Mux.HandleFunc("/resource_providers/"+id, func(w http.ResponseWriter, r *http.Request) {
					gotMethod[id] = r.Method
					gotAPIVersion[id] = r.Header.Get("OpenStack-API-Version")
					th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
					w.WriteHeader(http.StatusNoContent)
				})
			}

			client := placementClient(fakeServer, "1.0")

			var buf bytes.Buffer
			if err := runProviderDelete(context.Background(), client, tc.ids, &buf); err != nil {
				t.Fatalf("runProviderDelete returned error: %v", err)
			}

			out := buf.String()
			for _, id := range tc.ids {
				if gotMethod[id] != http.MethodDelete {
					t.Errorf("request method for %s = %q, want DELETE", id, gotMethod[id])
				}
				if gotAPIVersion[id] != "placement 1.0" {
					t.Errorf("OpenStack-API-Version for %s = %q, want %q", id, gotAPIVersion[id], "placement 1.0")
				}
				if !strings.Contains(out, id) {
					t.Errorf("output missing deleted uuid %s:\n%s", id, out)
				}
			}
		})
	}
}

func TestRunProviderAllocationDelete_Request(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	consumer := "cccccccc-cccc-cccc-cccc-cccccccccccc"
	var gotMethod string
	fakeServer.Mux.HandleFunc("/allocations/"+consumer, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		w.WriteHeader(http.StatusNoContent)
	})

	client := placementClient(fakeServer, "1.0")

	var buf bytes.Buffer
	if err := runProviderAllocationDelete(context.Background(), client, []string{consumer}, &buf); err != nil {
		t.Fatalf("runProviderAllocationDelete returned error: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("request method = %q, want DELETE", gotMethod)
	}
	if !strings.Contains(buf.String(), consumer) {
		t.Errorf("output missing consumer uuid:\n%s", buf.String())
	}
}

// TestRunProviderDelete_CollectsFailures verifies the provider delete seam
// attempts every UUID and joins failures instead of aborting on the first.
func TestRunProviderDelete_CollectsFailures(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	bad := "11111111-1111-1111-1111-111111111111"
	good := "22222222-2222-2222-2222-222222222222"
	var deleted []string
	fakeServer.Mux.HandleFunc("/resource_providers/"+bad, func(w http.ResponseWriter, _ *http.Request) {
		deleted = append(deleted, bad)
		w.WriteHeader(http.StatusNotFound)
	})
	fakeServer.Mux.HandleFunc("/resource_providers/"+good, func(w http.ResponseWriter, _ *http.Request) {
		deleted = append(deleted, good)
		w.WriteHeader(http.StatusNoContent)
	})

	client := placementClient(fakeServer, "1.0")

	var buf bytes.Buffer
	err := runProviderDelete(context.Background(), client, []string{bad, good}, &buf)
	if err == nil {
		t.Fatalf("runProviderDelete = nil, want error for %s", bad)
	}
	if len(deleted) != 2 {
		t.Errorf("attempted deletes = %v, want both ids attempted", deleted)
	}
	if !strings.Contains(err.Error(), bad) {
		t.Errorf("error missing failed id %s: %v", bad, err)
	}
	if !strings.Contains(buf.String(), good) {
		t.Errorf("output missing successfully deleted id %s:\n%s", good, buf.String())
	}
}

// TestRunProviderAllocationDelete_CollectsFailures verifies the allocation
// delete seam attempts every consumer and joins failures.
func TestRunProviderAllocationDelete_CollectsFailures(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	bad := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	good := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	var deleted []string
	fakeServer.Mux.HandleFunc("/allocations/"+bad, func(w http.ResponseWriter, _ *http.Request) {
		deleted = append(deleted, bad)
		w.WriteHeader(http.StatusNotFound)
	})
	fakeServer.Mux.HandleFunc("/allocations/"+good, func(w http.ResponseWriter, _ *http.Request) {
		deleted = append(deleted, good)
		w.WriteHeader(http.StatusNoContent)
	})

	client := placementClient(fakeServer, "1.0")

	var buf bytes.Buffer
	err := runProviderAllocationDelete(context.Background(), client, []string{bad, good}, &buf)
	if err == nil {
		t.Fatalf("runProviderAllocationDelete = nil, want error for %s", bad)
	}
	if len(deleted) != 2 {
		t.Errorf("attempted deletes = %v, want both consumers attempted", deleted)
	}
	if !strings.Contains(err.Error(), bad) {
		t.Errorf("error missing failed consumer %s: %v", bad, err)
	}
	if !strings.Contains(buf.String(), good) {
		t.Errorf("output missing successfully deleted consumer %s:\n%s", good, buf.String())
	}
}

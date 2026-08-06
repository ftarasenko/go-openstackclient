package placement

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"
	fakeclient "github.com/gophercloud/gophercloud/v2/testhelper/client"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

const inventoriesBody = `{
  "resource_provider_generation": 7,
  "inventories": {
    "VCPU":       {"allocation_ratio": 16.0, "max_unit": 64, "min_unit": 1, "reserved": 0, "step_size": 1, "total": 64},
    "MEMORY_MB":  {"allocation_ratio": 1.5,  "max_unit": 262144, "min_unit": 1, "reserved": 8192, "step_size": 1, "total": 262144},
    "DISK_GB":    {"allocation_ratio": 1.0,  "max_unit": 1800, "min_unit": 1, "reserved": 20, "step_size": 1, "total": 1800}
  }
}`

func TestRunProviderInventoryList_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotAPIVersion string
	fakeServer.Mux.HandleFunc("/resource_providers/rp1/inventories", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotAPIVersion = r.Header.Get("OpenStack-API-Version")
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(inventoriesBody))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runProviderInventoryList(context.Background(), placementClient(fakeServer, "latest"), o, "rp1", &buf)
	if err != nil {
		t.Fatalf("runProviderInventoryList error: %v", err)
	}

	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if gotAPIVersion != "placement latest" {
		t.Errorf("OpenStack-API-Version = %q, want \"placement latest\"", gotAPIVersion)
	}

	out := buf.String()
	for _, want := range []string{
		"resource_class", "total", "reserved", "allocation_ratio",
		"VCPU", "64", "MEMORY_MB", "262144", "8192", "DISK_GB", "1800",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("inventory list output missing %q\n---\n%s", want, out)
		}
	}

	// Rows come from a JSON object, so they must be sorted by class for stable
	// output across invocations.
	disk := strings.Index(out, "DISK_GB")
	mem := strings.Index(out, "MEMORY_MB")
	vcpu := strings.Index(out, "VCPU")
	if disk >= mem || mem >= vcpu {
		t.Errorf("inventory rows are not sorted by resource class (DISK_GB, MEMORY_MB, VCPU):\n%s", out)
	}
}

func TestRunProviderInventoryShow_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotPath string
	fakeServer.Mux.HandleFunc("/resource_providers/rp1/inventories/VCPU", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		th.TestMethod(t, r, http.MethodGet)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
          "resource_provider_generation": 7,
          "allocation_ratio": 16.0, "max_unit": 64, "min_unit": 1,
          "reserved": 0, "step_size": 1, "total": 64
        }`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runProviderInventoryShow(context.Background(), placementClient(fakeServer, "latest"), o, "rp1", "VCPU", &buf)
	if err != nil {
		t.Fatalf("runProviderInventoryShow error: %v", err)
	}
	if gotPath != "/resource_providers/rp1/inventories/VCPU" {
		t.Errorf("path = %q, want /resource_providers/rp1/inventories/VCPU", gotPath)
	}
	for _, want := range []string{"total", "64", "allocation_ratio", "16", "resource_provider_generation", "7"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("inventory show output missing %q\n---\n%s", want, buf.String())
		}
	}
}

func TestRunProviderUsageShow_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotPath string
	fakeServer.Mux.HandleFunc("/resource_providers/rp1/usages", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		th.TestMethod(t, r, http.MethodGet)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
          "resource_provider_generation": 7,
          "usages": {"VCPU": 12, "MEMORY_MB": 24576, "DISK_GB": 200}
        }`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runProviderUsageShow(context.Background(), placementClient(fakeServer, "latest"), o, "rp1", &buf); err != nil {
		t.Fatalf("runProviderUsageShow error: %v", err)
	}
	if gotPath != "/resource_providers/rp1/usages" {
		t.Errorf("path = %q, want /resource_providers/rp1/usages", gotPath)
	}
	for _, want := range []string{"resource_class", "usage", "VCPU", "12", "MEMORY_MB", "24576", "DISK_GB", "200"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("usage show output missing %q\n---\n%s", want, buf.String())
		}
	}
}

func TestRunProviderAggregateList_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotPath string
	fakeServer.Mux.HandleFunc("/resource_providers/rp1/aggregates", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		th.TestMethod(t, r, http.MethodGet)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
          "resource_provider_generation": 7,
          "aggregates": ["42d4d1d7-1234-4b9e-8f4a-000000000001", "42d4d1d7-1234-4b9e-8f4a-000000000002"]
        }`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runProviderAggregateList(context.Background(), placementClient(fakeServer, "latest"), o, "rp1", &buf); err != nil {
		t.Fatalf("runProviderAggregateList error: %v", err)
	}
	if gotPath != "/resource_providers/rp1/aggregates" {
		t.Errorf("path = %q, want /resource_providers/rp1/aggregates", gotPath)
	}
	for _, want := range []string{"uuid", "000000000001", "000000000002"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("aggregate list output missing %q\n---\n%s", want, buf.String())
		}
	}
}

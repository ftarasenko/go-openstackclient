package volume

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

// Two pools that do not agree on their capability keys, which is the normal
// case: cinder returns whatever each driver reported. The replicated backend
// also carries its own raw figure alongside cinder's normalised one — the pair
// --long exists to expose.
const poolListBody = `{
  "pools": [
    {
      "name": "storage-1@vitastor#vitastor-ssd",
      "capabilities": {
        "backend_state": "up",
        "volume_backend_name": "vitastor",
        "total_capacity_gb": 40960,
        "free_capacity_gb": 8192,
        "allocated_capacity_gb": 30000,
        "vitastor_raw_total_gb": 40960,
        "vitastor_raw_free_gb": 24576,
        "replication_targets": []
      }
    },
    {
      "name": "storage-2@lvm#lvm-1",
      "capabilities": {
        "backend_state": "up",
        "volume_backend_name": "lvm",
        "total_capacity_gb": "infinite",
        "free_capacity_gb": "unknown",
        "thin_provisioning_support": true
      }
    }
  ]
}`

func TestRunBackendPoolList_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotQuery, gotMethod string
	fakeServer.Mux.HandleFunc("/scheduler-stats/get_pools", func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotQuery = r.Method, r.URL.RawQuery
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(poolListBody))
	})

	o := &output.Options{Format: output.FormatCSV}
	var buf bytes.Buffer
	if err := runBackendPoolList(context.Background(), volumeClient(fakeServer, "3.70"), o, false, &buf); err != nil {
		t.Fatalf("runBackendPoolList: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	// detail=True is what makes cinder return the capabilities at all.
	if gotQuery != "detail=True" {
		t.Errorf("query = %q, want detail=True", gotQuery)
	}
	out := buf.String()
	for _, want := range []string{
		"Name", "Backend State", "Total Capacity GB", "Free Capacity GB", "Allocated Capacity GB",
		"storage-1@vitastor#vitastor-ssd", "storage-2@lvm#lvm-1", "40960", "8192",
		// Cinder reports a capacity as "infinite"/"unknown" for some drivers;
		// those strings must survive rather than becoming a number.
		"infinite", "unknown",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("pool list output missing %q\n---\n%s", want, out)
		}
	}
	// The driver's own keys are --long material, not default columns.
	for _, absent := range []string{"Vitastor Raw Free GB", "Thin Provisioning Support"} {
		if strings.Contains(out, absent) {
			t.Errorf("default pool list unexpectedly has %q\n---\n%s", absent, out)
		}
	}
}

// --long adds every capability any pool reported, unioned across pools and
// ASCII-sorted. That is where a replicated backend's raw figure sits next to
// cinder's normalised total and free, which is the only way to see that the two
// were computed differently.
func TestRunBackendPoolList_LongUnionsDriverKeys(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/scheduler-stats/get_pools", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(poolListBody))
	})

	o := &output.Options{Format: output.FormatCSV}
	var buf bytes.Buffer
	if err := runBackendPoolList(context.Background(), volumeClient(fakeServer, "3.70"), o, true, &buf); err != nil {
		t.Fatalf("runBackendPoolList: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"Vitastor Raw Total GB", "Vitastor Raw Free GB", "Thin Provisioning Support",
		"24576",
		// A composite value has to fit one cell, so it is rendered as JSON.
		"[]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("pool list --long output missing %q\n---\n%s", want, out)
		}
	}
	header := strings.SplitN(out, "\n", 2)[0]
	if i, j := strings.Index(header, "Thin Provisioning Support"), strings.Index(header, "Vitastor Raw Free GB"); i > j {
		t.Errorf("extra capability columns are not sorted: %q", header)
	}
}

func TestRunBackendCapabilityShow(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/capabilities/storage-1@vitastor", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
		  "namespace": "OS::Storage::Capabilities::storage-1@vitastor",
		  "vendor_name": "Example",
		  "volume_backend_name": "vitastor",
		  "driver_version": "1.0",
		  "storage_protocol": "vitastor",
		  "replication_targets": [],
		  "properties": {"compression": {"type": "boolean"}}
		}`))
	})

	o := &output.Options{Format: output.FormatCSV}
	var buf bytes.Buffer
	if err := runBackendCapabilityShow(context.Background(), volumeClient(fakeServer, "3.70"), o,
		"storage-1@vitastor", &buf); err != nil {
		t.Fatalf("runBackendCapabilityShow: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"volume_backend_name", "vitastor", "storage_protocol", "compression"} {
		if !strings.Contains(out, want) {
			t.Errorf("capability show output missing %q\n---\n%s", want, out)
		}
	}
}

// A cloud where the caller is not an admin answers both endpoints with 403, and
// the error has to name the command's subject rather than leak a bare HTTP code.
func TestRunBackendPoolList_ErrorNamesTheOperation(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/scheduler-stats/get_pools", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	fakeServer.Mux.HandleFunc("/capabilities/storage-1", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})

	o := &output.Options{Format: output.FormatCSV}
	var buf bytes.Buffer
	err := runBackendPoolList(context.Background(), volumeClient(fakeServer, "3.70"), o, false, &buf)
	if err == nil || !strings.Contains(err.Error(), "listing storage pools") {
		t.Errorf("err = %v, want it to name the listing", err)
	}
	err = runBackendCapabilityShow(context.Background(), volumeClient(fakeServer, "3.70"), o, "storage-1", &buf)
	if err == nil || !strings.Contains(err.Error(), `capabilities of backend "storage-1"`) {
		t.Errorf("err = %v, want it to name the backend", err)
	}
}

func TestCapabilityHeader(t *testing.T) {
	cases := map[string]string{
		"total_capacity_gb":           "Total Capacity GB",
		"backend_state":               "Backend State",
		"qos_support":                 "QoS Support",
		"max_over_subscription_ratio": "Max Over Subscription Ratio",
		"vg":                          "VG",
	}
	for in, want := range cases {
		if got := capabilityHeader(in); got != want {
			t.Errorf("capabilityHeader(%q) = %q, want %q", in, got, want)
		}
	}
}

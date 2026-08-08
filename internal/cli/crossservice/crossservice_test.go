package crossservice

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	th "github.com/gophercloud/gophercloud/v2/testhelper"
	fakeclient "github.com/gophercloud/gophercloud/v2/testhelper/client"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

func serviceClient(fakeServer th.FakeServer, typ, microversion string) *gophercloud.ServiceClient {
	sc := fakeclient.ServiceClient(fakeServer)
	sc.Type = typ
	sc.Microversion = microversion
	return sc
}

func TestNetworkAvailabilityZones_ReportsNeutronsOwnResource(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotPath string
	fakeServer.Mux.HandleFunc("/availability_zones", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		// Unlike nova and cinder, neutron reports the resource kind itself —
		// the same zone name can appear once for networks and once for routers.
		_, _ = w.Write([]byte(`{"availability_zones": [
		  {"name": "az-1", "resource": "network", "state": "available"},
		  {"name": "az-1", "resource": "router",  "state": "unavailable"}
		]}`))
	})

	zones, err := networkAvailabilityZones(context.Background(), serviceClient(fakeServer, "network", ""))
	if err != nil {
		t.Fatalf("networkAvailabilityZones returned error: %v", err)
	}
	th.AssertEquals(t, "/availability_zones", gotPath)
	th.AssertEquals(t, 2, len(zones))
	th.AssertEquals(t, "network", zones[0].resource)
	th.AssertEquals(t, "router", zones[1].resource)
	th.AssertEquals(t, "unavailable", zones[1].state)
}

func TestComputeAvailabilityZones_LongSelectsTheDetailEndpoint(t *testing.T) {
	for _, tc := range []struct {
		long bool
		path string
	}{
		{false, "/os-availability-zone"},
		{true, "/os-availability-zone/detail"},
	} {
		fakeServer := th.SetupHTTP()

		var gotPath string
		handler := func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"availabilityZoneInfo": [
			  {"zoneName": "nova", "zoneState": {"available": true}, "hosts": null}
			]}`))
		}
		fakeServer.Mux.HandleFunc("/os-availability-zone", handler)
		fakeServer.Mux.HandleFunc("/os-availability-zone/detail", handler)

		zones, err := computeAvailabilityZones(context.Background(), serviceClient(fakeServer, "compute", "latest"), tc.long)
		if err != nil {
			t.Fatalf("computeAvailabilityZones(long=%v) returned error: %v", tc.long, err)
		}
		// The detail endpoint is admin-only, so --long must not be the default.
		th.AssertEquals(t, tc.path, gotPath)
		th.AssertEquals(t, 1, len(zones))
		th.AssertEquals(t, "available", zones[0].state)
		th.AssertEquals(t, "compute", zones[0].resource)
		fakeServer.Teardown()
	}
}

func TestZoneState(t *testing.T) {
	th.AssertEquals(t, "available", zoneState(true))
	th.AssertEquals(t, "not available", zoneState(false))
}

func TestUsageWindow_DefaultsAndValidation(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	start, end, err := (&usageFlags{}).window(now)
	if err != nil {
		t.Fatalf("window returned error: %v", err)
	}
	// Upstream defaults to four weeks back and *tomorrow*, so today is fully
	// covered rather than truncated at the current instant.
	th.AssertEquals(t, "2026-02-01", start.Format(usageDateLayout))
	th.AssertEquals(t, "2026-03-02", end.Format(usageDateLayout))

	start, end, err = (&usageFlags{start: "2026-01-01", end: "2026-01-31"}).window(now)
	if err != nil {
		t.Fatalf("window returned error: %v", err)
	}
	th.AssertEquals(t, "2026-01-01", start.Format(usageDateLayout))
	th.AssertEquals(t, "2026-01-31", end.Format(usageDateLayout))

	// A reversed window would silently return nothing from nova, so it is
	// rejected here instead.
	if _, _, err := (&usageFlags{start: "2026-02-01", end: "2026-01-01"}).window(now); err == nil {
		t.Error("expected an end before start to be rejected")
	}
	if _, _, err := (&usageFlags{start: "yesterday"}).window(now); err == nil {
		t.Error("expected an unparseable date to be rejected")
	}
}

func TestParseUsageDate_AcceptsBothForms(t *testing.T) {
	if _, err := parseUsageDate("2026-01-20"); err != nil {
		t.Errorf("plain date rejected: %v", err)
	}
	// An RFC 3339 timestamp allows a window narrower than a day, which the
	// documented date form cannot express.
	if _, err := parseUsageDate("2026-01-20T06:30:00Z"); err != nil {
		t.Errorf("RFC 3339 timestamp rejected: %v", err)
	}
}

func TestRunUsageList_QueryAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotQuery string
	fakeServer.Mux.HandleFunc("/os-simple-tenant-usage", func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tenant_usages": [
		  {"tenant_id": "proj-1", "total_hours": 10.5, "total_vcpus_usage": 21.0,
		   "total_memory_mb_usage": 43008.0, "total_local_gb_usage": 210.0,
		   "server_usages": [{"instance_id": "s1"}, {"instance_id": "s2"}]}
		]}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	f := &usageFlags{start: "2026-01-01", end: "2026-01-31"}
	client := serviceClient(fakeServer, "compute", "latest")
	if err := runUsageList(context.Background(), client, o, f, time.Now(), &out); err != nil {
		t.Fatalf("runUsageList returned error: %v", err)
	}

	for _, want := range []string{"start=2026-01-01", "end=2026-01-31"} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("query %q missing %q", gotQuery, want)
		}
	}
	// The server count comes from the length of server_usages; nova does not
	// report it as a field.
	if !strings.Contains(out.String(), "proj-1\t2\t") {
		t.Errorf("unexpected output: %q", out.String())
	}
}

func TestRunUsageShow_SingleProject(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/os-simple-tenant-usage/proj-1", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tenant_usage": {"tenant_id": "proj-1", "total_hours": 10.5,
		  "total_vcpus_usage": 21.0, "total_memory_mb_usage": 43008.0, "total_local_gb_usage": 210.0,
		  "start": "2026-01-01T00:00:00.000000", "stop": "2026-01-31T00:00:00.000000",
		  "server_usages": [{"instance_id": "s1"}]}}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	client := serviceClient(fakeServer, "compute", "latest")
	err := runUsageShow(context.Background(), client, o, "proj-1", &usageFlags{}, time.Now(), &out)
	if err != nil {
		t.Fatalf("runUsageShow returned error: %v", err)
	}
	for _, want := range []string{"proj-1", "21", "2026-01-01"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestAppendRateLimits_ReadsTheRateArray(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/limits", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"limits": {"absolute": {}, "rate": [
		  {"uri": "*", "regex": ".*", "limit": [
		    {"verb": "POST", "value": 10, "remaining": 2, "unit": "MINUTE", "next-available": "2026-01-01T00:00:00Z"}
		  ]}
		]}}`))
	})

	t1 := output.Table{Columns: []string{"Service", "Verb", "URI", "Regex", "Limit", "Remaining", "Unit", "Next Available"}}
	client := serviceClient(fakeServer, "volume", "latest")
	if err := appendRateLimits(context.Background(), client, "volume", "", &t1); err != nil {
		t.Fatalf("appendRateLimits returned error: %v", err)
	}
	th.AssertEquals(t, 1, len(t1.Rows))
	th.AssertEquals(t, "POST", t1.Rows[0][1])
	th.AssertEquals(t, 10, t1.Rows[0][4])
}

func TestAppendRateLimits_EmptyRateIsNotAnError(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/limits", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// nova's limits view hardcodes "rate": [] — no rows is the normal answer
		// there, not a failure.
		_, _ = w.Write([]byte(`{"limits": {"absolute": {"maxTotalCores": 20}, "rate": []}}`))
	})

	t1 := output.Table{Columns: []string{"Service", "Verb", "URI", "Regex", "Limit", "Remaining", "Unit", "Next Available"}}
	client := serviceClient(fakeServer, "compute", "latest")
	if err := appendRateLimits(context.Background(), client, "compute", "proj-1", &t1); err != nil {
		t.Fatalf("appendRateLimits returned error: %v", err)
	}
	th.AssertEquals(t, 0, len(t1.Rows))
}

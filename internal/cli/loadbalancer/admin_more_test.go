package loadbalancer

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// --- flavor ------------------------------------------------------------

func TestRunFlavorList_FilterAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/v2.0/lbaas/flavors", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		th.TestFormValues(t, r, map[string]string{"name": "standard"})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"flavors": [
          {"id": "fl1", "name": "standard", "enabled": true, "flavor_profile_id": "fp1", "description": "std"}
        ]}`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runFlavorList(context.Background(), lbClient(fakeServer), o, "standard", &buf); err != nil {
		t.Fatalf("runFlavorList error: %v", err)
	}
	for _, want := range []string{"fl1", "standard", "fp1", "std"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q\n---\n%s", want, buf.String())
		}
	}
}

func TestRunFlavorList_Error(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/v2.0/lbaas/flavors", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runFlavorList(context.Background(), lbClient(fakeServer), o, "", &buf)
	if err == nil || !strings.Contains(err.Error(), "listing load balancer flavors") {
		t.Fatalf("runFlavorList() = %v, want a wrapped listing error", err)
	}
}

func TestRunFlavorShow_ResolvesNameThenShows(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/v2.0/lbaas/flavors", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"flavors": [{"id": "fl1", "name": "standard"}]}`))
	})
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/flavors/fl1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"flavor": {"id": "fl1", "name": "standard", "enabled": true, "flavor_profile_id": "fp1", "description": "std"}}`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runFlavorShow(context.Background(), lbClient(fakeServer), o, "standard", &buf); err != nil {
		t.Fatalf("runFlavorShow error: %v", err)
	}
	if !strings.Contains(buf.String(), "fl1") {
		t.Errorf("output missing flavor ID\n---\n%s", buf.String())
	}
}

// Two flavors sharing a name is ambiguous and must be rejected before any Get.
func TestRunFlavorShow_AmbiguousNameErrors(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/v2.0/lbaas/flavors", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"flavors": [
          {"id": "fl1", "name": "dup"}, {"id": "fl2", "name": "dup"}
        ]}`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runFlavorShow(context.Background(), lbClient(fakeServer), o, "dup", &buf)
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("runFlavorShow() = %v, want an ambiguous-name error", err)
	}
}

func TestRunFlavorShow_NotFound(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubEmptyList(fakeServer, "/v2.0/lbaas/flavors", "flavors")
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/flavors/nonesuch", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runFlavorShow(context.Background(), lbClient(fakeServer), o, "nonesuch", &buf)
	if err == nil || !strings.Contains(err.Error(), `showing load balancer flavor "nonesuch"`) {
		t.Fatalf("runFlavorShow() = %v, want a wrapped showing error", err)
	}
}

func TestRunFlavorCreate_ResolvesProfileAndSendsBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/v2.0/lbaas/flavorprofiles", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"flavorprofiles": [{"id": "fp1", "name": "amphora-single", "provider_name": "amphora"}]}`))
	})
	var gotBody string
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/flavors", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		gotBody = string(buf)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"flavor": {"id": "fl1", "name": "standard", "enabled": true, "flavor_profile_id": "fp1"}}`))
	})

	f := &octaviaFlavorCreateFlags{description: "std", flavorProfile: "amphora-single"}
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runFlavorCreate(context.Background(), lbClient(fakeServer), o, "standard", f, &buf); err != nil {
		t.Fatalf("runFlavorCreate error: %v", err)
	}
	if !strings.Contains(gotBody, `"flavor_profile_id":"fp1"`) {
		t.Errorf("request body missing resolved flavor_profile_id\n---\n%s", gotBody)
	}
	if !strings.Contains(gotBody, `"enabled":true`) {
		t.Errorf("request body should default enabled true, got %q", gotBody)
	}
	if !strings.Contains(buf.String(), "fl1") {
		t.Errorf("output missing new flavor ID\n---\n%s", buf.String())
	}
}

// flavors.CreateOpts tags Enabled `json:"enabled,omitempty"` with no pointer, so
// unlike `flavor set` (which routes --disable through a raw PUT, see
// TestRunFlavorSet_DisableSendsEnabledFalse in admin_test.go) `flavor create
// --disable` has no such fallback: false is indistinguishable from the zero
// value and gophercloud's request encoder drops the key entirely. This locks in
// that documented gap rather than asserting a false fix.
func TestRunFlavorCreate_DisableOmitsEnabledField(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubEmptyList(fakeServer, "/v2.0/lbaas/flavorprofiles", "flavorprofiles")
	var gotBody string
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/flavors", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		gotBody = string(buf)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"flavor": {"id": "fl1", "name": "standard", "enabled": false, "flavor_profile_id": "fp1"}}`))
	})

	f := &octaviaFlavorCreateFlags{flavorProfile: "fp1", disable: true}
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runFlavorCreate(context.Background(), lbClient(fakeServer), o, "standard", f, &buf); err != nil {
		t.Fatalf("runFlavorCreate error: %v", err)
	}
	if strings.Contains(gotBody, "enabled") {
		t.Errorf("expected the omitempty encoder to drop enabled:false entirely, got %q", gotBody)
	}
}

func TestRunFlavorCreate_Error(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubEmptyList(fakeServer, "/v2.0/lbaas/flavorprofiles", "flavorprofiles")
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/flavors", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusBadRequest)
		}
	})

	f := &octaviaFlavorCreateFlags{flavorProfile: "fp1"}
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runFlavorCreate(context.Background(), lbClient(fakeServer), o, "bad", f, &buf)
	if err == nil || !strings.Contains(err.Error(), `creating load balancer flavor "bad"`) {
		t.Fatalf("runFlavorCreate() = %v, want a wrapped creating error", err)
	}
}

func TestRunFlavorDelete_ResolvesEachRefAndReports(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/v2.0/lbaas/flavors", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"flavors": [{"id": "fl1", "name": "standard"}]}`))
	})
	var deleted []string
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/flavors/fl1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodDelete)
		deleted = append(deleted, "fl1")
		w.WriteHeader(http.StatusNoContent)
	})

	var buf bytes.Buffer
	if err := runFlavorDelete(context.Background(), lbClient(fakeServer), []string{"standard"}, &buf); err != nil {
		t.Fatalf("runFlavorDelete error: %v", err)
	}
	if len(deleted) != 1 {
		t.Fatalf("deleted = %v, want exactly one DELETE", deleted)
	}
	if !strings.Contains(buf.String(), "Deleted load balancer flavor standard") {
		t.Errorf("output = %q", buf.String())
	}
}

// --- flavorprofile -------------------------------------------------------

func TestRunFlavorProfileList_FilterAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/v2.0/lbaas/flavorprofiles", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		th.TestFormValues(t, r, map[string]string{"name": "amphora-single"})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"flavorprofiles": [
          {"id": "fp1", "name": "amphora-single", "provider_name": "amphora"}
        ]}`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runFlavorProfileList(context.Background(), lbClient(fakeServer), o, "amphora-single", &buf); err != nil {
		t.Fatalf("runFlavorProfileList error: %v", err)
	}
	for _, want := range []string{"fp1", "amphora-single", "amphora"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q\n---\n%s", want, buf.String())
		}
	}
}

func TestRunFlavorProfileList_Error(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/v2.0/lbaas/flavorprofiles", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runFlavorProfileList(context.Background(), lbClient(fakeServer), o, "", &buf)
	if err == nil || !strings.Contains(err.Error(), "listing flavor profiles") {
		t.Fatalf("runFlavorProfileList() = %v, want a wrapped listing error", err)
	}
}

func TestRunFlavorProfileShow_ResolvesNameThenShows(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/v2.0/lbaas/flavorprofiles", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2.0/lbaas/flavorprofiles" && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"flavorprofiles": [{"id": "fp1", "name": "amphora-single", "provider_name": "amphora"}]}`))
			return
		}
		w.WriteHeader(http.StatusMethodNotAllowed)
	})
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/flavorprofiles/fp1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"flavorprofile": {"id": "fp1", "name": "amphora-single", "provider_name": "amphora", "flavor_data": "{}"}}`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runFlavorProfileShow(context.Background(), lbClient(fakeServer), o, "amphora-single", &buf); err != nil {
		t.Fatalf("runFlavorProfileShow error: %v", err)
	}
	if !strings.Contains(buf.String(), "fp1") {
		t.Errorf("output missing flavor profile ID\n---\n%s", buf.String())
	}
}

func TestRunFlavorProfileShow_NotFound(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubEmptyList(fakeServer, "/v2.0/lbaas/flavorprofiles", "flavorprofiles")
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/flavorprofiles/nonesuch", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runFlavorProfileShow(context.Background(), lbClient(fakeServer), o, "nonesuch", &buf)
	if err == nil || !strings.Contains(err.Error(), `showing flavor profile "nonesuch"`) {
		t.Fatalf("runFlavorProfileShow() = %v, want a wrapped showing error", err)
	}
}

func TestRunFlavorProfileCreate_RequestBodyAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/v2.0/lbaas/flavorprofiles", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		th.TestJSONRequest(t, r, `{"flavorprofile": {
          "name": "amphora-single", "provider_name": "amphora", "flavor_data": "{\"loadbalancer_topology\": \"SINGLE\"}"
        }}`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"flavorprofile": {
          "id": "fp1", "name": "amphora-single", "provider_name": "amphora",
          "flavor_data": "{\"loadbalancer_topology\": \"SINGLE\"}"
        }}`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runFlavorProfileCreate(context.Background(), lbClient(fakeServer), o,
		"amphora-single", "amphora", `{"loadbalancer_topology": "SINGLE"}`, &buf)
	if err != nil {
		t.Fatalf("runFlavorProfileCreate error: %v", err)
	}
	if !strings.Contains(buf.String(), "fp1") {
		t.Errorf("output missing new flavor profile ID\n---\n%s", buf.String())
	}
}

func TestRunFlavorProfileCreate_Error(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/v2.0/lbaas/flavorprofiles", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusConflict)
		}
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runFlavorProfileCreate(context.Background(), lbClient(fakeServer), o, "dup", "amphora", "{}", &buf)
	if err == nil || !strings.Contains(err.Error(), `creating flavor profile "dup"`) {
		t.Fatalf("runFlavorProfileCreate() = %v, want a wrapped creating error", err)
	}
}

// Every UpdateOpts field is omitempty, so only --provider being given must send
// provider_name alone, leaving name and flavor_data out of the body.
func TestRunFlavorProfileSet_SparseUpdate(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubEmptyList(fakeServer, "/v2.0/lbaas/flavorprofiles", "flavorprofiles")
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/flavorprofiles/fp1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"flavorprofile": {"provider_name": "ovn"}}`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"flavorprofile": {"id": "fp1", "name": "amphora-single", "provider_name": "ovn", "flavor_data": "{}"}}`))
	})

	f := &flavorProfileSetFlags{providerName: "ovn"}
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runFlavorProfileSet(context.Background(), lbClient(fakeServer), o, "fp1", f, &buf); err != nil {
		t.Fatalf("runFlavorProfileSet error: %v", err)
	}
	if !strings.Contains(buf.String(), "ovn") {
		t.Errorf("output missing updated provider\n---\n%s", buf.String())
	}
}

func TestRunFlavorProfileSet_Error(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubEmptyList(fakeServer, "/v2.0/lbaas/flavorprofiles", "flavorprofiles")
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/flavorprofiles/fp1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			w.WriteHeader(http.StatusNotFound)
		}
	})

	f := &flavorProfileSetFlags{name: "renamed"}
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runFlavorProfileSet(context.Background(), lbClient(fakeServer), o, "fp1", f, &buf)
	if err == nil || !strings.Contains(err.Error(), `updating flavor profile "fp1"`) {
		t.Fatalf("runFlavorProfileSet() = %v, want a wrapped updating error", err)
	}
}

func TestRunFlavorProfileDelete_ResolvesEachRefAndReports(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubEmptyList(fakeServer, "/v2.0/lbaas/flavorprofiles", "flavorprofiles")
	var deleted []string
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/flavorprofiles/fp1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodDelete)
		deleted = append(deleted, "fp1")
		w.WriteHeader(http.StatusNoContent)
	})

	var buf bytes.Buffer
	if err := runFlavorProfileDelete(context.Background(), lbClient(fakeServer), []string{"fp1"}, &buf); err != nil {
		t.Fatalf("runFlavorProfileDelete error: %v", err)
	}
	if len(deleted) != 1 {
		t.Fatalf("deleted = %v, want exactly one DELETE", deleted)
	}
	if !strings.Contains(buf.String(), "Deleted flavor profile fp1") {
		t.Errorf("output = %q", buf.String())
	}
}

// --- loadbalancer show ---------------------------------------------------

func TestRunLBShow_ResolvesNameThenShows(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/v2.0/lbaas/loadbalancers", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		th.TestFormValues(t, r, map[string]string{"name": "web-lb"})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"loadbalancers": [{"id": "lb1", "name": "web-lb"}]}`))
	})
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/loadbalancers/lb1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"loadbalancer": ` + lbBody + `}`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runLBShow(context.Background(), lbClient(fakeServer), o, "web-lb", &buf); err != nil {
		t.Fatalf("runLBShow error: %v", err)
	}
	for _, want := range []string{"lb1", "web-lb", "ACTIVE", "10.0.0.10"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q\n---\n%s", want, buf.String())
		}
	}
}

func TestRunLBShow_NotFound(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubLBList(fakeServer)
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/loadbalancers/nonesuch", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runLBShow(context.Background(), lbClient(fakeServer), o, "nonesuch", &buf)
	if err == nil || !strings.Contains(err.Error(), `showing load balancer "nonesuch"`) {
		t.Fatalf("runLBShow() = %v, want a wrapped showing error", err)
	}
}

// --- quota show ------------------------------------------------------------

func TestRunLBQuotaShow_Output(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/v2.0/quotas/p1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"quota": {"loadbalancer": 5, "listener": -1, "pool": 10, "member": 50, "healthmonitor": -1, "l7policy": 5, "l7rule": 20}}`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runLBQuotaShow(context.Background(), lbClient(fakeServer), o, "p1", &buf); err != nil {
		t.Fatalf("runLBQuotaShow error: %v", err)
	}
	for _, want := range []string{"loadbalancer", "5", "listener", "-1"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q\n---\n%s", want, buf.String())
		}
	}
}

func TestRunLBQuotaShow_Error(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/v2.0/quotas/p1", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runLBQuotaShow(context.Background(), lbClient(fakeServer), o, "p1", &buf)
	if err == nil || !strings.Contains(err.Error(), `showing load balancer quotas for project "p1"`) {
		t.Fatalf("runLBQuotaShow() = %v, want a wrapped showing error", err)
	}
}

// --- amphora show / failover ----------------------------------------------

func TestRunAmphoraShow_Output(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/v2.0/octavia/amphorae/am1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"amphora": {
          "id": "am1", "loadbalancer_id": "lb1", "compute_id": "srv1", "role": "MASTER",
          "status": "ALLOCATED", "lb_network_ip": "10.1.0.5", "ha_ip": "10.0.0.10",
          "image_id": "img1", "cached_zone": "az1"
        }}`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runAmphoraShow(context.Background(), lbClient(fakeServer), o, "am1", &buf); err != nil {
		t.Fatalf("runAmphoraShow error: %v", err)
	}
	for _, want := range []string{"am1", "lb1", "MASTER", "ALLOCATED", "10.1.0.5"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q\n---\n%s", want, buf.String())
		}
	}
}

func TestRunAmphoraShow_NotFound(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/v2.0/octavia/amphorae/nonesuch", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runAmphoraShow(context.Background(), lbClient(fakeServer), o, "nonesuch", &buf)
	if err == nil || !strings.Contains(err.Error(), "showing amphora nonesuch") {
		t.Fatalf("runAmphoraShow() = %v, want a wrapped showing error", err)
	}
}

func TestRunAmphoraFailover_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotPath string
	fakeServer.Mux.HandleFunc("/v2.0/octavia/amphorae/am1/failover", func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusAccepted)
	})

	var buf bytes.Buffer
	if err := runAmphoraFailover(context.Background(), lbClient(fakeServer), "am1", &buf); err != nil {
		t.Fatalf("runAmphoraFailover error: %v", err)
	}
	if gotMethod != http.MethodPut || gotPath != "/v2.0/octavia/amphorae/am1/failover" {
		t.Errorf("got %s %s, want PUT /v2.0/octavia/amphorae/am1/failover", gotMethod, gotPath)
	}
	if !strings.Contains(buf.String(), "Requested failover of amphora am1") {
		t.Errorf("output = %q", buf.String())
	}
}

func TestRunAmphoraFailover_Error(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/v2.0/octavia/amphorae/am1/failover", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	var buf bytes.Buffer
	err := runAmphoraFailover(context.Background(), lbClient(fakeServer), "am1", &buf)
	if err == nil || !strings.Contains(err.Error(), "failing over amphora am1") {
		t.Fatalf("runAmphoraFailover() = %v, want a wrapped failing-over error", err)
	}
}

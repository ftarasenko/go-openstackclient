package keyvrm

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
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

func keyvrmTestClient(fakeServer th.FakeServer) *gophercloud.ServiceClient {
	sc := fakeclient.ServiceClient(fakeServer)
	sc.Type = "keyvrm"
	sc.ResourceBase = sc.Endpoint + "v1/"
	return sc
}

func TestQuery(t *testing.T) {
	got := query("/v1/host_aggregates", listOpts{Limit: 50, Offset: 10, filters: map[string]string{"marker": "HA", "empty": ""}})
	if !strings.Contains(got, "limit=50") || !strings.Contains(got, "offset=10") || !strings.Contains(got, "marker=HA") {
		t.Errorf("query = %q", got)
	}
	if strings.Contains(got, "empty=") {
		t.Errorf("empty filter should be omitted: %q", got)
	}
	if base := query("/v1/x", listOpts{}); base != "/v1/x" {
		t.Errorf("no-params query = %q", base)
	}
}

func TestBuildBody_ExcludeNone(t *testing.T) {
	cmd := &cobra.Command{Use: "t", RunE: func(*cobra.Command, []string) error { return nil }}
	cmd.Flags().Bool("enabled", false, "")
	cmd.Flags().Int("period", 0, "")
	cmd.Flags().String("nova-enabled-filters", "", "")
	if err := cmd.Flags().Parse([]string{"--enabled", "--period", "60"}); err != nil {
		t.Fatal(err)
	}
	spec := []flagSpec{
		{"enabled", "enabled", kindBool},
		{"period", "period", kindInt},
		{"nova-enabled-filters", "nova_enabled_filters", kindStr},
	}
	body := buildBody(cmd, spec)
	if len(body) != 2 || body["enabled"] != true || body["period"] != 60 {
		t.Errorf("buildBody = %#v, want only enabled+period", body)
	}
	if _, ok := body["nova_enabled_filters"]; ok {
		t.Error("unset flag must be excluded")
	}
}

func TestRunHAList_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotQuery string
	fakeServer.Mux.HandleFunc("/v1/host_aggregates", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, "GET")
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"ha-1","availability_zone_name":"az1","host_aggregate_name":"agg1","marker":"HA","no_op_mode":false,"lb_period":60,"created_at":"2026-01-01T00:00:00Z"}],"total":1,"limit":50,"offset":0}`)
	})

	sc := keyvrmTestClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}
	opts := listOpts{Limit: 50, filters: map[string]string{"availability_zone_name": "az1", "marker": "HA"}}

	var buf bytes.Buffer
	if err := runHAList(context.Background(), sc, o, opts, &buf); err != nil {
		t.Fatalf("runHAList: %v", err)
	}
	if !strings.Contains(gotQuery, "availability_zone_name=az1") || !strings.Contains(gotQuery, "marker=HA") {
		t.Errorf("query = %q", gotQuery)
	}
	if !strings.Contains(buf.String(), "ha-1") || !strings.Contains(buf.String(), "agg1") {
		t.Errorf("output = %q", buf.String())
	}
}

func TestRunAppConfigSet_PutsOnlyChangedFields(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/v1/app_config", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, "PUT")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"enabled":true,"period":60}`)
	})

	sc := keyvrmTestClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}
	body := map[string]any{"enabled": true, "period": 60}

	var buf bytes.Buffer
	if err := runAppConfigSet(context.Background(), sc, o, body, &buf); err != nil {
		t.Fatalf("runAppConfigSet: %v", err)
	}
	if gotBody["enabled"] != true || gotBody["period"] != float64(60) {
		t.Errorf("PUT body = %#v", gotBody)
	}
	if _, ok := gotBody["nova_enabled_filters"]; ok {
		t.Error("PUT body must not contain unset fields")
	}
}

func TestRunAppConfigShow_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/v1/app_config", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, "GET")
		th.AssertEquals(t, "/v1/app_config", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"enabled":true,"period":120,"nova_enabled_filters":"RamFilter","ha_power_fence_mode":"ipmi","executor_timeout":300}`)
	})

	sc := keyvrmTestClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}

	var buf bytes.Buffer
	if err := runAppConfigShow(context.Background(), sc, o, &buf); err != nil {
		t.Fatalf("runAppConfigShow: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"true", "120", "RamFilter", "ipmi", "300"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q missing %q", out, want)
		}
	}
}

func TestRunAZList_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotQuery string
	fakeServer.Mux.HandleFunc("/v1/azones", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, "GET")
		th.AssertEquals(t, "/v1/azones", r.URL.Path)
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"name":"az1","aggregates_count":3,"aggregates_event_counts":{"active":2,"warning":1,"error":0,"noop":4}}],"total":1,"limit":50,"offset":5}`)
	})

	sc := keyvrmTestClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}

	var buf bytes.Buffer
	if err := runAZList(context.Background(), sc, o, listOpts{Limit: 50, Offset: 5}, &buf); err != nil {
		t.Fatalf("runAZList: %v", err)
	}
	if !strings.Contains(gotQuery, "limit=50") || !strings.Contains(gotQuery, "offset=5") {
		t.Errorf("query = %q", gotQuery)
	}
	out := buf.String()
	for _, want := range []string{"az1", "3", "2", "1", "4"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q missing %q", out, want)
		}
	}
}

func TestRunHAShow_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/v1/host_aggregates/ha-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, "GET")
		th.AssertEquals(t, "/v1/host_aggregates/ha-1", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"ha-1","availability_zone_name":"az1","host_aggregate_name":"agg1","marker":"HA","no_op_mode":false,"lb_period":60,"created_at":"2026-01-01T00:00:00Z"}`)
	})

	sc := keyvrmTestClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}

	var buf bytes.Buffer
	if err := runHAShow(context.Background(), sc, o, "ha-1", &buf); err != nil {
		t.Fatalf("runHAShow: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"ha-1", "az1", "agg1", "HA"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q missing %q", out, want)
		}
	}
}

func TestRunHASet_PutsOnlyChangedFields(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/v1/host_aggregates/ha-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, "PUT")
		th.AssertEquals(t, "/v1/host_aggregates/ha-1", r.URL.Path)
		th.TestJSONRequest(t, r, `{"marker":"LB","lb_period":90}`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"ha-1","availability_zone_name":"az1","host_aggregate_name":"agg1","marker":"LB","no_op_mode":false,"lb_period":90,"created_at":"2026-01-01T00:00:00Z"}`)
	})

	sc := keyvrmTestClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}
	body := map[string]any{"marker": "LB", "lb_period": 90}

	var buf bytes.Buffer
	if err := runHASet(context.Background(), sc, o, "ha-1", body, &buf); err != nil {
		t.Fatalf("runHASet: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "LB") || !strings.Contains(out, "90") {
		t.Errorf("output = %q", out)
	}
}

func TestRunEventRecList_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotQuery string
	fakeServer.Mux.HandleFunc("/v1/host_aggregate_events/ev-1/recommendations", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, "GET")
		th.AssertEquals(t, "/v1/host_aggregate_events/ev-1/recommendations", r.URL.Path)
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"rec-1","host_aggregate_event_id":"ev-1","vm_uuid":"vm-9","source_hv_name":"hv-a","destination_hv_name":"hv-b","status":"pending","type":"evacuate","evacuate_priority":2}],"total":1,"limit":50,"offset":0}`)
	})

	sc := keyvrmTestClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}
	opts := listOpts{Limit: 50, filters: map[string]string{"status": "pending"}}

	var buf bytes.Buffer
	if err := runEventRecList(context.Background(), sc, o, "ev-1", opts, &buf); err != nil {
		t.Fatalf("runEventRecList: %v", err)
	}
	if !strings.Contains(gotQuery, "status=pending") {
		t.Errorf("query = %q", gotQuery)
	}
	out := buf.String()
	for _, want := range []string{"rec-1", "vm-9", "hv-a", "hv-b", "pending", "evacuate"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q missing %q", out, want)
		}
	}
}

func TestRunRecommendationTrigger(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	hit := false
	fakeServer.Mux.HandleFunc("/v1/recommendations/rec-1/run", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, "POST")
		hit = true
		w.WriteHeader(http.StatusAccepted)
	})

	sc := keyvrmTestClient(fakeServer)
	if err := runRecommendation(context.Background(), sc, "rec-1"); err != nil {
		t.Fatalf("runRecommendation: %v", err)
	}
	if !hit {
		t.Error("expected POST to /v1/recommendations/rec-1/run")
	}
}

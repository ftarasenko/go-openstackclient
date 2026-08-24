package keyvrm

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"
)

// --- query() page-parameter builder ---

func TestQuery_LimitOffsetOnly(t *testing.T) {
	got := query("/v1/x", listOpts{Limit: 25})
	if got != "/v1/x?limit=25" {
		t.Errorf("query = %q, want limit-only", got)
	}
	got = query("/v1/x", listOpts{Offset: 5})
	if got != "/v1/x?offset=5" {
		t.Errorf("query = %q, want offset-only", got)
	}
}

func TestQuery_ZeroLimitOffsetOmitted(t *testing.T) {
	got := query("/v1/x", listOpts{Limit: 0, Offset: 0, filters: map[string]string{"marker": "HA"}})
	if strings.Contains(got, "limit=") || strings.Contains(got, "offset=") {
		t.Errorf("query = %q, zero limit/offset must be omitted", got)
	}
	if !strings.Contains(got, "marker=HA") {
		t.Errorf("query = %q, missing filter", got)
	}
}

func TestQuery_NilFilters(t *testing.T) {
	got := query("/v1/x", listOpts{Limit: 10, filters: nil})
	if got != "/v1/x?limit=10" {
		t.Errorf("query = %q, want limit only with nil filters map", got)
	}
}

// --- getMarkers ---

func TestGetMarkers_Decode(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/v1/host_aggregates/markers", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, "GET")
		th.AssertEquals(t, "/v1/host_aggregates/markers", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `["LB","HA","HA+LB"]`)
	})

	sc := keyvrmTestClient(fakeServer)
	got, err := getMarkers(context.Background(), sc)
	if err != nil {
		t.Fatalf("getMarkers: %v", err)
	}
	want := []string{"LB", "HA", "HA+LB"}
	if len(got) != len(want) {
		t.Fatalf("getMarkers = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("getMarkers[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestGetMarkers_ErrorPath(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/v1/host_aggregates/markers", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	sc := keyvrmTestClient(fakeServer)
	if _, err := getMarkers(context.Background(), sc); err == nil {
		t.Fatal("expected error from 500 response, got nil")
	}
}

func TestGetMarkers_MalformedBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/v1/host_aggregates/markers", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"not":"a list"}`)
	})

	sc := keyvrmTestClient(fakeServer)
	if _, err := getMarkers(context.Background(), sc); err == nil {
		t.Fatal("expected decode error for object body into []string, got nil")
	}
}

// --- listHostAggregateEvents ---

func TestListHostAggregateEvents_RequestAndDecode(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotQuery string
	fakeServer.Mux.HandleFunc("/v1/host_aggregates/ha-1/events", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, "GET")
		th.AssertEquals(t, "/v1/host_aggregates/ha-1/events", r.URL.Path)
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"ev-1","host_aggregate_config_id":"ha-1","marker":"HA","status":"active","state":"stable","error_details":"","created_at":"2026-01-01T00:00:00Z"}],"total":1,"limit":50,"offset":0}`)
	})

	sc := keyvrmTestClient(fakeServer)
	p, err := listHostAggregateEvents(context.Background(), sc, "ha-1", listOpts{Limit: 50, filters: map[string]string{"status": "active"}})
	if err != nil {
		t.Fatalf("listHostAggregateEvents: %v", err)
	}
	if !strings.Contains(gotQuery, "status=active") || !strings.Contains(gotQuery, "limit=50") {
		t.Errorf("query = %q", gotQuery)
	}
	if len(p.Data) != 1 || p.Data[0].ID != "ev-1" || p.Data[0].Status != "active" {
		t.Errorf("decoded page = %#v", p)
	}
	if p.Total != 1 {
		t.Errorf("Total = %d, want 1", p.Total)
	}
}

func TestListHostAggregateEvents_ErrorPath(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/v1/host_aggregates/missing/events", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	sc := keyvrmTestClient(fakeServer)
	if _, err := listHostAggregateEvents(context.Background(), sc, "missing", listOpts{}); err == nil {
		t.Fatal("expected error from 404 response, got nil")
	}
}

// --- listZoneHostAggregates ---

func TestListZoneHostAggregates_RequestAndDecode(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotQuery string
	fakeServer.Mux.HandleFunc("/v1/azones/az1/host_aggregates", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, "GET")
		th.AssertEquals(t, "/v1/azones/az1/host_aggregates", r.URL.Path)
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"ha-2","availability_zone_name":"az1","host_aggregate_name":"agg2","marker":null,"no_op_mode":true,"lb_period":30,"created_at":"2026-01-02T00:00:00Z"}],"total":1,"limit":50,"offset":0}`)
	})

	sc := keyvrmTestClient(fakeServer)
	opts := listOpts{Limit: 50, filters: map[string]string{"host_aggregate_name": "agg2", "no_op_mode": "true"}}
	p, err := listZoneHostAggregates(context.Background(), sc, "az1", opts)
	if err != nil {
		t.Fatalf("listZoneHostAggregates: %v", err)
	}
	if !strings.Contains(gotQuery, "host_aggregate_name=agg2") || !strings.Contains(gotQuery, "no_op_mode=true") {
		t.Errorf("query = %q", gotQuery)
	}
	if len(p.Data) != 1 || p.Data[0].ID != "ha-2" || p.Data[0].Marker != nil || !p.Data[0].NoOpMode {
		t.Errorf("decoded page = %#v", p.Data)
	}
}

func TestListZoneHostAggregates_MalformedBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/v1/azones/az1/host_aggregates", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `not json`)
	})

	sc := keyvrmTestClient(fakeServer)
	if _, err := listZoneHostAggregates(context.Background(), sc, "az1", listOpts{}); err == nil {
		t.Fatal("expected decode error for malformed body, got nil")
	}
}

// --- getEvent ---

func TestGetEvent_RequestAndDecode(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/v1/host_aggregate_events/ev-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, "GET")
		th.AssertEquals(t, "/v1/host_aggregate_events/ev-1", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"ev-1","host_aggregate_config_id":"ha-1","marker":"HA","status":"error","state":"failed","error_details":"boom","created_at":"2026-01-01T00:00:00Z"}`)
	})

	sc := keyvrmTestClient(fakeServer)
	e, err := getEvent(context.Background(), sc, "ev-1")
	if err != nil {
		t.Fatalf("getEvent: %v", err)
	}
	if e.ID != "ev-1" || e.Status != "error" || e.ErrorDetails != "boom" {
		t.Errorf("getEvent = %#v", e)
	}
}

func TestGetEvent_ErrorPath(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/v1/host_aggregate_events/missing", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	sc := keyvrmTestClient(fakeServer)
	if _, err := getEvent(context.Background(), sc, "missing"); err == nil {
		t.Fatal("expected error from 404 response, got nil")
	}
}

// --- listRecommendations ---

func TestListRecommendations_RequestAndDecode(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotQuery string
	fakeServer.Mux.HandleFunc("/v1/recommendations", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, "GET")
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"rec-1","host_aggregate_event_id":"ev-1","vm_uuid":"vm-9","source_hv_name":"hv-a","destination_hv_name":"hv-b","status":"pending","type":"balance","evacuate_priority":null,"created_at":"2026-01-01T00:00:00Z"}],"total":1,"limit":25,"offset":0}`)
	})

	sc := keyvrmTestClient(fakeServer)
	opts := listOpts{Limit: 25, filters: map[string]string{"host_aggregate_event_id": "ev-1", "status": "pending"}}
	p, err := listRecommendations(context.Background(), sc, opts)
	if err != nil {
		t.Fatalf("listRecommendations: %v", err)
	}
	if !strings.Contains(gotQuery, "host_aggregate_event_id=ev-1") || !strings.Contains(gotQuery, "status=pending") {
		t.Errorf("query = %q", gotQuery)
	}
	if len(p.Data) != 1 || p.Data[0].ID != "rec-1" || p.Data[0].EvacuatePriority != nil {
		t.Errorf("decoded page = %#v", p.Data)
	}
}

func TestListRecommendations_ErrorPath(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/v1/recommendations", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	})

	sc := keyvrmTestClient(fakeServer)
	if _, err := listRecommendations(context.Background(), sc, listOpts{}); err == nil {
		t.Fatal("expected error from 400 response, got nil")
	}
}

// --- getRecommendation ---

func TestGetRecommendation_RequestAndDecode(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/v1/recommendations/rec-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, "GET")
		th.AssertEquals(t, "/v1/recommendations/rec-1", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"rec-1","host_aggregate_event_id":"ev-1","vm_uuid":"vm-9","source_hv_name":"hv-a","destination_hv_name":"hv-b","status":"running","type":"evacuate","reason":"overload","evacuate_priority":2,"created_at":"2026-01-01T00:00:00Z"}`)
	})

	sc := keyvrmTestClient(fakeServer)
	r, err := getRecommendation(context.Background(), sc, "rec-1")
	if err != nil {
		t.Fatalf("getRecommendation: %v", err)
	}
	if r.ID != "rec-1" || r.Reason != "overload" || r.EvacuatePriority == nil || *r.EvacuatePriority != 2 {
		t.Errorf("getRecommendation = %#v", r)
	}
}

func TestGetRecommendation_ErrorPath(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/v1/recommendations/missing", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	sc := keyvrmTestClient(fakeServer)
	if _, err := getRecommendation(context.Background(), sc, "missing"); err == nil {
		t.Fatal("expected error from 404 response, got nil")
	}
}

// --- listRecommendationOperations ---

func TestListRecommendationOperations_RequestAndDecode(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotQuery string
	fakeServer.Mux.HandleFunc("/v1/recommendations/rec-1/operations", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, "GET")
		th.AssertEquals(t, "/v1/recommendations/rec-1/operations", r.URL.Path)
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"op-1","recommendation_id":"rec-1","status":"failed","openstack_request_id":"req-1","nova_migration_id":"mig-1","error_details":"timeout","failure_type":"transient","created_at":"2026-01-01T00:00:00Z"}],"total":1,"limit":50,"offset":0}`)
	})

	sc := keyvrmTestClient(fakeServer)
	opts := listOpts{Limit: 50, filters: map[string]string{"status": "failed"}}
	p, err := listRecommendationOperations(context.Background(), sc, "rec-1", opts)
	if err != nil {
		t.Fatalf("listRecommendationOperations: %v", err)
	}
	if !strings.Contains(gotQuery, "status=failed") {
		t.Errorf("query = %q", gotQuery)
	}
	if len(p.Data) != 1 || p.Data[0].ID != "op-1" || p.Data[0].FailureType != "transient" {
		t.Errorf("decoded page = %#v", p.Data)
	}
}

func TestListRecommendationOperations_ErrorPath(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/v1/recommendations/missing/operations", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	sc := keyvrmTestClient(fakeServer)
	if _, err := listRecommendationOperations(context.Background(), sc, "missing", listOpts{}); err == nil {
		t.Fatal("expected error from 500 response, got nil")
	}
}

// --- stopRecommendation ---

func TestStopRecommendation_Request(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	hit := false
	fakeServer.Mux.HandleFunc("/v1/recommendations/rec-1/stop", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, "POST")
		hit = true
		w.WriteHeader(http.StatusOK)
	})

	sc := keyvrmTestClient(fakeServer)
	if err := stopRecommendation(context.Background(), sc, "rec-1"); err != nil {
		t.Fatalf("stopRecommendation: %v", err)
	}
	if !hit {
		t.Error("expected POST to /v1/recommendations/rec-1/stop")
	}
}

func TestStopRecommendation_ErrorPath(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/v1/recommendations/rec-1/stop", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
	})

	sc := keyvrmTestClient(fakeServer)
	if err := stopRecommendation(context.Background(), sc, "rec-1"); err == nil {
		t.Fatal("expected error from 409 response, got nil")
	}
}

// --- runEventRecommendations ---

func TestRunEventRecommendations_Request(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	hit := false
	fakeServer.Mux.HandleFunc("/v1/host_aggregate_events/ev-1/recommendations/run", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, "POST")
		hit = true
		w.WriteHeader(http.StatusAccepted)
	})

	sc := keyvrmTestClient(fakeServer)
	if err := runEventRecommendations(context.Background(), sc, "ev-1"); err != nil {
		t.Fatalf("runEventRecommendations: %v", err)
	}
	if !hit {
		t.Error("expected POST to /v1/host_aggregate_events/ev-1/recommendations/run")
	}
}

func TestRunEventRecommendations_ErrorPath(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/v1/host_aggregate_events/missing/recommendations/run", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	sc := keyvrmTestClient(fakeServer)
	if err := runEventRecommendations(context.Background(), sc, "missing"); err == nil {
		t.Fatal("expected error from 404 response, got nil")
	}
}

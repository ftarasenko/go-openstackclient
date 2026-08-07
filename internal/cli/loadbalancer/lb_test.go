package loadbalancer

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

// lbClient builds a fake octavia client. Octavia versions via the URL rather than
// a microversion header, so Type is set but Microversion is deliberately empty —
// matching what auth.Client.LoadBalancer produces.
func lbClient(fakeServer th.FakeServer) *gophercloud.ServiceClient {
	sc := fakeclient.ServiceClient(fakeServer)
	sc.Type = "load-balancer"
	// openstack.NewLoadBalancerV2 roots every request at <endpoint>v2.0/, so the
	// fake client does too and the asserted paths are the real ones.
	sc.ResourceBase = sc.Endpoint + "v2.0/"
	return sc
}

// stubLBList answers resolveLoadBalancerID's name lookup with an empty list, so a
// non-UUID reference falls through to being treated as an ID (the documented
// zero-match behaviour). Tests that assert on the collection endpoint register
// their own handler instead.
func stubLBList(fakeServer th.FakeServer) {
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/loadbalancers", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"loadbalancers": []}`))
	})
}

const lbBody = `{
  "id": "lb1", "name": "web-lb", "description": "front end",
  "provisioning_status": "ACTIVE", "operating_status": "ONLINE",
  "admin_state_up": true, "project_id": "p1",
  "vip_address": "10.0.0.10", "vip_port_id": "port-1", "vip_subnet_id": "sub-1",
  "vip_network_id": "net-1", "provider": "amphora", "availability_zone": "az1",
  "tags": ["prod"]
}`

func TestRunLBList_FiltersAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/loadbalancers", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		// Octavia has no microversion header; asserting its absence keeps the
		// client factory honest.
		if got := r.Header.Get("OpenStack-API-Version"); got != "" {
			t.Errorf("octavia takes no microversion header, got %q", got)
		}
		th.TestFormValues(t, r, map[string]string{
			"name":                "web-lb",
			"project_id":          "p1",
			"provisioning_status": "ACTIVE",
			"admin_state_up":      "true",
		})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"loadbalancers": [` + lbBody + `]}`))
	})

	up := true
	f := &lbListFlags{name: "web-lb", provisioningStatus: "ACTIVE", adminStateUp: &up, long: true}
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runLBList(context.Background(), lbClient(fakeServer), o, f, resolvedLBRefs{projectID: "p1"}, &buf)
	if err != nil {
		t.Fatalf("runLBList error: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	for _, want := range []string{"lb1", "web-lb", "10.0.0.10", "ACTIVE", "ONLINE", "amphora", "az1"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q\n---\n%s", want, buf.String())
		}
	}
}

func TestRunLBCreate_RequestBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/loadbalancers", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"loadbalancers": []}`))
			return
		}
		gotMethod = r.Method
		th.TestJSONRequest(t, r, `{"loadbalancer": {
          "name": "web-lb",
          "description": "front end",
          "vip_subnet_id": "sub-1",
          "admin_state_up": true,
          "tags": ["prod"]
        }}`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"loadbalancer": ` + lbBody + `}`))
	})

	up := true
	f := &lbCreateFlags{description: "front end", tag: []string{"prod"}, adminStateUp: &up}
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runLBCreate(context.Background(), lbClient(fakeServer), o, "web-lb", f,
		resolvedLBRefs{vipSubnetID: "sub-1"}, &buf)
	if err != nil {
		t.Fatalf("runLBCreate error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if !strings.Contains(buf.String(), "lb1") {
		t.Errorf("output missing the new load balancer ID\n---\n%s", buf.String())
	}
}

// Octavia returns PENDING_CREATE; --wait must poll until ACTIVE and re-read the
// record so the reported VIP and status are the settled ones.
func TestRunLBCreate_WaitPollsUntilActive(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/v2.0/lbaas/loadbalancers", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"loadbalancers": []}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"loadbalancer": {"id": "lb1", "name": "web-lb", "provisioning_status": "PENDING_CREATE"}}`))
	})
	gets := 0
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/loadbalancers/lb1", func(w http.ResponseWriter, _ *http.Request) {
		gets++
		w.Header().Set("Content-Type", "application/json")
		if gets < 3 {
			_, _ = w.Write([]byte(`{"loadbalancer": {"id": "lb1", "name": "web-lb", "provisioning_status": "PENDING_CREATE"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"loadbalancer": ` + lbBody + `}`))
	})

	fastPolling(t)

	f := &lbCreateFlags{wait: true, waitTimeout: 10 * time.Second}
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runLBCreate(context.Background(), lbClient(fakeServer), o, "web-lb", f,
		resolvedLBRefs{vipSubnetID: "sub-1"}, &buf)
	if err != nil {
		t.Fatalf("runLBCreate --wait error: %v", err)
	}
	if gets < 3 {
		t.Errorf("--wait made %d GETs; expected it to poll past PENDING_CREATE", gets)
	}
	if !strings.Contains(buf.String(), "ACTIVE") {
		t.Errorf("--wait should report the settled status\n---\n%s", buf.String())
	}
}

// ERROR is terminal: --wait must fail immediately rather than spinning until the
// timeout.
func TestWaitForLoadBalancerActive_FailsFastOnError(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubLBList(fakeServer)
	gets := 0
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/loadbalancers/lb1", func(w http.ResponseWriter, _ *http.Request) {
		gets++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"loadbalancer": {"id": "lb1", "provisioning_status": "ERROR"}}`))
	})

	fastPolling(t)

	err := waitForLoadBalancerActive(context.Background(), lbClient(fakeServer), "lb1", time.Minute)
	if err == nil || !strings.Contains(err.Error(), "ERROR") {
		t.Fatalf("err = %v, want an ERROR-state failure", err)
	}
	if gets != 1 {
		t.Errorf("made %d GETs; ERROR is terminal so one is enough", gets)
	}
}

// A 404 while waiting for a delete is the success signal, not a failure.
func TestWaitForLoadBalancerDeleted_404IsSuccess(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubLBList(fakeServer)
	gets := 0
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/loadbalancers/lb1", func(w http.ResponseWriter, _ *http.Request) {
		gets++
		if gets < 2 {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"loadbalancer": {"id": "lb1", "provisioning_status": "PENDING_DELETE"}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})

	fastPolling(t)

	if err := waitForLoadBalancerDeleted(context.Background(), lbClient(fakeServer), "lb1", time.Minute); err != nil {
		t.Fatalf("waitForLoadBalancerDeleted error: %v", err)
	}
	if gets < 2 {
		t.Errorf("expected the wait to poll past PENDING_DELETE, made %d GETs", gets)
	}
}

func TestRunLBSet_OnlySendsGivenAttributes(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubLBList(fakeServer)
	var gotMethod string
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/loadbalancers/lb1", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		th.TestJSONRequest(t, r, `{"loadbalancer": {"name": "renamed"}}`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"loadbalancer": ` + lbBody + `}`))
	})

	// description and tag are populated but their flags were not given, so they
	// must not appear in the request body.
	f := &lbSetFlags{name: "renamed", description: "ignored", tag: []string{"ignored"}}
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runLBSet(context.Background(), lbClient(fakeServer), o, "lb1", f, changedSet{"name": true}, &buf); err != nil {
		t.Fatalf("runLBSet error: %v", err)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
}

func TestRunLBSet_NoTagClearsTags(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubLBList(fakeServer)
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/loadbalancers/lb1", func(w http.ResponseWriter, r *http.Request) {
		th.TestJSONRequest(t, r, `{"loadbalancer": {"tags": []}}`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"loadbalancer": ` + lbBody + `}`))
	})

	f := &lbSetFlags{noTag: true}
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runLBSet(context.Background(), lbClient(fakeServer), o, "lb1", f, changedSet{}, &buf); err != nil {
		t.Fatalf("runLBSet error: %v", err)
	}
}

// A flagless set is rejected before any request, so the fake server registers
// nothing: any call at all would 404.
func TestRunLBSet_RejectsEmptyUpdate(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runLBSet(context.Background(), lbClient(fakeServer), o, "lb1", &lbSetFlags{}, changedSet{}, &buf)
	if err == nil || !strings.Contains(err.Error(), "nothing to set") {
		t.Fatalf("expected a 'nothing to set' error, got %v", err)
	}
}

func TestRunLBDelete_CascadeAndWording(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubLBList(fakeServer)
	var gotCascade string
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/loadbalancers/lb1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodDelete)
		gotCascade = r.URL.Query().Get("cascade")
		w.WriteHeader(http.StatusNoContent)
	})

	var buf bytes.Buffer
	err := runLBDelete(context.Background(), lbClient(fakeServer), []string{"lb1"}, true, false, 0, &buf)
	if err != nil {
		t.Fatalf("runLBDelete error: %v", err)
	}
	if gotCascade != "true" {
		t.Errorf("cascade query = %q, want true", gotCascade)
	}
	// Without --wait the delete is only accepted, and the wording must not claim
	// otherwise — octavia leaves the LB in PENDING_DELETE.
	if !strings.Contains(buf.String(), "Requested deletion") {
		t.Errorf("output %q should say the deletion was requested, not completed", buf.String())
	}
}

func TestRunLBFailover_Request(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubLBList(fakeServer)
	var gotMethod, gotPath string
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/loadbalancers/lb1/failover", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusAccepted)
	})

	var buf bytes.Buffer
	if err := runLBFailover(context.Background(), lbClient(fakeServer), "lb1", false, 0, &buf); err != nil {
		t.Fatalf("runLBFailover error: %v", err)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
	if gotPath != "/v2.0/lbaas/loadbalancers/lb1/failover" {
		t.Errorf("path = %q", gotPath)
	}
}

func TestRunLBStatsShow_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubLBList(fakeServer)
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/loadbalancers/lb1/stats", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"stats": {
          "active_connections": 3, "bytes_in": 4096, "bytes_out": 8192,
          "request_errors": 1, "total_connections": 42
        }}`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runLBStatsShow(context.Background(), lbClient(fakeServer), o, "lb1", &buf); err != nil {
		t.Fatalf("runLBStatsShow error: %v", err)
	}
	for _, want := range []string{"active_connections", "3", "bytes_in", "4096", "total_connections", "42"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q\n---\n%s", want, buf.String())
		}
	}
}

// The status endpoint returns a nested tree; it is flattened to one row per object
// so an OFFLINE member is visible at a glance.
func TestRunLBStatusShow_FlattensTheTree(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubLBList(fakeServer)
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/loadbalancers/lb1/status", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"statuses": {"loadbalancer": {
          "id": "lb1", "name": "web-lb", "provisioning_status": "ACTIVE", "operating_status": "DEGRADED",
          "listeners": [{
            "id": "li1", "name": "http", "provisioning_status": "ACTIVE", "operating_status": "ONLINE",
            "pools": [{
              "id": "po1", "name": "web-pool", "provisioning_status": "ACTIVE", "operating_status": "DEGRADED",
              "healthmonitor": {"id": "hm1", "name": "http-check", "provisioning_status": "ACTIVE", "operating_status": "ONLINE"},
              "members": [
                {"id": "me1", "name": "web-1", "provisioning_status": "ACTIVE", "operating_status": "ONLINE"},
                {"id": "me2", "name": "web-2", "provisioning_status": "ACTIVE", "operating_status": "ERROR"}
              ]
            }]
          }]
        }}}`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runLBStatusShow(context.Background(), lbClient(fakeServer), o, "lb1", &buf); err != nil {
		t.Fatalf("runLBStatusShow error: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"loadbalancer", "web-lb", "DEGRADED",
		"listener", "http",
		"pool", "web-pool",
		"healthmonitor", "http-check",
		"member", "web-1", "web-2", "ERROR",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("flattened status output missing %q\n---\n%s", want, out)
		}
	}
}

// fastPolling shrinks the --wait poll interval so tests do not sleep for seconds.
func fastPolling(t *testing.T) {
	t.Helper()
	old := provisioningPollInterval
	provisioningPollInterval = time.Millisecond
	t.Cleanup(func() { provisioningPollInterval = old })
}

// A --wait that times out must still put the load balancer on stdout: octavia
// accepted the create, so the resource exists, and if its ID lives only inside
// the error string the operator has to scrape it out to clean up.
func TestRunLBCreate_WaitTimeoutStillRendersTheLoadBalancer(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	defer func(prev time.Duration) { provisioningPollInterval = prev }(provisioningPollInterval)
	provisioningPollInterval = time.Millisecond

	fakeServer.Mux.HandleFunc("/v2.0/lbaas/loadbalancers/", func(w http.ResponseWriter, r *http.Request) {
		// Every poll reports PENDING_CREATE, so the wait can only time out.
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"loadbalancer": {"id": "lb-1", "name": "web",
          "provisioning_status": "PENDING_CREATE", "operating_status": "OFFLINE"}}`))
	})
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/loadbalancers", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"loadbalancers": []}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"loadbalancer": {"id": "lb-1", "name": "web",
          "vip_subnet_id": "subnet-1", "provisioning_status": "PENDING_CREATE",
          "operating_status": "OFFLINE"}}`))
	})

	f := &lbCreateFlags{wait: true, waitTimeout: 20 * time.Millisecond}
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runLBCreate(context.Background(), lbClient(fakeServer), o, "web", f,
		resolvedLBRefs{vipSubnetID: "subnet-1"}, &buf)
	if err == nil {
		t.Fatal("expected the wait to time out, got nil")
	}
	// The last observed status distinguishes "octavia is slow" from "koc stopped
	// watching too early".
	if !strings.Contains(err.Error(), "PENDING_CREATE") {
		t.Errorf("timeout error should carry the last provisioning_status, got: %v", err)
	}
	if !strings.Contains(buf.String(), "lb-1") {
		t.Errorf("the created load balancer must reach stdout so its ID is recoverable\n---\n%s", buf.String())
	}
}

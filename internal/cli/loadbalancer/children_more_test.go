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

// --- listener list/show/delete ---------------------------------------------

func TestRunListenerList_FiltersByLoadBalancerAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubLBList(fakeServer)
	var gotQuery string
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/listeners", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		gotQuery = r.URL.RawQuery
		th.TestFormValues(t, r, map[string]string{
			"loadbalancer_id": "lb1", "protocol": "HTTP", "admin_state_up": "true",
		})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"listeners": [{
          "id": "li1", "name": "http", "protocol": "HTTP", "protocol_port": 80,
          "default_pool_id": "po1", "operating_status": "ONLINE",
          "provisioning_status": "ACTIVE", "admin_state_up": true,
          "connection_limit": 100, "project_id": "proj1"
        }]}`))
	})

	up := true
	f := &listenerListFlags{loadBalancer: "lb1", protocol: "HTTP", long: true, adminStateUp: &up}
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runListenerList(context.Background(), lbClient(fakeServer), o, f, "", &buf); err != nil {
		t.Fatalf("runListenerList error: %v", err)
	}
	if gotQuery == "" {
		t.Fatal("expected a filtered query string")
	}
	for _, want := range []string{"li1", "http", "ONLINE", "ACTIVE", "proj1"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q\n---\n%s", want, buf.String())
		}
	}
}

func TestRunListenerShow_RendersFields(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubEmptyList(fakeServer, "/v2.0/lbaas/listeners", "listeners")
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/listeners/li1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"listener": {"id": "li1", "name": "http", "protocol": "HTTP"}}`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runListenerShow(context.Background(), lbClient(fakeServer), o, "li1", &buf); err != nil {
		t.Fatalf("runListenerShow error: %v", err)
	}
	if !strings.Contains(buf.String(), "li1") {
		t.Errorf("output missing listener ID\n---\n%s", buf.String())
	}
}

func TestRunListenerShow_WrapsNotFoundError(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubEmptyList(fakeServer, "/v2.0/lbaas/listeners", "listeners")
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/listeners/missing", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runListenerShow(context.Background(), lbClient(fakeServer), o, "missing", &buf)
	if err == nil || !strings.Contains(err.Error(), "showing listener") {
		t.Fatalf("expected a wrapped 'showing listener' error, got %v", err)
	}
}

func TestRunListenerDelete_AggregatesFailures(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubEmptyList(fakeServer, "/v2.0/lbaas/listeners", "listeners")
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/listeners/li1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodDelete)
		w.WriteHeader(http.StatusNoContent)
	})
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/listeners/li2", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
	})

	var buf bytes.Buffer
	err := runListenerDelete(context.Background(), lbClient(fakeServer), []string{"li1", "li2"}, &buf)
	if err == nil {
		t.Fatal("expected a joined error for the failing listener")
	}
	if !strings.Contains(err.Error(), "li2") {
		t.Errorf("error should name the failing listener: %v", err)
	}
	if !strings.Contains(buf.String(), "li1") {
		t.Errorf("output should still confirm deletion of the listener that succeeded\n---\n%s", buf.String())
	}
}

// --- pool list/show/set/delete ----------------------------------------------

func TestRunPoolList_ScopedToLoadBalancer(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubLBList(fakeServer)
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/pools", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		th.TestFormValues(t, r, map[string]string{"loadbalancer_id": "lb1", "lb_algorithm": "ROUND_ROBIN"})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"pools": [{
          "id": "po1", "name": "web-pool", "protocol": "HTTP", "lb_algorithm": "ROUND_ROBIN",
          "members": [{"id": "me1"}], "operating_status": "ONLINE"
        }]}`))
	})

	f := &poolListFlags{loadBalancer: "lb1", lbAlgorithm: "ROUND_ROBIN"}
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runPoolList(context.Background(), lbClient(fakeServer), o, f, "", &buf); err != nil {
		t.Fatalf("runPoolList error: %v", err)
	}
	if !strings.Contains(buf.String(), "po1") {
		t.Errorf("output missing the pool ID\n---\n%s", buf.String())
	}
}

func TestRunPoolShow_RendersFields(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubEmptyList(fakeServer, "/v2.0/lbaas/pools", "pools")
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/pools/po1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"pool": {"id": "po1", "name": "web-pool", "protocol": "HTTP"}}`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runPoolShow(context.Background(), lbClient(fakeServer), o, "po1", &buf); err != nil {
		t.Fatalf("runPoolShow error: %v", err)
	}
	if !strings.Contains(buf.String(), "po1") {
		t.Errorf("output missing the pool ID\n---\n%s", buf.String())
	}
}

// A pool "set" must send only the attributes actually flagged: the algorithm
// and session persistence here, none of the untouched TLS/name fields.
func TestRunPoolSet_OnlySendsGivenAttributes(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubEmptyList(fakeServer, "/v2.0/lbaas/pools", "pools")
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/pools/po1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"pool": {
          "lb_algorithm": "LEAST_CONNECTIONS",
          "session_persistence": {"type": "SOURCE_IP"}
        }}`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"pool": {"id": "po1", "lb_algorithm": "LEAST_CONNECTIONS"}}`))
	})

	f := &poolWriteFlags{
		lbAlgorithm:        "LEAST_CONNECTIONS",
		sessionPersistence: []string{"type=SOURCE_IP"},
		name:               "ignored", tlsEnabled: true,
	}
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	changed := changedSet{flagLBAlgorithm: true, "session-persistence": true}
	if err := runPoolSet(context.Background(), lbClient(fakeServer), o, "po1", f, changed, &buf); err != nil {
		t.Fatalf("runPoolSet error: %v", err)
	}
}

// --disable-tls must send an explicit tls_enabled:false, not omit the field.
func TestRunPoolSet_DisableTLSSendsExplicitFalse(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubEmptyList(fakeServer, "/v2.0/lbaas/pools", "pools")
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/pools/po1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"pool": {"tls_enabled": false}}`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"pool": {"id": "po1"}}`))
	})

	f := &poolWriteFlags{noTLS: true}
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runPoolSet(context.Background(), lbClient(fakeServer), o, "po1", f, changedSet{flagDisableTLS: true}, &buf)
	if err != nil {
		t.Fatalf("runPoolSet error: %v", err)
	}
}

func TestRunPoolSet_RejectsEmptyUpdate(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runPoolSet(context.Background(), lbClient(fakeServer), o, "po1", &poolWriteFlags{}, changedSet{}, &buf)
	if err == nil || !strings.Contains(err.Error(), "nothing to set") {
		t.Fatalf("expected a 'nothing to set' error, got %v", err)
	}
}

func TestRunPoolDelete_Request(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubEmptyList(fakeServer, "/v2.0/lbaas/pools", "pools")
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/pools/po1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodDelete)
		w.WriteHeader(http.StatusNoContent)
	})

	var buf bytes.Buffer
	if err := runPoolDelete(context.Background(), lbClient(fakeServer), []string{"po1"}, &buf); err != nil {
		t.Fatalf("runPoolDelete error: %v", err)
	}
	if !strings.Contains(buf.String(), "po1") {
		t.Errorf("output missing the deleted pool ref\n---\n%s", buf.String())
	}
}

// --- member show/set/delete --------------------------------------------------

func TestRunMemberShow_RendersFields(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubEmptyList(fakeServer, "/v2.0/lbaas/pools", "pools")
	stubEmptyList(fakeServer, "/v2.0/lbaas/pools/po1/members", "members")
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/pools/po1/members/me1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"member": {"id": "me1", "address": "192.0.2.10", "protocol_port": 80}}`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runMemberShow(context.Background(), lbClient(fakeServer), o, "po1", "me1", &buf); err != nil {
		t.Fatalf("runMemberShow error: %v", err)
	}
	if !strings.Contains(buf.String(), "192.0.2.10") {
		t.Errorf("output missing the member address\n---\n%s", buf.String())
	}
}

// A member "set" must send only the flagged attributes, including an explicit
// --disable-backup as backup:false rather than omitting the field.
func TestRunMemberSet_OnlySendsGivenAttributes(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubEmptyList(fakeServer, "/v2.0/lbaas/pools", "pools")
	stubEmptyList(fakeServer, "/v2.0/lbaas/pools/po1/members", "members")
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/pools/po1/members/me1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"member": {"weight": 50, "monitor_port": 8080}}`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"member": {"id": "me1", "weight": 50}}`))
	})

	f := &memberWriteFlags{
		weight: 50, monitorPort: 8080, name: "ignored",
		changed: changedSet{"weight": true, "monitor-port": true},
	}
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runMemberSet(context.Background(), lbClient(fakeServer), o, "po1", "me1", f, &buf)
	if err != nil {
		t.Fatalf("runMemberSet error: %v", err)
	}
}

func TestRunMemberSet_DisableBackupSendsExplicitFalse(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubEmptyList(fakeServer, "/v2.0/lbaas/pools", "pools")
	stubEmptyList(fakeServer, "/v2.0/lbaas/pools/po1/members", "members")
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/pools/po1/members/me1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"member": {"backup": false}}`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"member": {"id": "me1"}}`))
	})

	f := &memberWriteFlags{noBackup: true, changed: changedSet{flagDisableBackup: true}}
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runMemberSet(context.Background(), lbClient(fakeServer), o, "po1", "me1", f, &buf)
	if err != nil {
		t.Fatalf("runMemberSet error: %v", err)
	}
}

func TestRunMemberSet_RejectsEmptyUpdate(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	f := &memberWriteFlags{changed: changedSet{}}
	err := runMemberSet(context.Background(), lbClient(fakeServer), o, "po1", "me1", f, &buf)
	if err == nil || !strings.Contains(err.Error(), "nothing to set") {
		t.Fatalf("expected a 'nothing to set' error, got %v", err)
	}
}

func TestRunMemberDelete_ScopedToPool(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubEmptyList(fakeServer, "/v2.0/lbaas/pools", "pools")
	stubEmptyList(fakeServer, "/v2.0/lbaas/pools/po1/members", "members")
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/pools/po1/members/me1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodDelete)
		w.WriteHeader(http.StatusNoContent)
	})

	var buf bytes.Buffer
	err := runMemberDelete(context.Background(), lbClient(fakeServer), "po1", []string{"me1"}, &buf)
	if err != nil {
		t.Fatalf("runMemberDelete error: %v", err)
	}
	if !strings.Contains(buf.String(), "po1") || !strings.Contains(buf.String(), "me1") {
		t.Errorf("output should name both the member and its pool\n---\n%s", buf.String())
	}
}

// --- health monitor list/show/delete ----------------------------------------

func TestRunHealthMonitorList_FiltersByPool(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubEmptyList(fakeServer, "/v2.0/lbaas/pools", "pools")
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/healthmonitors", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		th.TestFormValues(t, r, map[string]string{"pool_id": "po1", "type": "HTTP"})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"healthmonitors": [{
          "id": "hm1", "name": "http-check", "type": "HTTP", "delay": 5, "timeout": 3,
          "max_retries": 3, "operating_status": "ONLINE"
        }]}`))
	})

	f := &healthMonitorListFlags{pool: "po1", typ: "HTTP"}
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runHealthMonitorList(context.Background(), lbClient(fakeServer), o, f, "", &buf); err != nil {
		t.Fatalf("runHealthMonitorList error: %v", err)
	}
	if !strings.Contains(buf.String(), "hm1") {
		t.Errorf("output missing the monitor ID\n---\n%s", buf.String())
	}
}

func TestRunHealthMonitorShow_RendersFields(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubEmptyList(fakeServer, "/v2.0/lbaas/healthmonitors", "healthmonitors")
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/healthmonitors/hm1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"healthmonitor": {"id": "hm1", "name": "http-check", "type": "HTTP"}}`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runHealthMonitorShow(context.Background(), lbClient(fakeServer), o, "hm1", &buf); err != nil {
		t.Fatalf("runHealthMonitorShow error: %v", err)
	}
	if !strings.Contains(buf.String(), "hm1") {
		t.Errorf("output missing the monitor ID\n---\n%s", buf.String())
	}
}

func TestRunHealthMonitorDelete_Request(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubEmptyList(fakeServer, "/v2.0/lbaas/healthmonitors", "healthmonitors")
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/healthmonitors/hm1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodDelete)
		w.WriteHeader(http.StatusNoContent)
	})

	var buf bytes.Buffer
	if err := runHealthMonitorDelete(context.Background(), lbClient(fakeServer), []string{"hm1"}, &buf); err != nil {
		t.Fatalf("runHealthMonitorDelete error: %v", err)
	}
	if !strings.Contains(buf.String(), "hm1") {
		t.Errorf("output missing the deleted monitor ref\n---\n%s", buf.String())
	}
}

// --- l7policy list/show/set/delete -------------------------------------------

func TestRunL7PolicyList_FiltersByListener(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubEmptyList(fakeServer, "/v2.0/lbaas/listeners", "listeners")
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/l7policies", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		th.TestFormValues(t, r, map[string]string{"listener_id": "li1", "action": "REJECT"})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"l7policies": [{
          "id": "l7p1", "name": "reject-all", "action": "REJECT", "listener_id": "li1",
          "operating_status": "ONLINE"
        }]}`))
	})

	f := &l7PolicyListFlags{listener: "li1", action: "REJECT"}
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runL7PolicyList(context.Background(), lbClient(fakeServer), o, f, "", &buf); err != nil {
		t.Fatalf("runL7PolicyList error: %v", err)
	}
	if !strings.Contains(buf.String(), "l7p1") {
		t.Errorf("output missing the policy ID\n---\n%s", buf.String())
	}
}

func TestRunL7PolicyShow_RendersFields(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubEmptyList(fakeServer, "/v2.0/lbaas/l7policies", "l7policies")
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/l7policies/l7p1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"l7policy": {"id": "l7p1", "name": "reject-all", "action": "REJECT"}}`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runL7PolicyShow(context.Background(), lbClient(fakeServer), o, "l7p1", &buf); err != nil {
		t.Fatalf("runL7PolicyShow error: %v", err)
	}
	if !strings.Contains(buf.String(), "l7p1") {
		t.Errorf("output missing the policy ID\n---\n%s", buf.String())
	}
}

// Switching a policy to REDIRECT_TO_POOL must resolve the new pool and send
// only the redirect target plus whatever else was flagged.
func TestRunL7PolicySet_RedirectPoolAndTags(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubEmptyList(fakeServer, "/v2.0/lbaas/l7policies", "l7policies")
	stubEmptyList(fakeServer, "/v2.0/lbaas/pools", "pools")
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/l7policies/l7p1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"l7policy": {
          "action": "REDIRECT_TO_POOL", "redirect_pool_id": "po1", "tags": ["blue"]
        }}`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"l7policy": {"id": "l7p1", "action": "REDIRECT_TO_POOL"}}`))
	})

	f := &l7PolicyWriteFlags{action: "REDIRECT_TO_POOL", redirectPool: "po1", tag: []string{"blue"}}
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	changed := changedSet{"action": true, "redirect-pool": true, "tag": true}
	err := runL7PolicySet(context.Background(), lbClient(fakeServer), o, "l7p1", f, changed, &buf)
	if err != nil {
		t.Fatalf("runL7PolicySet error: %v", err)
	}
}

func TestRunL7PolicySet_NoTagClearsTags(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubEmptyList(fakeServer, "/v2.0/lbaas/l7policies", "l7policies")
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/l7policies/l7p1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"l7policy": {"tags": []}}`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"l7policy": {"id": "l7p1"}}`))
	})

	f := &l7PolicyWriteFlags{noTag: true}
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runL7PolicySet(context.Background(), lbClient(fakeServer), o, "l7p1", f, changedSet{}, &buf)
	if err != nil {
		t.Fatalf("runL7PolicySet error: %v", err)
	}
}

func TestRunL7PolicySet_RejectsEmptyUpdate(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	f := &l7PolicyWriteFlags{}
	err := runL7PolicySet(context.Background(), lbClient(fakeServer), o, "l7p1", f, changedSet{}, &buf)
	if err == nil || !strings.Contains(err.Error(), "nothing to set") {
		t.Fatalf("expected a 'nothing to set' error, got %v", err)
	}
}

func TestRunL7PolicyDelete_Request(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubEmptyList(fakeServer, "/v2.0/lbaas/l7policies", "l7policies")
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/l7policies/l7p1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodDelete)
		w.WriteHeader(http.StatusNoContent)
	})

	var buf bytes.Buffer
	if err := runL7PolicyDelete(context.Background(), lbClient(fakeServer), []string{"l7p1"}, &buf); err != nil {
		t.Fatalf("runL7PolicyDelete error: %v", err)
	}
	if !strings.Contains(buf.String(), "l7p1") {
		t.Errorf("output missing the deleted policy ref\n---\n%s", buf.String())
	}
}

// --- l7rule list/show/set/delete ---------------------------------------------

func TestRunL7RuleList_FiltersByTypeAndCompare(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubEmptyList(fakeServer, "/v2.0/lbaas/l7policies", "l7policies")
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/l7policies/l7p1/rules", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		th.TestFormValues(t, r, map[string]string{"type": "PATH", "compare_type": "STARTS_WITH"})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"rules": [{
          "id": "r1", "type": "PATH", "compare_type": "STARTS_WITH", "value": "/api",
          "operating_status": "ONLINE"
        }]}`))
	})

	f := &l7RuleListFlags{typ: "PATH", compareType: "STARTS_WITH"}
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runL7RuleList(context.Background(), lbClient(fakeServer), o, "l7p1", f, &buf); err != nil {
		t.Fatalf("runL7RuleList error: %v", err)
	}
	if !strings.Contains(buf.String(), "r1") {
		t.Errorf("output missing the rule ID\n---\n%s", buf.String())
	}
}

func TestRunL7RuleShow_RendersFields(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubEmptyList(fakeServer, "/v2.0/lbaas/l7policies", "l7policies")
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/l7policies/l7p1/rules/r1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"rule": {"id": "r1", "type": "PATH", "value": "/api"}}`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runL7RuleShow(context.Background(), lbClient(fakeServer), o, "l7p1", "r1", &buf); err != nil {
		t.Fatalf("runL7RuleShow error: %v", err)
	}
	if !strings.Contains(buf.String(), "r1") {
		t.Errorf("output missing the rule ID\n---\n%s", buf.String())
	}
}

// A rule "set" must send only the flagged attributes; --invert must be sent
// as invert:true even though the rule's own default is false.
func TestRunL7RuleSet_InvertAndKeyValue(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubEmptyList(fakeServer, "/v2.0/lbaas/l7policies", "l7policies")
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/l7policies/l7p1/rules/r1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"rule": {"key": "X-Trace", "invert": true}}`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"rule": {"id": "r1", "key": "X-Trace"}}`))
	})

	f := &l7RuleWriteFlags{key: "X-Trace", invert: true, changed: changedSet{"key": true, flagInvert: true}}
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runL7RuleSet(context.Background(), lbClient(fakeServer), o, "l7p1", "r1", f, &buf)
	if err != nil {
		t.Fatalf("runL7RuleSet error: %v", err)
	}
}

func TestRunL7RuleSet_NoInvertSendsExplicitFalse(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubEmptyList(fakeServer, "/v2.0/lbaas/l7policies", "l7policies")
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/l7policies/l7p1/rules/r1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"rule": {"invert": false}}`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"rule": {"id": "r1"}}`))
	})

	f := &l7RuleWriteFlags{noInvert: true, changed: changedSet{flagNoInvert: true}}
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runL7RuleSet(context.Background(), lbClient(fakeServer), o, "l7p1", "r1", f, &buf)
	if err != nil {
		t.Fatalf("runL7RuleSet error: %v", err)
	}
}

func TestRunL7RuleSet_RejectsEmptyUpdate(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	f := &l7RuleWriteFlags{changed: changedSet{}}
	err := runL7RuleSet(context.Background(), lbClient(fakeServer), o, "l7p1", "r1", f, &buf)
	if err == nil || !strings.Contains(err.Error(), "nothing to set") {
		t.Fatalf("expected a 'nothing to set' error, got %v", err)
	}
}

func TestRunL7RuleDelete_Request(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubEmptyList(fakeServer, "/v2.0/lbaas/l7policies", "l7policies")
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/l7policies/l7p1/rules/r1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodDelete)
		w.WriteHeader(http.StatusNoContent)
	})

	var buf bytes.Buffer
	err := runL7RuleDelete(context.Background(), lbClient(fakeServer), "l7p1", []string{"r1"}, &buf)
	if err != nil {
		t.Fatalf("runL7RuleDelete error: %v", err)
	}
	if !strings.Contains(buf.String(), "r1") {
		t.Errorf("output missing the deleted rule ref\n---\n%s", buf.String())
	}
}

package loadbalancer

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"
	"github.com/spf13/pflag"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// An unset sends an explicit null per cleared attribute, and removing tags
// reads the current list first and writes back the rest, as upstream does.
func TestRunUnset_LoadBalancerNullsAndTagRemoval(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var methods []string
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/loadbalancers/lb1", func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"loadbalancer": {"id": "lb1", "tags": ["prod", "web", "blue"]}}`))
		case http.MethodPut:
			th.TestJSONRequest(t, r, `{"loadbalancer": {"name": null, "vip_qos_policy_id": null, "tags": ["prod"]}}`)
			_, _ = w.Write([]byte(`{"loadbalancer": ` + lbBody + `}`))
		}
	})

	o := &output.Options{Format: output.FormatValue, Columns: []string{"name"}}
	var buf bytes.Buffer
	target := unsetTarget{path: []string{"lbaas", "loadbalancers", "lb1"}, ref: "web-lb"}
	req := unsetRequest{attrs: []string{"name", "vip_qos_policy_id"}, removeTags: []string{"web", "blue"}}
	if err := runUnset(context.Background(), lbClient(fakeServer), o, lbUnsetSpec, target, req, &buf); err != nil {
		t.Fatalf("runUnset: %v", err)
	}
	if strings.Join(methods, ",") != "GET,PUT" {
		t.Errorf("requests = %v, want GET then PUT", methods)
	}
	if got := strings.TrimSpace(buf.String()); got != "web-lb" {
		t.Errorf("rendered name = %q, want the PUT response's record", got)
	}
}

// --all-tag clears the list without reading it, and --wait polls the child's
// own provisioning_status, rendering the settled record.
func TestRunUnset_MemberAllTagAndWait(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fastPolling(t)

	gets := 0
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/pools/p1/members/m1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodPut:
			th.TestJSONRequest(t, r, `{"member": {"weight": null, "backup": null, "tags": []}}`)
			_, _ = w.Write([]byte(`{"member": {"id": "m1", "weight": 3, "provisioning_status": "PENDING_UPDATE"}}`))
		case http.MethodGet:
			gets++
			status := "PENDING_UPDATE"
			if gets == 2 {
				status = "ACTIVE"
			}
			_, _ = w.Write([]byte(`{"member": {"id": "m1", "weight": 1, "provisioning_status": "` + status + `"}}`))
		}
	})

	o := &output.Options{Format: output.FormatValue, Columns: []string{"weight", "provisioning_status"}}
	var buf bytes.Buffer
	target := unsetTarget{path: []string{"lbaas", "pools", "p1", "members", "m1"}, ref: "m1"}
	req := unsetRequest{attrs: []string{"weight", "backup"}, allTags: true, removeTags: []string{"ignored"}, wait: true}
	if err := runUnset(context.Background(), lbClient(fakeServer), o, memberUnsetSpec, target, req, &buf); err != nil {
		t.Fatalf("runUnset: %v", err)
	}
	if gets != 2 {
		t.Errorf("polled %d times, want 2", gets)
	}
	if got := strings.Fields(buf.String()); strings.Join(got, " ") != "1 ACTIVE" {
		t.Errorf("rendered %q, want the settled record", buf.String())
	}
}

func TestRunUnset_WaitFailsFastOnError(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fastPolling(t)

	fakeServer.Mux.HandleFunc("/v2.0/lbaas/l7policies/pol1/rules/r1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPut {
			th.TestJSONRequest(t, r, `{"rule": {"key": null}}`)
		}
		_, _ = w.Write([]byte(`{"rule": {"id": "r1", "provisioning_status": "ERROR"}}`))
	})

	o := &output.Options{Format: output.FormatTable}
	target := unsetTarget{path: []string{"lbaas", "l7policies", "pol1", "rules", "r1"}, ref: "r1"}
	err := runUnset(context.Background(), lbClient(fakeServer), o, l7RuleUnsetSpec, target,
		unsetRequest{attrs: []string{"key"}, wait: true}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), `waiting for l7rule "r1": entered ERROR`) {
		t.Fatalf("err = %v, want the ERROR named", err)
	}
}

func TestRunUnset_RejectsEmptyRequest(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	o := &output.Options{Format: output.FormatTable}
	err := runUnset(context.Background(), lbClient(fakeServer), o, flavorUnsetSpec,
		unsetTarget{path: []string{"lbaas", "flavors", "f1"}, ref: "f1"}, unsetRequest{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "nothing to unset") {
		t.Fatalf("err = %v, want nothing to unset", err)
	}
}

func TestRunUnset_FlavorDescription(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/v2.0/lbaas/flavors/f1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"flavor": {"description": null}}`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"flavor": {"id": "f1", "name": "gold", "description": "", "enabled": true}}`))
	})

	o := &output.Options{Format: output.FormatValue, Columns: []string{"name"}}
	var buf bytes.Buffer
	err := runUnset(context.Background(), lbClient(fakeServer), o, flavorUnsetSpec,
		unsetTarget{path: []string{"lbaas", "flavors", "f1"}, ref: "gold"}, unsetRequest{attrs: []string{"description"}}, &buf)
	if err != nil {
		t.Fatalf("runUnset: %v", err)
	}
	if strings.TrimSpace(buf.String()) != "gold" {
		t.Errorf("output = %q", buf.String())
	}
}

// Every unset verb registers exactly upstream's flags (python-octaviaclient
// 3.15.0's Unset* parsers), plus --wait-timeout wherever --wait is.
func TestUnsetVerbs_UpstreamFlags(t *testing.T) {
	root := NewCommand(nil, &output.Options{})
	want := map[string][]string{
		"unset":                  {"name", "description", "vip-qos-policy-id", "vip-sg-id", "tag", "all-tag", "wait"},
		"listener unset":         {"name", "description", "connection-limit", "default-pool", "default-tls-container-ref", "sni-container-refs", "insert-headers", "timeout-client-data", "timeout-member-connect", "timeout-member-data", "timeout-tcp-inspect", "client-ca-tls-container-ref", "client-authentication", "client-crl-container-ref", "allowed-cidrs", "tls-versions", "tls-ciphers", "alpn-protocols", "hsts-max-age", "hsts-include-subdomains", "hsts-preload", "tag", "all-tag", "wait"},
		"pool unset":             {"name", "description", "ca-tls-container-ref", "crl-container-ref", "session-persistence", "tls-container-ref", "tls-versions", "tls-ciphers", "alpn-protocols", "tag", "all-tag", "wait"},
		"member unset":           {"backup", "monitor-address", "monitor-port", "name", "weight", "tag", "all-tag", "wait"},
		"healthmonitor unset":    {"domain-name", "expected-codes", "http-method", "http-version", "max-retries-down", "name", "url-path", "tag", "all-tag", "wait"},
		"l7policy unset":         {"description", "name", "redirect-http-code", "tag", "all-tag", "wait"},
		"l7rule unset":           {"invert", "key", "tag", "all-tag", "wait"},
		"flavor unset":           {"description"},
		"availabilityzone unset": {"description"},
	}
	for path, flags := range want {
		cmd, _, err := root.Find(strings.Fields(path))
		if err != nil || cmd.Name() != "unset" {
			t.Errorf("loadbalancer %s: not found (%v)", path, err)
			continue
		}
		got := map[string]bool{}
		cmd.Flags().VisitAll(func(f *pflag.Flag) { got[f.Name] = true })
		for _, f := range flags {
			if !got[f] {
				t.Errorf("loadbalancer %s lacks upstream's --%s", path, f)
			}
			delete(got, f)
		}
		delete(got, flagWaitTimeout)
		for f := range got {
			t.Errorf("loadbalancer %s registers --%s, which upstream's parser does not define", path, f)
		}
	}
}

func TestRunListenerStatsShow(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/v2.0/lbaas/listeners/l1/stats", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"stats": {"active_connections": 2, "bytes_in": 10, "bytes_out": 20, "request_errors": 1, "total_connections": 7}}`))
	})

	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	if err := runListenerStatsShow(context.Background(), lbClient(fakeServer), o, "8ef2a3f0-0000-4000-8000-000000000001", &buf); err == nil {
		t.Fatal("a UUID that is not on the mux should 404")
	}
	buf.Reset()
	// A non-UUID reference is looked up by name first.
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/listeners", func(w http.ResponseWriter, r *http.Request) {
		th.TestFormValues(t, r, map[string]string{"name": "http"})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"listeners": [{"id": "l1", "name": "http"}]}`))
	})
	if err := runListenerStatsShow(context.Background(), lbClient(fakeServer), o, "http", &buf); err != nil {
		t.Fatalf("runListenerStatsShow: %v", err)
	}
	if got := strings.Fields(buf.String()); strings.Join(got, " ") != "2 10 20 1 7" {
		t.Errorf("output = %q", buf.String())
	}
}

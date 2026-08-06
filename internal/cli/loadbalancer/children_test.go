package loadbalancer

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2/openstack/loadbalancer/v2/pools"
	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// stubEmptyList registers an octavia collection endpoint that answers an empty
// list, so the name→ID resolvers fall through to treating a reference as an ID.
func stubEmptyList(fakeServer th.FakeServer, path, key string) {
	fakeServer.Mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"` + key + `": []}`))
	})
}

func TestRunListenerCreate_RequestBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubLBList(fakeServer)
	stubEmptyList(fakeServer, "/v2.0/lbaas/pools", "pools")
	var gotMethod string
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/listeners", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"listeners": []}`))
			return
		}
		gotMethod = r.Method
		// connection_limit and the timeouts are pointers: 0 means "not given", so
		// only the ones actually set appear.
		th.TestJSONRequest(t, r, `{"listener": {
          "name": "http",
          "loadbalancer_id": "lb1",
          "protocol": "HTTP",
          "protocol_port": 80,
          "connection_limit": 2000,
          "insert_headers": {"X-Forwarded-For": "true"},
          "admin_state_up": true
        }}`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"listener": {
          "id": "li1", "name": "http", "protocol": "HTTP", "protocol_port": 80,
          "provisioning_status": "PENDING_CREATE", "operating_status": "OFFLINE"
        }}`))
	})

	up := true
	f := &listenerWriteFlags{
		loadBalancer:    "lb1",
		protocol:        "HTTP",
		protocolPort:    80,
		connectionLimit: 2000,
		insertHeader:    []string{"X-Forwarded-For=true"},
		adminStateUp:    &up,
	}
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runListenerCreate(context.Background(), lbClient(fakeServer), o, "http", f, "", &buf); err != nil {
		t.Fatalf("runListenerCreate error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if !strings.Contains(buf.String(), "li1") {
		t.Errorf("output missing the new listener ID\n---\n%s", buf.String())
	}
}

// A listener "set" must send only what was named; the timeouts and connection
// limit share the same sparse treatment as the strings.
func TestRunListenerSet_OnlySendsGivenAttributes(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubEmptyList(fakeServer, "/v2.0/lbaas/listeners", "listeners")
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/listeners/li1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"listener": {"connection_limit": 5000}}`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"listener": {"id": "li1", "name": "http", "connection_limit": 5000}}`))
	})

	// description and the other timeouts are populated but not flagged.
	f := &listenerWriteFlags{connectionLimit: 5000, description: "ignored", timeoutClientData: 999}
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runListenerSet(context.Background(), lbClient(fakeServer), o, "li1", f,
		changedSet{"connection-limit": true}, &buf)
	if err != nil {
		t.Fatalf("runListenerSet error: %v", err)
	}
}

func TestRunListenerSet_RejectsEmptyUpdate(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runListenerSet(context.Background(), lbClient(fakeServer), o, "li1", &listenerWriteFlags{}, changedSet{}, &buf)
	if err == nil || !strings.Contains(err.Error(), "nothing to set") {
		t.Fatalf("expected a 'nothing to set' error, got %v", err)
	}
}

func TestRunPoolCreate_RequestBodyWithSessionPersistence(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubLBList(fakeServer)
	var gotMethod string
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/pools", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"pools": []}`))
			return
		}
		gotMethod = r.Method
		th.TestJSONRequest(t, r, `{"pool": {
          "name": "web-pool",
          "protocol": "HTTP",
          "lb_algorithm": "ROUND_ROBIN",
          "loadbalancer_id": "lb1",
          "session_persistence": {"type": "APP_COOKIE", "cookie_name": "sid"}
        }}`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"pool": {"id": "po1", "name": "web-pool", "protocol": "HTTP", "lb_algorithm": "ROUND_ROBIN"}}`))
	})

	f := &poolWriteFlags{
		loadBalancer:       "lb1",
		protocol:           "HTTP",
		lbAlgorithm:        "ROUND_ROBIN",
		sessionPersistence: []string{"type=APP_COOKIE,cookie_name=sid"},
	}
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runPoolCreate(context.Background(), lbClient(fakeServer), o, "web-pool", f, "", &buf); err != nil {
		t.Fatalf("runPoolCreate error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
}

func TestParseSessionPersistence(t *testing.T) {
	tests := []struct {
		name    string
		spec    string
		want    pools.SessionPersistence
		wantErr string
	}{
		{
			name: "source ip", spec: "type=SOURCE_IP",
			want: pools.SessionPersistence{Type: "SOURCE_IP"},
		},
		{
			name: "app cookie", spec: "type=APP_COOKIE,cookie_name=sid",
			want: pools.SessionPersistence{Type: "APP_COOKIE", CookieName: "sid"},
		},
		{
			name: "hyphenated key", spec: "type=APP_COOKIE,cookie-name=sid",
			want: pools.SessionPersistence{Type: "APP_COOKIE", CookieName: "sid"},
		},
		// octavia rejects APP_COOKIE without a name; catching it here gives a better
		// message than relaying a 400.
		{name: "app cookie without a name", spec: "type=APP_COOKIE", wantErr: "requires cookie_name"},
		{name: "no type", spec: "cookie_name=sid", wantErr: "requires type="},
		{name: "unknown key", spec: "type=SOURCE_IP,ttl=60", wantErr: "unknown key"},
		{name: "not key=value", spec: "SOURCE_IP", wantErr: "expected key=value"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseSessionPersistence([]string{tc.spec})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got == nil || *got != tc.want {
				t.Errorf("parseSessionPersistence() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// Weight 0 takes a member out of rotation without removing it, so an explicit
// --weight 0 must be sent rather than dropped as "unset".
func TestRunMemberCreate_ExplicitZeroWeightIsSent(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubEmptyList(fakeServer, "/v2.0/lbaas/pools", "pools")
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/pools/po1/members", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"members": []}`))
			return
		}
		th.TestJSONRequest(t, r, `{"member": {
          "name": "web-1", "address": "10.0.0.21", "protocol_port": 80, "weight": 0
        }}`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"member": {"id": "me1", "name": "web-1", "address": "10.0.0.21", "protocol_port": 80, "weight": 0}}`))
	})

	f := &memberWriteFlags{address: "10.0.0.21", protocolPort: 80, weight: 0}
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runMemberCreate(context.Background(), lbClient(fakeServer), o, "po1", "web-1", f,
		resolvedLBRefs{}, changedSet{"weight": true}, &buf)
	if err != nil {
		t.Fatalf("runMemberCreate error: %v", err)
	}
	if !strings.Contains(buf.String(), "me1") {
		t.Errorf("output missing the new member ID\n---\n%s", buf.String())
	}
}

// Without the flag, weight must be absent so octavia's own default (1) applies.
func TestRunMemberCreate_UnsetWeightIsOmitted(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubEmptyList(fakeServer, "/v2.0/lbaas/pools", "pools")
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/pools/po1/members", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"members": []}`))
			return
		}
		th.TestJSONRequest(t, r, `{"member": {"name": "web-1", "address": "10.0.0.21", "protocol_port": 80}}`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"member": {"id": "me1", "name": "web-1"}}`))
	})

	f := &memberWriteFlags{address: "10.0.0.21", protocolPort: 80}
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runMemberCreate(context.Background(), lbClient(fakeServer), o, "po1", "web-1", f,
		resolvedLBRefs{}, changedSet{}, &buf)
	if err != nil {
		t.Fatalf("runMemberCreate error: %v", err)
	}
}

func TestRunMemberList_ScopedToThePool(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubEmptyList(fakeServer, "/v2.0/lbaas/pools", "pools")
	var gotPath string
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/pools/po1/members", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		th.TestMethod(t, r, http.MethodGet)
		th.TestFormValues(t, r, map[string]string{"address": "10.0.0.21"})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"members": [{
          "id": "me1", "name": "web-1", "address": "10.0.0.21", "protocol_port": 80,
          "weight": 1, "operating_status": "ONLINE", "provisioning_status": "ACTIVE",
          "backup": false, "subnet_id": "sub-1"
        }]}`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	f := &memberListFlags{address: "10.0.0.21", long: true}
	if err := runMemberList(context.Background(), lbClient(fakeServer), o, "po1", f, "", &buf); err != nil {
		t.Fatalf("runMemberList error: %v", err)
	}
	if gotPath != "/v2.0/lbaas/pools/po1/members" {
		t.Errorf("path = %q, want the pool-scoped member collection", gotPath)
	}
	for _, want := range []string{"me1", "web-1", "10.0.0.21", "ONLINE", "sub-1"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q\n---\n%s", want, buf.String())
		}
	}
}

func TestRunHealthMonitorCreate_RequestBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubEmptyList(fakeServer, "/v2.0/lbaas/pools", "pools")
	var gotMethod string
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/healthmonitors", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"healthmonitors": []}`))
			return
		}
		gotMethod = r.Method
		th.TestJSONRequest(t, r, `{"healthmonitor": {
          "name": "http-check", "pool_id": "po1", "type": "HTTP",
          "delay": 5, "timeout": 3, "max_retries": 3,
          "url_path": "/healthz", "expected_codes": "200"
        }}`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"healthmonitor": {
          "id": "hm1", "name": "http-check", "type": "HTTP", "delay": 5, "timeout": 3, "max_retries": 3
        }}`))
	})

	f := &healthMonitorWriteFlags{
		typ: "HTTP", delay: 5, timeout: 3, maxRetries: 3,
		urlPath: "/healthz", expectedCodes: "200",
	}
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runHealthMonitorCreate(context.Background(), lbClient(fakeServer), o, "po1", "http-check", f, "", &buf)
	if err != nil {
		t.Fatalf("runHealthMonitorCreate error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if !strings.Contains(buf.String(), "hm1") {
		t.Errorf("output missing the new monitor ID\n---\n%s", buf.String())
	}
}

// Octavia rejects a create without delay/timeout/max-retries, so the command says
// which one is missing rather than relaying a generic 400.
func TestHealthMonitorCreate_RequiresTheTimingFlags(t *testing.T) {
	for _, args := range [][]string{
		{"po1", "check", "--type=HTTP", "--timeout=3", "--max-retries=3"},
		{"po1", "check", "--type=HTTP", "--delay=5", "--max-retries=3"},
		{"po1", "check", "--type=HTTP", "--delay=5", "--timeout=3"},
		{"po1", "check", "--delay=5", "--timeout=3", "--max-retries=3"},
	} {
		cmd := newHealthMonitorCreateCommand(nil, &output.Options{Format: output.FormatTable})
		cmd.SetArgs(args)
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		if err := cmd.Execute(); err == nil {
			t.Errorf("%v should have been rejected as incomplete", args)
		}
	}
}

func TestRunHealthMonitorSet_OnlySendsGivenAttributes(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubEmptyList(fakeServer, "/v2.0/lbaas/healthmonitors", "healthmonitors")
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/healthmonitors/hm1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"healthmonitor": {"delay": 10}}`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"healthmonitor": {"id": "hm1", "delay": 10}}`))
	})

	f := &healthMonitorWriteFlags{delay: 10, timeout: 99, urlPath: "/ignored"}
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runHealthMonitorSet(context.Background(), lbClient(fakeServer), o, "hm1", f, changedSet{"delay": true}, &buf)
	if err != nil {
		t.Fatalf("runHealthMonitorSet error: %v", err)
	}
}

// A pool attaches to exactly one of a load balancer or a listener.
func TestPoolCreate_RequiresExactlyOneAnchor(t *testing.T) {
	for _, args := range [][]string{
		{"p", "--protocol=HTTP", "--lb-algorithm=ROUND_ROBIN"},
		{"p", "--protocol=HTTP", "--lb-algorithm=ROUND_ROBIN", "--loadbalancer=lb1", "--listener=li1"},
	} {
		cmd := newPoolCreateCommand(nil, &output.Options{Format: output.FormatTable})
		cmd.SetArgs(args)
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		err := cmd.Execute()
		if err == nil || !strings.Contains(err.Error(), "exactly one of") {
			t.Errorf("%v: err = %v, want an exactly-one-anchor error", args, err)
		}
	}
}

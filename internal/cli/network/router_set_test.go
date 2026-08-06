package network

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/layer3/routers"
	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// routerGetBody is a router with an external gateway and one static route, used to
// exercise the read-back paths.
const routerGetBody = `{"router": {
  "id": "r1", "name": "gw", "status": "ACTIVE", "admin_state_up": true,
  "external_gateway_info": {
    "network_id": "ext-net",
    "enable_snat": true,
    "external_fixed_ips": [{"subnet_id": "ext-sub", "ip_address": "203.0.113.10"}],
    "qos_policy_id": "qos-1"
  },
  "routes": [{"destination": "10.10.0.0/16", "nexthop": "10.0.0.99"}]
}}`

// Neutron replaces external_gateway_info wholesale and rejects it without a
// network_id, so --disable-snat on its own has to re-send the router's current
// gateway network — and its fixed IPs, or neutron would reallocate the gateway
// address.
func TestRunRouterSet_DisableSNATPreservesGateway(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/routers", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"routers": []}`))
	})
	var gotMethod string
	fakeServer.Mux.HandleFunc("/routers/r1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(routerGetBody))
			return
		}
		gotMethod = r.Method
		th.TestJSONRequest(t, r, `{"router": {"external_gateway_info": {
          "network_id": "ext-net",
          "enable_snat": false,
          "external_fixed_ips": [{"subnet_id": "ext-sub", "ip_address": "203.0.113.10"}],
          "qos_policy_id": "qos-1"
        }}}`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(routerGetBody))
	})

	f := &routerSetFlags{disableSNAT: true}
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runRouterSet(context.Background(), networkClient(fakeServer), o, "r1", f,
		changedSet{"disable-snat": true}, &buf)
	if err != nil {
		t.Fatalf("runRouterSet error: %v", err)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
}

// With --external-gateway given there is nothing to preserve, so no read-back
// happens and only what was asked for is sent.
func TestRunRouterSet_ExternalGatewayWithSNATSkipsReadBack(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/networks", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"networks": []}`))
	})
	fakeServer.Mux.HandleFunc("/routers", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"routers": []}`))
	})
	fakeServer.Mux.HandleFunc("/routers/r1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			t.Error("--external-gateway supplies the network id; no read-back should be needed")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		th.TestJSONRequest(t, r, `{"router": {"external_gateway_info": {
          "network_id": "ext-net-2", "enable_snat": true
        }}}`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(routerGetBody))
	})

	f := &routerSetFlags{externalGateway: "ext-net-2", enableSNAT: true}
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runRouterSet(context.Background(), networkClient(fakeServer), o, "r1", f,
		changedSet{"enable-snat": true}, &buf)
	if err != nil {
		t.Fatalf("runRouterSet error: %v", err)
	}
}

// SNAT lives inside external_gateway_info, which neutron will not accept without a
// network — so on a gateway-less router the flag is an error, not a silent no-op.
func TestRunRouterSet_SNATWithoutAGatewayIsAnError(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/routers", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"routers": []}`))
	})
	fakeServer.Mux.HandleFunc("/routers/r1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"router": {"id": "r1", "name": "internal", "external_gateway_info": {}}}`))
	})

	f := &routerSetFlags{enableSNAT: true}
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runRouterSet(context.Background(), networkClient(fakeServer), o, "r1", f,
		changedSet{"enable-snat": true}, &buf)
	if err == nil || !strings.Contains(err.Error(), "no external gateway") {
		t.Fatalf("expected a missing-gateway error, got %v", err)
	}
}

// --route appends to the router's existing routes; --no-route clears them; the two
// together replace them. That is OSC's contract, and each case implies a different
// request body.
func TestRunRouterSet_RouteAppendClearAndReplace(t *testing.T) {
	tests := []struct {
		name     string
		flags    routerSetFlags
		wantBody string
		wantGet  bool
	}{
		{
			name:  "append keeps the existing route",
			flags: routerSetFlags{route: []string{"destination=192.168.0.0/24,gateway=10.0.0.1"}},
			wantBody: `{"router": {"routes": [
              {"destination": "10.10.0.0/16", "nexthop": "10.0.0.99"},
              {"destination": "192.168.0.0/24", "nexthop": "10.0.0.1"}
            ]}}`,
			wantGet: true,
		},
		{
			name:     "no-route clears",
			flags:    routerSetFlags{noRoute: true},
			wantBody: `{"router": {"routes": []}}`,
		},
		{
			name: "both replaces",
			flags: routerSetFlags{
				route:   []string{"destination=192.168.0.0/24,gateway=10.0.0.1"},
				noRoute: true,
			},
			wantBody: `{"router": {"routes": [{"destination": "192.168.0.0/24", "nexthop": "10.0.0.1"}]}}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()

			fakeServer.Mux.HandleFunc("/routers", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"routers": []}`))
			})
			var sawGet bool
			fakeServer.Mux.HandleFunc("/routers/r1", func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					sawGet = true
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(routerGetBody))
					return
				}
				th.TestJSONRequest(t, r, tc.wantBody)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(routerGetBody))
			})

			o := &output.Options{Format: output.FormatTable}
			var buf bytes.Buffer
			flags := tc.flags
			if err := runRouterSet(context.Background(), networkClient(fakeServer), o, "r1", &flags, changedSet{}, &buf); err != nil {
				t.Fatalf("runRouterSet error: %v", err)
			}
			if sawGet != tc.wantGet {
				t.Errorf("read-back happened = %v, want %v (only appending needs the current routes)", sawGet, tc.wantGet)
			}
		})
	}
}

func TestParseRoutes(t *testing.T) {
	tests := []struct {
		name    string
		spec    string
		want    routers.Route
		wantErr string
	}{
		{
			name: "destination and gateway", spec: "destination=10.0.0.0/8,gateway=192.168.1.1",
			want: routers.Route{DestinationCIDR: "10.0.0.0/8", NextHop: "192.168.1.1"},
		},
		{
			// nexthop is the API's own spelling of the same key.
			name: "nexthop spelling", spec: "destination=10.0.0.0/8,nexthop=192.168.1.1",
			want: routers.Route{DestinationCIDR: "10.0.0.0/8", NextHop: "192.168.1.1"},
		},
		{name: "missing gateway", spec: "destination=10.0.0.0/8", wantErr: "requires both"},
		{name: "missing destination", spec: "gateway=192.168.1.1", wantErr: "requires both"},
		{name: "unknown key", spec: "destination=10.0.0.0/8,gateway=1.1.1.1,metric=5", wantErr: "unknown key"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseRoutes([]string{tc.spec})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("parseRoutes() error = %v, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseRoutes() error = %v", err)
			}
			if len(got) != 1 || got[0] != tc.want {
				t.Errorf("parseRoutes() = %+v, want [%+v]", got, tc.want)
			}
		})
	}
}

// Appending a route the router already has must not duplicate it.
func TestRunRouterSet_AppendIsIdempotent(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/routers", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"routers": []}`))
	})
	fakeServer.Mux.HandleFunc("/routers/r1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(routerGetBody))
			return
		}
		th.TestJSONRequest(t, r, `{"router": {"routes": [
          {"destination": "10.10.0.0/16", "nexthop": "10.0.0.99"}
        ]}}`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(routerGetBody))
	})

	f := &routerSetFlags{route: []string{"destination=10.10.0.0/16,gateway=10.0.0.99"}}
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runRouterSet(context.Background(), networkClient(fakeServer), o, "r1", f, changedSet{}, &buf); err != nil {
		t.Fatalf("runRouterSet error: %v", err)
	}
}

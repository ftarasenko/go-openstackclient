package network

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

const (
	parityRouterID  = "aaaaaaaa-1111-4111-8111-111111111111"
	parityExtNetID  = "bbbbbbbb-2222-4222-8222-222222222222"
	parityFlavorID  = "cccccccc-3333-4333-8333-333333333333"
	parityQoSID     = "dddddddd-4444-4444-8444-444444444444"
	parityProjectID = "eeeeeeee-5555-4555-8555-555555555555"
)

// "router create" took only --enable/--disable; every attribute upstream's
// CreateRouter sends now reaches the body, with ha, flavor_id and
// --extra-property merged over the typed opts, and tags set afterwards.
func TestRunRouterCreate_SendsEveryAttributeThenTags(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	emptyLookup(t, fakeServer, "/networks", "networks")
	emptyLookup(t, fakeServer, "/subnets", "subnets")
	emptyLookup(t, fakeServer, "/qos/policies", "policies")
	fakeServer.Mux.HandleFunc("/routers", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		th.TestJSONRequest(t, r, `{"router":{
		  "name":"r1","description":"edge","admin_state_up":true,"distributed":true,
		  "project_id":"p1","availability_zone_hints":["az1","az2"],
		  "external_gateway_info":{"network_id":"ext-net","enable_snat":false,
		    "external_fixed_ips":[{"subnet_id":"ext-sub","ip_address":"203.0.113.5"}],
		    "qos_policy_id":"qos-1"},
		  "ha":true,"flavor_id":"`+parityFlavorID+`","custom":"x"}}`)
		writeJSON(t, w, http.StatusCreated, `{"router":{"id":"rtr-1","name":"r1","ha":true,
		  "flavor_id":"`+parityFlavorID+`","availability_zones":[],"tags":[]}}`)
	})
	fakeServer.Mux.HandleFunc("/routers/rtr-1/tags", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"tags":["blue","red"]}`)
		writeJSON(t, w, http.StatusOK, `{"tags":["blue","red"]}`)
	})

	f := &routerCreateFlags{
		description: "edge", distributed: true, ha: true, azHints: []string{"az1", "az2"},
		externalGateway: "ext-net", fixedIPs: []string{"subnet=ext-sub,ip-address=203.0.113.5"},
		disableSNAT: true, qosPolicy: "qos-1", flavor: parityFlavorID, projectID: "p1",
		extraProperty: []string{"name=custom,value=x"},
		tagWriteFlags: tagWriteFlags{tags: []string{"red", "blue"}},
	}
	o := &output.Options{Format: output.FormatJSON}
	var buf bytes.Buffer
	if err := runRouterCreate(context.Background(), networkClient(fakeServer), o, "r1", f, &buf); err != nil {
		t.Fatalf("runRouterCreate: %v", err)
	}
	for _, want := range []string{`"ha": true`, `"flavor_id": "` + parityFlavorID + `"`, `"blue"`, `"availability_zones": []`} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %s:\n%s", want, buf.String())
		}
	}
}

// --centralized / --no-ha are the explicit false halves of their pairs.
func TestRunRouterCreate_CentralizedNoHA(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/routers", func(w http.ResponseWriter, r *http.Request) {
		th.TestJSONRequest(t, r, `{"router":{"name":"r","admin_state_up":true,"distributed":false,"ha":false}}`)
		writeJSON(t, w, http.StatusCreated, `{"router":{"id":"rtr-1"}}`)
	})
	f := &routerCreateFlags{centralized: true, noHA: true}
	var buf bytes.Buffer
	if err := runRouterCreate(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatValue}, "r", f, &buf); err != nil {
		t.Fatalf("runRouterCreate: %v", err)
	}
}

// Upstream raises this only after creating the router; koc refuses before
// sending anything.
func TestRunRouterCreate_GatewayFlagsNeedExternalGateway(t *testing.T) {
	for name, f := range map[string]*routerCreateFlags{
		"snat":     {enableSNAT: true},
		"fixed ip": {fixedIPs: []string{"subnet=s"}},
		"qos":      {qosPolicy: "q"},
	} {
		t.Run(name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown() // no handlers: any request 404s
			err := runRouterCreate(context.Background(), networkClient(fakeServer), &output.Options{}, "r", f, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), "need --external-gateway") {
				t.Fatalf("error = %v, want the needs-external-gateway message", err)
			}
		})
	}
}

func TestRouterFlavorID(t *testing.T) {
	tests := []struct {
		name, flavor, flavorID, want, wantErr string
	}{
		{name: "none"},
		{name: "uuid", flavor: parityFlavorID, want: parityFlavorID},
		{name: "alias wins and passes through", flavor: "gold", flavorID: "f-1", want: "f-1"},
		{name: "name refused", flavor: "gold", wantErr: "cannot look a network flavor up by name"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := routerFlavorID(tc.flavor, tc.flavorID)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("routerFlavorID = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

// The new set attributes: description (even empty), distributed/HA, and a
// gateway rebuilt from --external-gateway with --fixed-ip and a null policy.
func TestRunRouterSet_NewAttributes(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	emptyLookup(t, fakeServer, "/routers", "routers")
	emptyLookup(t, fakeServer, "/networks", "networks")
	emptyLookup(t, fakeServer, "/subnets", "subnets")
	fakeServer.Mux.HandleFunc("/routers/r1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"router":{"description":"","distributed":false,"ha":false,
		  "external_gateway_info":{"network_id":"ext-2",
		    "external_fixed_ips":[{"subnet_id":"sub-2"}],"qos_policy_id":null},
		  "custom":7}}`)
		writeJSON(t, w, http.StatusOK, `{"router":{"id":"r1","ha":false}}`)
	})
	f := &routerSetFlags{
		centralized: true, noHA: true, externalGateway: "ext-2",
		fixedIPs: []string{"subnet=sub-2"}, noQoSPolicy: true,
		extraProperty: []string{"type=int,name=custom,value=7"},
	}
	flags := changedSet{flagDescription: true, flagRouterCentralized: true, flagRouterNoHA: true}
	var buf bytes.Buffer
	if err := runRouterSet(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatValue}, "r1", f, flags, &buf); err != nil {
		t.Fatalf("runRouterSet: %v", err)
	}
}

// A QoS-only change on a router with a gateway sends upstream's body: the
// current network and the policy — not the fixed IPs or SNAT, whose
// sub-attributes neutron's default policy reserves for admins.
func TestRunRouterSet_QoSOnlySendsNetworkAndPolicy(t *testing.T) {
	for name, tc := range map[string]struct {
		flags routerSetFlags
		body  string
	}{
		"qos-policy":    {routerSetFlags{qosPolicy: "qos-2"}, `{"router":{"external_gateway_info":{"network_id":"ext-net","qos_policy_id":"qos-2"}}}`},
		"no-qos-policy": {routerSetFlags{noQoSPolicy: true}, `{"router":{"external_gateway_info":{"network_id":"ext-net","qos_policy_id":null}}}`},
	} {
		t.Run(name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()
			emptyLookup(t, fakeServer, "/routers", "routers")
			emptyLookup(t, fakeServer, "/qos/policies", "policies")
			fakeServer.Mux.HandleFunc("/routers/r1", func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					writeJSON(t, w, http.StatusOK, routerGetBody)
					return
				}
				th.TestJSONRequest(t, r, tc.body)
				writeJSON(t, w, http.StatusOK, routerGetBody)
			})
			flags := tc.flags
			if err := runRouterSet(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatValue},
				"r1", &flags, changedSet{}, &bytes.Buffer{}); err != nil {
				t.Fatalf("runRouterSet: %v", err)
			}
		})
	}
}

// --fixed-ip without --external-gateway re-sends the current network (koc
// does not make the operator repeat it) and keeps SNAT and the policy.
func TestRunRouterSet_FixedIPKeepsTheCurrentGateway(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	emptyLookup(t, fakeServer, "/routers", "routers")
	emptyLookup(t, fakeServer, "/subnets", "subnets")
	fakeServer.Mux.HandleFunc("/routers/r1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeJSON(t, w, http.StatusOK, routerGetBody)
			return
		}
		th.TestJSONRequest(t, r, `{"router":{"external_gateway_info":{"network_id":"ext-net","enable_snat":true,
		  "external_fixed_ips":[{"subnet_id":"ext-sub","ip_address":"203.0.113.20"}],"qos_policy_id":"qos-1"}}}`)
		writeJSON(t, w, http.StatusOK, routerGetBody)
	})
	f := &routerSetFlags{fixedIPs: []string{"subnet=ext-sub,ip-address=203.0.113.20"}}
	if err := runRouterSet(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatValue},
		"r1", f, changedSet{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("runRouterSet: %v", err)
	}
}

// Upstream skips the router PUT when tags are the only change.
func TestRunRouterSet_TagsOnlySkipsTheUpdate(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	emptyLookup(t, fakeServer, "/routers", "routers")
	fakeServer.Mux.HandleFunc("/routers/r1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		writeJSON(t, w, http.StatusOK, `{"router":{"id":"r1","tags":["old"]}}`)
	})
	fakeServer.Mux.HandleFunc("/routers/r1/tags", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"tags":["new"]}`)
		writeJSON(t, w, http.StatusOK, `{"tags":["new"]}`)
	})
	f := &routerSetFlags{tagWriteFlags: tagWriteFlags{noTag: true, tags: []string{"new"}}}
	var buf bytes.Buffer
	if err := runRouterSet(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatValue},
		"r1", f, changedSet{}, &buf); err != nil {
		t.Fatalf("runRouterSet: %v", err)
	}
	if !strings.Contains(buf.String(), "new") || strings.Contains(buf.String(), "old") {
		t.Errorf("output does not show the replaced tag set:\n%s", buf.String())
	}
}

// unset --route rewrites the list minus the named route, --qos-policy sends
// upstream's {network_id, qos_policy_id: null}, --extra-property nulls, and
// --all-tag clears the tags afterwards.
func TestRunRouterUnset_RouteQoSExtraAndTags(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	emptyLookup(t, fakeServer, "/routers", "routers")
	var puts int
	fakeServer.Mux.HandleFunc("/routers/r1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeJSON(t, w, http.StatusOK, routerGetBody)
			return
		}
		puts++
		th.TestJSONRequest(t, r, `{"router":{"routes":[],
		  "external_gateway_info":{"network_id":"ext-net","qos_policy_id":null},"custom":null}}`)
		writeJSON(t, w, http.StatusOK, `{"router":{"id":"r1","tags":["a"]}}`)
	})
	fakeServer.Mux.HandleFunc("/routers/r1/tags", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"tags":[]}`)
		writeJSON(t, w, http.StatusOK, `{"tags":[]}`)
	})
	f := &routerUnsetFlags{
		route:     []string{"destination=10.10.0.0/16,gateway=10.0.0.99"},
		qosPolicy: true, extraProperty: []string{"name=custom"},
		tagWriteFlags: tagWriteFlags{allTag: true},
	}
	if err := runRouterUnset(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatValue}, "r1", f, &bytes.Buffer{}); err != nil {
		t.Fatalf("runRouterUnset: %v", err)
	}
	if puts != 1 {
		t.Errorf("router PUTs = %d, want 1", puts)
	}
}

func TestRunRouterUnset_Refusals(t *testing.T) {
	tests := []struct {
		name    string
		get     string
		flags   routerUnsetFlags
		wantErr string
	}{
		{
			name: "route not on the router", get: routerGetBody,
			flags:   routerUnsetFlags{route: []string{"destination=192.0.2.0/24,gateway=10.0.0.1"}},
			wantErr: "does not contain route destination=192.0.2.0/24,gateway=10.0.0.1",
		},
		{
			name: "qos without a gateway", get: `{"router":{"id":"r1","external_gateway_info":null}}`,
			flags:   routerUnsetFlags{qosPolicy: true},
			wantErr: "has no external gateway",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()
			emptyLookup(t, fakeServer, "/routers", "routers")
			fakeServer.Mux.HandleFunc("/routers/r1", func(w http.ResponseWriter, r *http.Request) {
				th.TestMethod(t, r, http.MethodGet) // a refusal must not PUT
				writeJSON(t, w, http.StatusOK, tc.get)
			})
			flags := tc.flags
			err := runRouterUnset(context.Background(), networkClient(fakeServer), &output.Options{}, "r1", &flags, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestRunRouterUnset_TagsOnlySkipsTheUpdate(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	emptyLookup(t, fakeServer, "/routers", "routers")
	fakeServer.Mux.HandleFunc("/routers/r1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		writeJSON(t, w, http.StatusOK, `{"router":{"id":"r1","tags":["a","b"]}}`)
	})
	fakeServer.Mux.HandleFunc("/routers/r1/tags", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"tags":["b"]}`)
		writeJSON(t, w, http.StatusOK, `{"tags":["b"]}`)
	})
	f := &routerUnsetFlags{tagWriteFlags: tagWriteFlags{tags: []string{"a"}}}
	if err := runRouterUnset(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatValue}, "r1", f, &bytes.Buffer{}); err != nil {
		t.Fatalf("runRouterUnset: %v", err)
	}
}

// remove gateway's <network> and --fixed-ip identify the gateway: a match
// clears it, a mismatch removes nothing.
func TestRunRouterRemoveGateway_NetworkAndFixedIPGuard(t *testing.T) {
	tests := []struct {
		name     string
		network  string
		fixedIPs []string
		wantErr  string
	}{
		{name: "matching network and address", network: "ext-net", fixedIPs: []string{"ip-address=203.0.113.10"}},
		{name: "matching subnet", fixedIPs: []string{"subnet=ext-sub"}},
		{name: "other network", network: "other-net", wantErr: "has no gateway on network other-net"},
		{name: "other address", fixedIPs: []string{"subnet=ext-sub,ip-address=203.0.113.99"}, wantErr: "no gateway address matching"},
		{name: "empty spec", fixedIPs: []string{"subnet="}, wantErr: "requires subnet= or ip-address="},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()
			emptyLookup(t, fakeServer, "/routers", "routers")
			emptyLookup(t, fakeServer, "/networks", "networks")
			emptyLookup(t, fakeServer, "/subnets", "subnets")
			var put bool
			fakeServer.Mux.HandleFunc("/routers/r1", func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPut {
					put = true
					th.TestJSONRequest(t, r, `{"router":{"external_gateway_info":{}}}`)
					writeJSON(t, w, http.StatusOK, `{"router":{"id":"r1"}}`)
					return
				}
				writeJSON(t, w, http.StatusOK, routerGetBody)
			})
			err := runRouterRemoveGateway(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatValue},
				"r1", tc.network, tc.fixedIPs, &bytes.Buffer{})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want %q", err, tc.wantErr)
				}
				if put {
					t.Error("a mismatch must not clear the gateway")
				}
				return
			}
			if err != nil || !put {
				t.Fatalf("err = %v, cleared = %v; want the gateway cleared", err, put)
			}
		})
	}
}

// router show adds upstream's ha / availability-zone / flavor fields; ha only
// when neutron sent it, as upstream hides it when absent.
func TestRunRouterShow_ExtensionFields(t *testing.T) {
	for name, tc := range map[string]struct {
		body   string
		wantHA bool
	}{
		"admin view": {`{"router":{"id":"r1","ha":true,"availability_zone_hints":["az1"],"availability_zones":["az1"],"flavor_id":"f-1"}}`, true},
		"user view":  {`{"router":{"id":"r1"}}`, false},
	} {
		t.Run(name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()
			emptyLookup(t, fakeServer, "/routers", "routers")
			emptyLookup(t, fakeServer, "/ports", "ports")
			fakeServer.Mux.HandleFunc("/routers/r1", func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, http.StatusOK, tc.body)
			})
			var buf bytes.Buffer
			if err := runRouterShow(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatJSON}, "r1", &buf); err != nil {
				t.Fatalf("runRouterShow: %v", err)
			}
			if got := strings.Contains(buf.String(), `"ha"`); got != tc.wantHA {
				t.Errorf("ha field shown = %v, want %v:\n%s", got, tc.wantHA, buf.String())
			}
			for _, want := range []string{`"availability_zone_hints"`, `"availability_zones"`, `"flavor_id"`} {
				if !strings.Contains(buf.String(), want) {
					t.Errorf("output missing %s:\n%s", want, buf.String())
				}
			}
		})
	}
}

// --project is resolved in RunE (a UUID needs no keystone call), and the
// create flags reach the seam through cobra.
func TestExec_RouterCreate_FlagsReachTheBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	emptyLookup(t, fakeServer, "/v2.0/networks", "networks")
	emptyLookup(t, fakeServer, "/v2.0/qos/policies", "policies")
	fakeServer.Mux.HandleFunc("/v2.0/routers", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		th.TestJSONRequest(t, r, `{"router":{"name":"edge","admin_state_up":false,"description":"d",
		  "project_id":"`+parityProjectID+`","availability_zone_hints":["az1"],
		  "external_gateway_info":{"network_id":"`+parityExtNetID+`","enable_snat":true,"qos_policy_id":"`+parityQoSID+`"},
		  "ha":true,"flavor_id":"`+parityFlavorID+`"}}`)
		writeJSON(t, w, http.StatusCreated, `{"router":{"id":"`+parityRouterID+`","name":"edge","tags":[]}}`)
	})
	fakeServer.Mux.HandleFunc("/v2.0/routers/"+parityRouterID+"/tags", func(w http.ResponseWriter, r *http.Request) {
		th.TestJSONRequest(t, r, `{"tags":["x"]}`)
		writeJSON(t, w, http.StatusOK, `{"tags":["x"]}`)
	})
	out, err := execNetwork(t, fakeServer, "router", "create", "edge", "--disable", "--description", "d",
		"--project", parityProjectID, "--availability-zone-hint", "az1", "--ha",
		"--external-gateway", parityExtNetID, "--enable-snat", "--qos-policy", parityQoSID,
		"--flavor-id", parityFlavorID, "--tag", "x")
	if err != nil {
		t.Fatalf("router create: %v (output %q)", err, out)
	}
}

// remove gateway's optional <network> positional reaches the guard.
func TestExec_RouterRemoveGateway_NetworkPositional(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	emptyLookup(t, fakeServer, "/v2.0/routers", "routers")
	emptyLookup(t, fakeServer, "/v2.0/networks", "networks")
	fakeServer.Mux.HandleFunc("/v2.0/routers/r1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			t.Error("a gateway on another network must not be cleared")
		}
		writeJSON(t, w, http.StatusOK, routerGetBody)
	})
	_, err := execNetwork(t, fakeServer, "router", "remove", "gateway", "r1", "other-net")
	if err == nil || !strings.Contains(err.Error(), "has no gateway on network other-net") {
		t.Fatalf("error = %v, want the network mismatch", err)
	}
}

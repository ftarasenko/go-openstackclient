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

// Post-Zed router flags (batch pz-router): the NDP-proxy and default-route
// BFD/ECMP pairs on create and set, --evpn-vni/--auto-evpn-vni on create,
// --advertise-host on add subnet, and the named-extension error.

// multihomingExtensions answers /extensions with external-gateway-multihoming
// enabled, which upstream demands before sending BFD/ECMP.
func multihomingExtensions(t *testing.T, fakeServer th.FakeServer, path string) {
	t.Helper()
	fakeServer.Mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		writeJSON(t, w, http.StatusOK, `{"extensions":[{"alias":"router"},{"alias":"external-gateway-multihoming"}]}`)
	})
}

func TestRunRouterCreate_PostZedBodies(t *testing.T) {
	for name, tc := range map[string]struct {
		f    *routerCreateFlags
		body string
	}{
		"enable halves and a vni": {
			f: &routerCreateFlags{
				externalGateway: parityExtNetID,
				evpnVNI:         "5001",
				routerPostZedFlags: routerPostZedFlags{
					enableNDPProxy: true, enableBFD: true, enableECMP: true,
				},
			},
			body: `{"router":{"name":"r1","admin_state_up":true,
			  "external_gateway_info":{"network_id":"` + parityExtNetID + `"},
			  "enable_ndp_proxy":true,"enable_default_route_bfd":true,
			  "enable_default_route_ecmp":true,"evpn_vni":5001}}`,
		},
		"disable halves and an auto vni": {
			f: &routerCreateFlags{
				autoEVPNVNI: true,
				routerPostZedFlags: routerPostZedFlags{
					disableNDPProxy: true, disableBFD: true, disableECMP: true,
				},
			},
			body: `{"router":{"name":"r1","admin_state_up":true,
			  "enable_ndp_proxy":false,"enable_default_route_bfd":false,
			  "enable_default_route_ecmp":false,"evpn_vni":0}}`,
		},
		// Upstream's order: the NDP-proxy flag is set after --extra-property
		// and wins; BFD is collected before it and loses.
		"ndp proxy wins over an extra property, bfd does not": {
			f: &routerCreateFlags{
				routerPostZedFlags: routerPostZedFlags{disableNDPProxy: true, enableBFD: true},
				extraProperty: []string{
					"type=bool,name=enable_ndp_proxy,value=true",
					"type=bool,name=enable_default_route_bfd,value=false",
				},
			},
			body: `{"router":{"name":"r1","admin_state_up":true,
			  "enable_ndp_proxy":false,"enable_default_route_bfd":false}}`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()
			echoLookup(t, fakeServer, "/networks", "networks")
			multihomingExtensions(t, fakeServer, "/extensions")
			fakeServer.Mux.HandleFunc("/routers", func(w http.ResponseWriter, r *http.Request) {
				th.TestMethod(t, r, http.MethodPost)
				th.TestJSONRequest(t, r, tc.body)
				writeJSON(t, w, http.StatusCreated, `{"router":{"id":"rtr-1","name":"r1","enable_ndp_proxy":true,"evpn_vni":5001}}`)
			})
			var buf bytes.Buffer
			if err := runRouterCreate(context.Background(), networkClient(fakeServer),
				&output.Options{Format: output.FormatJSON}, "r1", tc.f, &buf); err != nil {
				t.Fatalf("runRouterCreate: %v", err)
			}
			for _, want := range []string{`"enable_ndp_proxy": true`, `"evpn_vni": 5001`} {
				if !strings.Contains(buf.String(), want) {
					t.Errorf("output missing %s:\n%s", want, buf.String())
				}
			}
		})
	}
}

func TestRunRouterSet_PostZedBodies(t *testing.T) {
	for name, tc := range map[string]struct {
		f    routerPostZedFlags
		body string
	}{
		"enable": {
			routerPostZedFlags{enableNDPProxy: true, enableBFD: true, enableECMP: true},
			`{"router":{"enable_ndp_proxy":true,"enable_default_route_bfd":true,"enable_default_route_ecmp":true}}`,
		},
		"disable": {
			routerPostZedFlags{disableNDPProxy: true, disableBFD: true, disableECMP: true},
			`{"router":{"enable_ndp_proxy":false,"enable_default_route_bfd":false,"enable_default_route_ecmp":false}}`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()
			echoLookup(t, fakeServer, "/routers", "routers")
			multihomingExtensions(t, fakeServer, "/extensions")
			fakeServer.Mux.HandleFunc("/routers/r1", func(w http.ResponseWriter, r *http.Request) {
				th.TestMethod(t, r, http.MethodPut)
				th.TestJSONRequest(t, r, tc.body)
				writeJSON(t, w, http.StatusOK, `{"router":{"id":"r1"}}`)
			})
			f := &routerSetFlags{routerPostZedFlags: tc.f}
			if err := runRouterSet(context.Background(), networkClient(fakeServer),
				&output.Options{Format: output.FormatValue}, "r1", f, changedSet{}, &bytes.Buffer{}); err != nil {
				t.Fatalf("runRouterSet: %v", err)
			}
		})
	}
}

// Set's NDP proxy needs no --external-gateway (upstream checks only on create)
// and, with no BFD/ECMP, no extension lookup either.
func TestRunRouterSet_NDPProxyAloneSendsOnlyIt(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	echoLookup(t, fakeServer, "/routers", "routers")
	fakeServer.Mux.HandleFunc("/extensions", func(http.ResponseWriter, *http.Request) {
		t.Error("the multihoming check must not run without BFD/ECMP")
	})
	fakeServer.Mux.HandleFunc("/routers/r1", func(w http.ResponseWriter, r *http.Request) {
		th.TestJSONRequest(t, r, `{"router":{"enable_ndp_proxy":true}}`)
		writeJSON(t, w, http.StatusOK, `{"router":{"id":"r1"}}`)
	})
	f := &routerSetFlags{routerPostZedFlags: routerPostZedFlags{enableNDPProxy: true}}
	if err := runRouterSet(context.Background(), networkClient(fakeServer),
		&output.Options{Format: output.FormatValue}, "r1", f, changedSet{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("runRouterSet: %v", err)
	}
}

func TestRunRouterAddSubnet_AdvertiseHost(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	echoLookup(t, fakeServer, "/routers", "routers")
	echoLookup(t, fakeServer, "/subnets", "subnets")
	fakeServer.Mux.HandleFunc("/routers/rtr-1/add_router_interface", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"subnet_id":"sub-1","advertise_host":true}`)
		writeJSON(t, w, http.StatusOK, `{"id":"rtr-1","subnet_id":"sub-1","port_id":"port-1"}`)
	})
	var buf bytes.Buffer
	if err := runRouterAddSubnet(context.Background(), networkClient(fakeServer), "rtr-1", "sub-1", true, &buf); err != nil {
		t.Fatalf("runRouterAddSubnet: %v", err)
	}
	if !strings.Contains(buf.String(), "Added interface for subnet sub-1 to router rtr-1") {
		t.Errorf("output = %q", buf.String())
	}
}

// The client-side refusals send no write: each mock has no /routers handler
// that accepts a POST.
func TestRunRouterCreate_PostZedRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		f       *routerCreateFlags
		exts    string
		wantErr string
	}{
		"ndp proxy needs a gateway": {
			f:       &routerCreateFlags{routerPostZedFlags: routerPostZedFlags{enableNDPProxy: true}},
			wantErr: "--enable-ndp-proxy needs --external-gateway",
		},
		"vni not an integer": {
			f: &routerCreateFlags{evpnVNI: "abc"}, wantErr: `"abc" is not a valid VNI`,
		},
		"vni zero": {
			f: &routerCreateFlags{evpnVNI: "0"}, wantErr: "VNI must be a positive integer",
		},
		"vni negative": {
			f: &routerCreateFlags{evpnVNI: "-3"}, wantErr: "VNI must be a positive integer",
		},
		"bfd without multihoming": {
			f:       &routerCreateFlags{routerPostZedFlags: routerPostZedFlags{enableBFD: true}},
			exts:    `{"extensions":[{"alias":"router"}]}`,
			wantErr: "external-gateway-multihoming extension is not enabled",
		},
		"ecmp via extra property without multihoming": {
			f:       &routerCreateFlags{extraProperty: []string{"type=bool,name=enable_default_route_ecmp,value=true"}},
			exts:    `{"extensions":[]}`,
			wantErr: "external-gateway-multihoming extension is not enabled",
		},
	} {
		t.Run(name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()
			if tc.exts != "" {
				fakeServer.Mux.HandleFunc("/extensions", func(w http.ResponseWriter, _ *http.Request) {
					writeJSON(t, w, http.StatusOK, tc.exts)
				})
			}
			fakeServer.Mux.HandleFunc("/routers", func(http.ResponseWriter, *http.Request) {
				t.Error("a refused create must send nothing")
			})
			err := runRouterCreate(context.Background(), networkClient(fakeServer), &output.Options{}, "r", tc.f, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestRunRouterSet_BFDWithoutMultihomingSendsNothing(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	echoLookup(t, fakeServer, "/routers", "routers")
	fakeServer.Mux.HandleFunc("/extensions", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, `{"extensions":[{"alias":"router"}]}`)
	})
	fakeServer.Mux.HandleFunc("/routers/r1", func(http.ResponseWriter, *http.Request) {
		t.Error("a refused set must send nothing")
	})
	f := &routerSetFlags{routerPostZedFlags: routerPostZedFlags{disableECMP: true}}
	err := runRouterSet(context.Background(), networkClient(fakeServer), &output.Options{}, "r1", f, changedSet{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "external-gateway-multihoming") {
		t.Fatalf("error = %v, want the multihoming refusal", err)
	}
}

// A Zed cloud answers enable_ndp_proxy with a bare 400; the error names the
// missing extension, on create, on set and on add subnet.
func TestRouterWrites_ExplainMissingExtension(t *testing.T) {
	const zedExtensions = `{"extensions":[{"alias":"router"},{"alias":"ext-gw-mode"}]}`
	const unrecognized = `{"NeutronError":{"type":"HTTPBadRequest","message":"Unrecognized attribute(s)"}}`
	for name, tc := range map[string]struct {
		path string
		run  func(th.FakeServer) error
		want string
	}{
		"create": {
			path: "/routers",
			run: func(fs th.FakeServer) error {
				f := &routerCreateFlags{routerPostZedFlags: routerPostZedFlags{disableNDPProxy: true}}
				return runRouterCreate(context.Background(), networkClient(fs), &output.Options{}, "r1", f, &bytes.Buffer{})
			},
			want: "l3-ext-ndp-proxy (for enable_ndp_proxy)",
		},
		"set": {
			path: "/routers/r1",
			run: func(fs th.FakeServer) error {
				f := &routerSetFlags{routerPostZedFlags: routerPostZedFlags{enableNDPProxy: true}}
				return runRouterSet(context.Background(), networkClient(fs), &output.Options{}, "r1", f, changedSet{}, &bytes.Buffer{})
			},
			want: "l3-ext-ndp-proxy (for enable_ndp_proxy)",
		},
		"unset extra property": {
			path: "/routers/r1",
			run: func(fs th.FakeServer) error {
				f := &routerUnsetFlags{extraProperty: []string{"name=enable_ndp_proxy"}}
				return runRouterUnset(context.Background(), networkClient(fs), &output.Options{}, "r1", f, &bytes.Buffer{})
			},
			want: "l3-ext-ndp-proxy (for enable_ndp_proxy)",
		},
		"add subnet": {
			path: "/routers/r1/add_router_interface",
			run: func(fs th.FakeServer) error {
				return runRouterAddSubnet(context.Background(), networkClient(fs), "r1", "s1", true, &bytes.Buffer{})
			},
			want: "evpn (for advertise_host)",
		},
	} {
		t.Run(name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()
			if tc.path != "/routers" {
				echoLookup(t, fakeServer, "/routers", "routers")
			}
			echoLookup(t, fakeServer, "/subnets", "subnets")
			fakeServer.Mux.HandleFunc("/extensions", func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, http.StatusOK, zedExtensions)
			})
			fakeServer.Mux.HandleFunc(tc.path, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					writeJSON(t, w, http.StatusOK, `{"routers":[]}`)
					return
				}
				writeJSON(t, w, http.StatusBadRequest, unrecognized)
			})
			err := tc.run(fakeServer)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to name %q", err, tc.want)
			}
		})
	}
}

// show renders enable_ndp_proxy always (empty on an older cloud, as upstream)
// and evpn_vni / the default-route switches only when neutron sent them.
func TestRunRouterShow_PostZedFields(t *testing.T) {
	for name, tc := range map[string]struct {
		body       string
		want       []string
		wantAbsent []string
	}{
		"zed": {
			body:       `{"router":{"id":"r1"}}`,
			want:       []string{`"enable_ndp_proxy": null`},
			wantAbsent: []string{"evpn_vni", "enable_default_route_bfd", "enable_default_route_ecmp"},
		},
		"current": {
			body: `{"router":{"id":"r1","enable_ndp_proxy":true,"evpn_vni":42,
			  "enable_default_route_bfd":false,"enable_default_route_ecmp":true}}`,
			want: []string{`"enable_ndp_proxy": true`, `"evpn_vni": 42`,
				`"enable_default_route_bfd": false`, `"enable_default_route_ecmp": true`},
		},
	} {
		t.Run(name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()
			echoLookup(t, fakeServer, "/routers", "routers")
			echoLookup(t, fakeServer, "/ports", "ports")
			fakeServer.Mux.HandleFunc("/routers/r1", func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, http.StatusOK, tc.body)
			})
			var buf bytes.Buffer
			if err := runRouterShow(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatJSON}, "r1", &buf); err != nil {
				t.Fatalf("runRouterShow: %v", err)
			}
			for _, want := range tc.want {
				if !strings.Contains(buf.String(), want) {
					t.Errorf("output missing %s:\n%s", want, buf.String())
				}
			}
			for _, absent := range tc.wantAbsent {
				if strings.Contains(buf.String(), absent) {
					t.Errorf("output shows %s:\n%s", absent, buf.String())
				}
			}
		})
	}
}

// The flags reach the seams through cobra.
func TestExec_RouterPostZedFlagsReachTheBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	echoLookup(t, fakeServer, "/v2.0/networks", "networks")
	echoLookup(t, fakeServer, "/v2.0/subnets", "subnets")
	multihomingExtensions(t, fakeServer, "/v2.0/extensions")
	fakeServer.Mux.HandleFunc("/v2.0/routers", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet { // set/add resolve the router by name first
			writeJSON(t, w, http.StatusOK, `{"routers":[]}`)
			return
		}
		th.TestMethod(t, r, http.MethodPost)
		th.TestJSONRequest(t, r, `{"router":{"name":"edge","admin_state_up":true,
		  "external_gateway_info":{"network_id":"`+parityExtNetID+`"},
		  "enable_ndp_proxy":true,"enable_default_route_bfd":true,
		  "enable_default_route_ecmp":false,"evpn_vni":7}}`)
		writeJSON(t, w, http.StatusCreated, `{"router":{"id":"`+parityRouterID+`","name":"edge"}}`)
	})
	fakeServer.Mux.HandleFunc("/v2.0/routers/"+parityRouterID, func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"router":{"enable_ndp_proxy":false,"enable_default_route_ecmp":true}}`)
		writeJSON(t, w, http.StatusOK, `{"router":{"id":"`+parityRouterID+`"}}`)
	})
	fakeServer.Mux.HandleFunc("/v2.0/routers/"+parityRouterID+"/add_router_interface", func(w http.ResponseWriter, r *http.Request) {
		th.TestJSONRequest(t, r, `{"subnet_id":"`+parityQoSID+`","advertise_host":true}`)
		writeJSON(t, w, http.StatusOK, `{"id":"`+parityRouterID+`"}`)
	})
	for _, argv := range [][]string{
		{"router", "create", "edge", "--external-gateway", parityExtNetID, "--enable-ndp-proxy",
			"--enable-default-route-bfd", "--disable-default-route-ecmp", "--evpn-vni", "7"},
		{"router", "set", parityRouterID, "--disable-ndp-proxy", "--enable-default-route-ecmp"},
		{"router", "add", "subnet", parityRouterID, parityQoSID, "--advertise-host"},
	} {
		if out, err := execNetwork(t, fakeServer, argv...); err != nil {
			t.Fatalf("%v: %v (output %q)", argv, err, out)
		}
	}
}

// The on/off pairs and --evpn-vni/--auto-evpn-vni are refused together
// before any request.
func TestExec_RouterPostZedPairsAreExclusive(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown() // no handlers: any request fails the command differently
	for _, argv := range [][]string{
		{"router", "create", "r", "--evpn-vni", "5", "--auto-evpn-vni"},
		{"router", "create", "r", "--enable-ndp-proxy", "--disable-ndp-proxy"},
		{"router", "set", "r", "--enable-default-route-bfd", "--disable-default-route-bfd"},
		{"router", "set", "r", "--enable-default-route-ecmp", "--disable-default-route-ecmp"},
	} {
		_, err := execNetwork(t, fakeServer, argv...)
		if err == nil || !strings.Contains(err.Error(), "were all set") {
			t.Errorf("%v: error = %v, want cobra's mutually-exclusive refusal", argv, err)
		}
	}
	// evpn_vni is create-only in neutron (allow_put false), as upstream's set.
	if _, err := execNetwork(t, fakeServer, "router", "set", "r", "--evpn-vni", "5"); err == nil ||
		!strings.Contains(err.Error(), "unknown flag") {
		t.Errorf("router set --evpn-vni: error = %v, want unknown flag", err)
	}
}

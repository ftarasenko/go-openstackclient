package network

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Post-Zed network/subnet flags: --pvlan/--no-pvlan, --qinq-vlan/--no-qinq-vlan
// (network), --leak-routes/--no-leak-routes (subnet set), and the
// missing-extension explanation on every network/subnet write.

const pzNetID = "5e1f0c2a-0000-4000-8000-000000000001"

// pzJSONSingle renders o.WriteSingle output as JSON and decodes it.
func pzJSONSingle(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("decoding output %q: %v", buf.String(), err)
	}
	return got
}

// pzExtensions serves GET <path> (the extensions collection; the seam client
// has no /v2.0 prefix, execNetwork's does) with the given aliases.
func pzExtensions(t *testing.T, fakeServer th.FakeServer, path string, aliases ...string) {
	t.Helper()
	parts := make([]string, 0, len(aliases))
	for _, a := range aliases {
		parts = append(parts, `{"alias":"`+a+`"}`)
	}
	fakeServer.Mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		writeJSON(t, w, http.StatusOK, `{"extensions":[`+strings.Join(parts, ",")+`]}`)
	})
}

// pzNoRequest fails the test if path is hit at all.
func pzNoRequest(t *testing.T, fakeServer th.FakeServer, path string) {
	t.Helper()
	fakeServer.Mux.HandleFunc(path, func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
	})
}

func assertOriginalHTTPError(t *testing.T, err error) {
	t.Helper()
	var httpErr gophercloud.ErrUnexpectedResponseCode
	if !errors.As(err, &httpErr) || httpErr.Actual != http.StatusBadRequest {
		t.Errorf("the original 400 must stay wrapped: %v", err)
	}
}

// --- network list ------------------------------------------------------------

func TestPZNetworkList_PVLANFilter(t *testing.T) {
	for _, tc := range []struct {
		name string
		f    *networkListFlags
		want string
	}{
		{"pvlan", &networkListFlags{pvlan: true}, "true"},
		{"no-pvlan", &networkListFlags{noPVLAN: true}, "false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()
			fakeServer.Mux.HandleFunc("/networks", func(w http.ResponseWriter, r *http.Request) {
				th.TestMethod(t, r, http.MethodGet)
				if got := r.URL.Query(); !reflect.DeepEqual(got, url.Values{"pvlan": {tc.want}}) {
					t.Errorf("query = %v, want pvlan=%s only", got, tc.want)
				}
				writeJSON(t, w, http.StatusOK, `{"networks":[{"id":"n1","name":"pv","subnets":[]}]}`)
			})
			var buf bytes.Buffer
			o := &output.Options{Format: output.FormatValue}
			if err := runNetworkList(context.Background(), networkClient(fakeServer), o, tc.f, "", &buf); err != nil {
				t.Fatalf("runNetworkList: %v", err)
			}
			if !strings.Contains(buf.String(), "pv") {
				t.Errorf("output missing the network:\n%s", buf.String())
			}
		})
	}
}

func TestPZNetworkList_PVLANThroughCobraAndExplained(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	pzExtensions(t, fakeServer, "/v2.0/extensions", "router", "qos")
	fakeServer.Mux.HandleFunc("/v2.0/networks", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("pvlan") != "true" {
			t.Errorf("query = %v, want pvlan=true", r.URL.Query())
		}
		writeJSON(t, w, http.StatusBadRequest, `{"NeutronError":{"message":"[pvlan] is invalid attribute for filtering"}}`)
	})
	_, err := execNetwork(t, fakeServer, "network", "list", "--pvlan")
	if err == nil || !strings.Contains(err.Error(), "pvlan (for pvlan)") {
		t.Fatalf("error does not name the pvlan extension: %v", err)
	}
	assertOriginalHTTPError(t, err)

	if _, err := execNetwork(t, fakeServer, "network", "list", "--pvlan", "--no-pvlan"); err == nil {
		t.Error("--pvlan with --no-pvlan must be rejected")
	}
}

// --- network create ----------------------------------------------------------

func TestPZNetworkCreate_PVLANAndQinQBody(t *testing.T) {
	for _, tc := range []struct {
		name string
		f    *networkCreateFlags
		body string
	}{
		{"on", &networkCreateFlags{pvlan: true, qinqVLAN: true},
			`{"network":{"name":"n1","admin_state_up":true,"pvlan":true,"qinq":true}}`},
		{"off", &networkCreateFlags{noPVLAN: true, noQinQVLAN: true},
			`{"network":{"name":"n1","admin_state_up":true,"pvlan":false,"qinq":false}}`},
		// Transparent VLAN is only incompatible with QinQ *enabled*.
		{"transparent-without-qinq", &networkCreateFlags{transparentVLAN: true, noQinQVLAN: true},
			`{"network":{"name":"n1","admin_state_up":true,"vlan_transparent":true,"qinq":false}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()
			fakeServer.Mux.HandleFunc("/networks", func(w http.ResponseWriter, r *http.Request) {
				th.TestMethod(t, r, http.MethodPost)
				th.TestJSONRequest(t, r, tc.body)
				writeJSON(t, w, http.StatusCreated, `{"network":{"id":"`+pzNetID+`","name":"n1","pvlan":true,"qinq":true}}`)
			})
			var buf bytes.Buffer
			o := &output.Options{Format: "json", Columns: []string{"pvlan", "is_vlan_qinq"}}
			if err := runNetworkCreate(context.Background(), networkClient(fakeServer), o, "n1", tc.f, &buf); err != nil {
				t.Fatalf("runNetworkCreate: %v", err)
			}
			got := pzJSONSingle(t, &buf)
			if got["pvlan"] != true || got["is_vlan_qinq"] != true {
				t.Errorf("show fields = %v, want pvlan and is_vlan_qinq true", got)
			}
		})
	}
}

func TestPZNetworkCreate_Exclusions(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	pzNoRequest(t, fakeServer, "/networks")
	client := networkClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}
	for name, tc := range map[string]struct {
		f    *networkCreateFlags
		want string
	}{
		"transparent+qinq": {&networkCreateFlags{transparentVLAN: true, qinqVLAN: true}, "--transparent-vlan and --qinq-vlan"},
		"no-portsec+pvlan": {&networkCreateFlags{disablePortSecurity: true, pvlan: true}, "--disable-port-security and --pvlan"},
	} {
		var buf bytes.Buffer
		err := runNetworkCreate(context.Background(), client, o, "n1", tc.f, &buf)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to mention %q", name, err, tc.want)
		}
	}
	for _, argv := range [][]string{
		{"network", "create", "n1", "--qinq-vlan", "--no-qinq-vlan"},
		{"network", "create", "n1", "--pvlan", "--no-pvlan"},
	} {
		if _, err := execNetwork(t, fakeServer, argv...); err == nil {
			t.Errorf("%v: expected a mutual-exclusion error", argv)
		}
	}
}

func TestPZNetworkCreate_ExplainsMissingExtension(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	// Neither pvlan nor qinq is enabled; port-security is.
	pzExtensions(t, fakeServer, "/v2.0/extensions", "port-security", "router")
	fakeServer.Mux.HandleFunc("/v2.0/networks", func(w http.ResponseWriter, r *http.Request) {
		th.TestJSONRequest(t, r, `{"network":{"name":"n1","admin_state_up":true,"pvlan":true,"qinq":true,"port_security_enabled":true}}`)
		writeJSON(t, w, http.StatusBadRequest, `{"NeutronError":{"message":"Unrecognized attribute(s) 'pvlan, qinq'"}}`)
	})
	_, err := execNetwork(t, fakeServer, "network", "create", "n1", "--pvlan", "--qinq-vlan", "--enable-port-security")
	if err == nil {
		t.Fatal("expected the mock 400")
	}
	msg := err.Error()
	if !strings.Contains(msg, "pvlan (for pvlan); qinq (for qinq)") {
		t.Errorf("error does not name both missing extensions:\n%s", msg)
	}
	if strings.Contains(msg, "port-security (") {
		t.Errorf("error blames an enabled extension:\n%s", msg)
	}
	assertOriginalHTTPError(t, err)
}

// --- network set / unset / show ----------------------------------------------

func TestPZNetworkSet_PVLANBody(t *testing.T) {
	for _, tc := range []struct {
		name  string
		f     *networkSetFlags
		flags fakeFlags
		body  string
	}{
		{"pvlan", &networkSetFlags{pvlan: true}, fakeFlags{netFlagPVLAN: true}, `{"network":{"pvlan":true}}`},
		{"no-pvlan", &networkSetFlags{noPVLAN: true}, fakeFlags{netFlagNoPVLAN: true}, `{"network":{"pvlan":false}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()
			echoLookup(t, fakeServer, "/networks", "networks")
			fakeServer.Mux.HandleFunc("/networks/net-1", func(w http.ResponseWriter, r *http.Request) {
				th.TestMethod(t, r, http.MethodPut)
				th.TestJSONRequest(t, r, tc.body)
				writeJSON(t, w, http.StatusOK, `{"network":{"id":"net-1"}}`)
			})
			var buf bytes.Buffer
			o := &output.Options{Format: output.FormatValue}
			if err := runNetworkSet(context.Background(), networkClient(fakeServer), o, "net-1", tc.f, tc.flags, &buf); err != nil {
				t.Fatalf("runNetworkSet: %v", err)
			}
		})
	}
}

func TestPZNetworkSet_PVLANPortSecurityExclusion(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	echoLookup(t, fakeServer, "/networks", "networks")
	pzNoRequest(t, fakeServer, "/networks/net-1")
	client := networkClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}

	f := &networkSetFlags{disablePortSecurity: true, pvlan: true}
	err := runNetworkSet(context.Background(), client, o, "net-1", f,
		fakeFlags{flagDisablePortSecurity: true, netFlagPVLAN: true}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "--disable-port-security and --pvlan") {
		t.Errorf("flags: err = %v", err)
	}

	// On set, upstream checks after merging --extra-property, so an extra
	// property that disables port security conflicts too.
	f = &networkSetFlags{pvlan: true, extraProperty: []string{"type=bool,name=port_security_enabled,value=false"}}
	err = runNetworkSet(context.Background(), client, o, "net-1", f, fakeFlags{netFlagPVLAN: true}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "--disable-port-security and --pvlan") {
		t.Errorf("extra property: err = %v", err)
	}
}

func TestPZNetworkSetAndUnset_ExplainMissingExtension(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	pzExtensions(t, fakeServer, "/extensions", "router")
	echoLookup(t, fakeServer, "/networks", "networks")
	fakeServer.Mux.HandleFunc("/networks/net-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		writeJSON(t, w, http.StatusBadRequest, `{"NeutronError":{"message":"Unrecognized attribute(s)"}}`)
	})
	client := networkClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}

	err := runNetworkSet(context.Background(), client, o, "net-1", &networkSetFlags{pvlan: true, dnsDomain: "example.com."},
		fakeFlags{netFlagPVLAN: true, flagDNSDomain: true}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "dns-integration (for dns_domain); pvlan (for pvlan)") {
		t.Errorf("set: err = %v", err)
	}
	assertOriginalHTTPError(t, err)

	err = runNetworkUnset(context.Background(), client, o, "net-1",
		&networkUnsetFlags{extraProperty: []string{"name=qos_policy_id"}}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "qos (for qos_policy_id)") {
		t.Errorf("unset: err = %v", err)
	}
}

func TestPZNetworkShow_PostZedFieldsEmptyWhenAbsent(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	echoLookup(t, fakeServer, "/networks", "networks")
	fakeServer.Mux.HandleFunc("/networks/net-1", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, `{"network":{"id":"net-1","name":"old"}}`)
	})
	fakeServer.Mux.HandleFunc("/networks/net-2", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, `{"network":{"id":"net-2","pvlan":false,"qinq":true}}`)
	})
	o := &output.Options{Format: "json", Columns: []string{"pvlan", "is_vlan_qinq"}}
	for id, want := range map[string]map[string]any{
		"net-1": {"pvlan": nil, "is_vlan_qinq": nil},
		"net-2": {"pvlan": false, "is_vlan_qinq": true},
	} {
		var buf bytes.Buffer
		if err := runNetworkShow(context.Background(), networkClient(fakeServer), o, id, &buf); err != nil {
			t.Fatalf("runNetworkShow %s: %v", id, err)
		}
		got := pzJSONSingle(t, &buf)
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s: %s = %#v, want %#v (all: %v)", id, k, got[k], v, got)
			}
		}
	}
}

// --- subnet ------------------------------------------------------------------

func TestPZSubnetSet_LeakRoutesBodyAndShow(t *testing.T) {
	for _, tc := range []struct {
		name  string
		f     *subnetSetFlags
		flags fakeFlags
		body  string
		want  bool
	}{
		{"leak", &subnetSetFlags{leakRoutes: true}, fakeFlags{subnetFlagLeakRoutes: true}, `{"subnet":{"leak_routes":true}}`, true},
		{"no-leak", &subnetSetFlags{noLeakRoutes: true}, fakeFlags{subnetFlagNoLeakRoutes: true}, `{"subnet":{"leak_routes":false}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()
			echoLookup(t, fakeServer, "/subnets", "subnets")
			fakeServer.Mux.HandleFunc("/subnets/sub-1", func(w http.ResponseWriter, r *http.Request) {
				th.TestMethod(t, r, http.MethodPut)
				th.TestJSONRequest(t, r, tc.body)
				writeJSON(t, w, http.StatusOK, `{"subnet":{"id":"sub-1","leak_routes":`+strconv.FormatBool(tc.want)+`}}`)
			})
			var buf bytes.Buffer
			o := &output.Options{Format: "json", Columns: []string{"leak_routes"}}
			if err := runSubnetSet(context.Background(), networkClient(fakeServer), o, "sub-1", tc.f, tc.flags, &buf); err != nil {
				t.Fatalf("runSubnetSet: %v", err)
			}
			if got := pzJSONSingle(t, &buf); got["leak_routes"] != tc.want {
				t.Errorf("leak_routes = %#v, want %v", got["leak_routes"], tc.want)
			}
		})
	}
}

func TestPZSubnetSet_LeakRoutesThroughCobra(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	echoLookup(t, fakeServer, "/v2.0/subnets", "subnets")
	fakeServer.Mux.HandleFunc("/v2.0/subnets/sub-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestJSONRequest(t, r, `{"subnet":{"leak_routes":false}}`)
		writeJSON(t, w, http.StatusOK, `{"subnet":{"id":"sub-1","leak_routes":false}}`)
	})
	if _, err := execNetwork(t, fakeServer, "subnet", "set", "sub-1", "--no-leak-routes"); err != nil {
		t.Fatalf("subnet set --no-leak-routes: %v", err)
	}
	if _, err := execNetwork(t, fakeServer, "subnet", "set", "sub-1", "--leak-routes", "--no-leak-routes"); err == nil {
		t.Error("--leak-routes with --no-leak-routes must be rejected")
	}
	if _, err := execNetwork(t, fakeServer, "subnet", "create", "s1", "--network", "n", "--leak-routes"); err == nil {
		t.Error("subnet create must not accept --leak-routes (set-only upstream)")
	}
}

func TestPZSubnetSet_ExplainsMissingExtension(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	pzExtensions(t, fakeServer, "/extensions", "subnet-dns-publish-fixed-ip")
	echoLookup(t, fakeServer, "/subnets", "subnets")
	fakeServer.Mux.HandleFunc("/subnets/sub-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		writeJSON(t, w, http.StatusBadRequest, `{"NeutronError":{"message":"Unrecognized attribute(s) 'leak_routes'"}}`)
	})
	f := &subnetSetFlags{leakRoutes: true, dnsPublishFixedIP: true}
	err := runSubnetSet(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatValue},
		"sub-1", f, fakeFlags{subnetFlagLeakRoutes: true, subnetFlagDNSPublishFixedIP: true}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "ovn-bgp (for leak_routes)") {
		t.Fatalf("error does not name ovn-bgp: %v", err)
	}
	if strings.Contains(err.Error(), "subnet-dns-publish-fixed-ip (") {
		t.Errorf("error blames an enabled extension: %v", err)
	}
	assertOriginalHTTPError(t, err)
}

func TestPZSubnetCreate_ExplainsTypedExtensionAttribute(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	pzExtensions(t, fakeServer, "/extensions", "router")
	echoLookup(t, fakeServer, "/networks", "networks")
	fakeServer.Mux.HandleFunc("/subnets", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		writeJSON(t, w, http.StatusBadRequest, `{"NeutronError":{"message":"Unrecognized attribute(s) 'dns_publish_fixed_ip'"}}`)
	})
	f := &subnetCreateFlags{network: "net-1", subnetRange: "192.0.2.0/24", ipVersion: 4,
		dnsPublishFixedIP: true, serviceType: []string{"network:router_gateway"}}
	err := runSubnetCreate(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatValue},
		"s1", f, &bytes.Buffer{})
	want := "subnet-dns-publish-fixed-ip (for dns_publish_fixed_ip); subnet-service-types (for service_types)"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want it to contain %q", err, want)
	}
	assertOriginalHTTPError(t, err)
}

func TestPZSubnetUnset_ExplainsMissingExtension(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	pzExtensions(t, fakeServer, "/extensions", "router")
	echoLookup(t, fakeServer, "/subnets", "subnets")
	fakeServer.Mux.HandleFunc("/subnets/sub-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeJSON(t, w, http.StatusOK, `{"subnet":{"id":"sub-1","service_types":["network:router_gateway"],"revision_number":3}}`)
			return
		}
		th.TestJSONRequest(t, r, `{"subnet":{"service_types":[]}}`)
		writeJSON(t, w, http.StatusBadRequest, `{"NeutronError":{"message":"Unrecognized attribute(s) 'service_types'"}}`)
	})
	f := &subnetUnsetFlags{serviceType: []string{"network:router_gateway"}}
	err := runSubnetUnset(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatValue},
		"sub-1", f, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "subnet-service-types (for service_types)") {
		t.Fatalf("err = %v", err)
	}
}

func TestPZSubnetShow_LeakRoutesEmptyWhenAbsent(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	echoLookup(t, fakeServer, "/subnets", "subnets")
	fakeServer.Mux.HandleFunc("/subnets/sub-1", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, `{"subnet":{"id":"sub-1","name":"s1","cidr":"192.0.2.0/24"}}`)
	})
	var buf bytes.Buffer
	o := &output.Options{Format: "json", Columns: []string{"name", "leak_routes"}}
	if err := runSubnetShow(context.Background(), networkClient(fakeServer), o, "sub-1", &buf); err != nil {
		t.Fatalf("runSubnetShow: %v", err)
	}
	got := pzJSONSingle(t, &buf)
	if got["name"] != "s1" || got["leak_routes"] != nil {
		t.Errorf("show = %v, want name s1 and an empty leak_routes", got)
	}
}

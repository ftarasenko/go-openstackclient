package network

import (
	"bytes"
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// serveExtensions answers GET /extensions/<alias> with 200 for each enabled
// alias and 404 for any other, and GET /extensions with the enabled list, so a
// test sees exactly which extensions a write consulted.
func serveExtensions(t *testing.T, fakeServer th.FakeServer, prefix string, enabled ...string) *[]string {
	t.Helper()
	var asked []string
	fakeServer.Mux.HandleFunc(prefix+"/extensions/", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		alias := strings.TrimPrefix(r.URL.Path, prefix+"/extensions/")
		asked = append(asked, alias)
		for _, e := range enabled {
			if e == alias {
				writeJSON(t, w, http.StatusOK, `{"extension":{"alias":"`+alias+`"}}`)
				return
			}
		}
		writeJSON(t, w, http.StatusNotFound, `{"NeutronError":{"message":"Extension `+alias+` not found."}}`)
	})
	fakeServer.Mux.HandleFunc(prefix+"/extensions", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		body := `{"extensions":[`
		for i, e := range enabled {
			if i > 0 {
				body += ","
			}
			body += `{"alias":"` + e + `"}`
		}
		writeJSON(t, w, http.StatusOK, body+`]}`)
	})
	return &asked
}

// serveNetwork answers GET /networks/<id> with the given pvlan flag.
func serveNetwork(t *testing.T, fakeServer th.FakeServer, path, pvlan string) {
	t.Helper()
	fakeServer.Mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		writeJSON(t, w, http.StatusOK, `{"network":{"id":"net-1","pvlan":`+pvlan+`}}`)
	})
}

// Every post-Zed create attribute in one body: the hint alias is expanded into
// neutron's nested hints, both hint extensions are checked first, and the PVLAN
// attributes make the network be read for its pvlan flag.
func TestRunPortCreate_PostZedAttributes(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	emptyLookup(t, fakeServer, "/networks", "networks")
	serveNetwork(t, fakeServer, "/networks/net-1", "true")
	asked := serveExtensions(t, fakeServer, "", "port-hints", "port-hint-ovs-tx-steering")
	fakeServer.Mux.HandleFunc("/ports", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		th.TestJSONRequest(t, r, `{"port":{
		  "name":"p1","network_id":"net-1",
		  "trusted":true,"numa_affinity_policy":"socket",
		  "hints":{"openvswitch":{"other_config":{"tx-steering":"hash"}}},
		  "device_profile":"dp-1","hardware_offload_type":"switchdev",
		  "pvlan_type":"community","pvlan_community":"blue"}}`)
		writeJSON(t, w, http.StatusCreated, `{"port":{"id":"port-1","name":"p1","trusted":true,
		  "numa_affinity_policy":"socket","device_profile":"dp-1","hardware_offload_type":"switchdev",
		  "hints":{"openvswitch":{"other_config":{"tx-steering":"hash"}}},
		  "pvlan_type":"community","pvlan_community":"blue","tags":[]}}`)
	})
	f := &portCreateFlags{
		network: "net-1", deviceProfile: "dp-1", hardwareOffloadType: "switchdev",
		portAttrFlags: portAttrFlags{portPostZedFlags: portPostZedFlags{
			trusted: true, numaSocket: true, hint: []string{"ovs-tx-steering=hash"},
			pvlanType: "community", pvlanCommunity: "blue",
		}},
	}
	flags := fakeFlags{flagPortPVLANType: true, flagPortPVLANCommunity: true}
	var buf bytes.Buffer
	if err := runPortCreate(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatJSON},
		"p1", f, flags, &buf); err != nil {
		t.Fatalf("runPortCreate: %v", err)
	}
	if want := []string{"port-hints", "port-hint-ovs-tx-steering"}; !reflect.DeepEqual(*asked, want) {
		t.Errorf("extensions checked = %v, want %v", *asked, want)
	}
	for _, want := range []string{
		`"trusted": true`, `"numa_affinity_policy": "socket"`, `"device_profile": "dp-1"`,
		`"hardware_offload_type": "switchdev"`, `"tx-steering": "hash"`,
		`"pvlan_type": "community"`, `"pvlan_community": "blue"`,
	} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %s:\n%s", want, buf.String())
		}
	}
}

// The RunE-registered flags reach the body; --pvlan-community is sent even when
// empty (upstream's "is not None"), and an empty community alone needs no
// network check.
func TestExec_PortCreate_PostZedFlagsReachTheBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	const netID = "11111111-1111-1111-1111-111111111111"
	emptyLookup(t, fakeServer, "/v2.0/networks", "networks")
	fakeServer.Mux.HandleFunc("/v2.0/ports", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		th.TestJSONRequest(t, r, `{"port":{"name":"p1","network_id":"`+netID+`",
		  "trusted":false,"numa_affinity_policy":"preferred","device_profile":"dp-2",
		  "hardware_offload_type":"switchdev","pvlan_community":""}}`)
		writeJSON(t, w, http.StatusCreated, `{"port":{"id":"port-1","name":"p1","tags":[]}}`)
	})
	out, err := execNetwork(t, fakeServer, "port", "create", "p1", "--network", netID,
		"--not-trusted", "--numa-policy-preferred", "--device-profile", "dp-2",
		"--hardware-offload-type", "switchdev", "--pvlan-community", "")
	if err != nil {
		t.Fatalf("port create: %v (output %q)", err, out)
	}
}

func TestExec_PortPostZedExclusions(t *testing.T) {
	for name, argv := range map[string][]string{
		"trusted":  {"port", "create", "p1", "--network", "n", "--trusted", "--not-trusted"},
		"numa":     {"port", "set", "p1", "--numa-policy-required", "--numa-policy-legacy"},
		"numa 3":   {"port", "create", "p1", "--network", "n", "--numa-policy-socket", "--numa-policy-preferred"},
		"pvlan":    {"port", "list", "--pvlan", "--no-pvlan"},
		"set only": {"port", "set", "p1", "--device-profile", "dp"},
	} {
		t.Run(name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()
			if _, err := execNetwork(t, fakeServer, argv...); err == nil {
				t.Fatalf("%v: expected an error", argv)
			}
		})
	}
}

// set sends the post-Zed attributes; the JSON hint form passes through as is,
// and a PVLAN change reads the port's own network (and pins the revision it
// read).
func TestRunPortSet_PostZedAttributes(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	emptyLookup(t, fakeServer, "/ports", "ports")
	serveNetwork(t, fakeServer, "/networks/net-1", "true")
	serveExtensions(t, fakeServer, "", "port-hints", "port-hint-ovs-tx-steering")
	var gotIfMatch string
	fakeServer.Mux.HandleFunc("/ports/port-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeJSON(t, w, http.StatusOK, `{"port":{"id":"port-1","network_id":"net-1","revision_number":7}}`)
			return
		}
		th.TestMethod(t, r, http.MethodPut)
		gotIfMatch = r.Header.Get("If-Match")
		th.TestJSONRequest(t, r, `{"port":{"trusted":false,"numa_affinity_policy":"required",
		  "hints":{"openvswitch":{"other_config":{"tx-steering":"thread"}}},"pvlan_type":"isolated"}}`)
		writeJSON(t, w, http.StatusOK, `{"port":{"id":"port-1","pvlan_type":"isolated"}}`)
	})
	f := &portSetFlags{portAttrFlags: portAttrFlags{portPostZedFlags: portPostZedFlags{
		notTrusted: true, numaRequired: true, pvlanType: "isolated",
		hint: []string{`{"openvswitch":{"other_config":{"tx-steering":"thread"}}}`},
	}}}
	if err := runPortSet(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatValue},
		"port-1", f, fakeFlags{flagPortPVLANType: true}, &bytes.Buffer{}); err != nil {
		t.Fatalf("runPortSet: %v", err)
	}
	if gotIfMatch != "revision_number=7" {
		t.Errorf("If-Match = %q, want revision_number=7", gotIfMatch)
	}
}

func TestExec_PortSet_PostZedFlagsReachTheBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	const portID = "33333333-3333-3333-3333-333333333333"
	emptyLookup(t, fakeServer, "/v2.0/ports", "ports")
	serveExtensions(t, fakeServer, "/v2.0", "port-hints", "port-hint-ovs-tx-steering")
	fakeServer.Mux.HandleFunc("/v2.0/ports/"+portID, func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"port":{"trusted":true,"numa_affinity_policy":"legacy",
		  "hints":{"openvswitch":{"other_config":{"tx-steering":"thread"}}}}}`)
		writeJSON(t, w, http.StatusOK, `{"port":{"id":"`+portID+`"}}`)
	})
	out, err := execNetwork(t, fakeServer, "port", "set", portID, "--trusted", "--numa-policy-legacy",
		"--hint", "ovs-tx-steering=thread")
	if err != nil {
		t.Fatalf("port set: %v (output %q)", err, out)
	}
}

func TestRunPortUnset_PostZedClears(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	emptyLookup(t, fakeServer, "/ports", "ports")
	fakeServer.Mux.HandleFunc("/ports/port-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeJSON(t, w, http.StatusOK, `{"port":{"id":"port-1","revision_number":2,"numa_affinity_policy":"required"}}`)
			return
		}
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"port":{"numa_affinity_policy":null,"hints":null,"pvlan_community":null}}`)
		writeJSON(t, w, http.StatusOK, `{"port":{"id":"port-1"}}`)
	})
	f := &portUnsetFlags{numaPolicy: true, hints: true, pvlanCommunity: true}
	if err := runPortUnset(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatValue},
		"port-1", f, &bytes.Buffer{}); err != nil {
		t.Fatalf("runPortUnset: %v", err)
	}
}

func TestExec_PortUnset_PostZedFlagsAreRegistered(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	const portID = "33333333-3333-3333-3333-333333333333"
	emptyLookup(t, fakeServer, "/v2.0/ports", "ports")
	fakeServer.Mux.HandleFunc("/v2.0/ports/"+portID, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeJSON(t, w, http.StatusOK, `{"port":{"id":"`+portID+`","revision_number":1}}`)
			return
		}
		th.TestJSONRequest(t, r, `{"port":{"numa_affinity_policy":null,"hints":null,"pvlan_community":null}}`)
		writeJSON(t, w, http.StatusOK, `{"port":{"id":"`+portID+`"}}`)
	})
	out, err := execNetwork(t, fakeServer, "port", "unset", portID, "--numa-policy", "--hints", "--pvlan-community")
	if err != nil {
		t.Fatalf("port unset: %v (output %q)", err, out)
	}
}

func TestParsePortHints(t *testing.T) {
	thread := map[string]any{"openvswitch": map[string]any{"other_config": map[string]any{"tx-steering": "thread"}}}
	hash := map[string]any{"openvswitch": map[string]any{"other_config": map[string]any{"tx-steering": "hash"}}}
	for name, tc := range map[string]struct {
		specs   []string
		want    map[string]any
		wantErr bool
	}{
		"alias thread":     {specs: []string{"ovs-tx-steering=thread"}, want: thread},
		"alias hash":       {specs: []string{"ovs-tx-steering=hash"}, want: hash},
		"full json":        {specs: []string{`{"openvswitch":{"other_config":{"tx-steering":"hash"}}}`}, want: hash},
		"later wins":       {specs: []string{"ovs-tx-steering=hash", "ovs-tx-steering=thread"}, want: thread},
		"empty json":       {specs: []string{"{}"}, want: nil},
		"bad value":        {specs: []string{"ovs-tx-steering=round-robin"}, wantErr: true},
		"unknown alias":    {specs: []string{"ovs-rx-steering=thread"}, wantErr: true},
		"alias plus json":  {specs: []string{"ovs-tx-steering=hash", `{"openvswitch":{"other_config":{"tx-steering":"hash"}}}`}, wantErr: true},
		"extra json key":   {specs: []string{`{"openvswitch":{"other_config":{"tx-steering":"hash","x":"y"}}}`}, wantErr: true},
		"no equals":        {specs: []string{"ovs-tx-steering"}, wantErr: true},
		"non-string value": {specs: []string{`{"ovs-tx-steering":1}`}, wantErr: true},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := parsePortHints(tc.specs)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr && !reflect.DeepEqual(got, tc.want) {
				t.Errorf("hints = %#v, want %#v", got, tc.want)
			}
		})
	}
}

// Upstream refuses --hint before sending anything when either hint extension
// is absent.
func TestRunPortCreate_HintNeedsItsExtensions(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	emptyLookup(t, fakeServer, "/networks", "networks")
	serveExtensions(t, fakeServer, "", "port-hints")
	fakeServer.Mux.HandleFunc("/ports", func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected %s /ports", r.Method)
	})
	f := &portCreateFlags{network: "net-1", portAttrFlags: portAttrFlags{portPostZedFlags: portPostZedFlags{
		hint: []string{"ovs-tx-steering=thread"},
	}}}
	err := runPortCreate(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatValue},
		"p1", f, fakeFlags{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "port-hint-ovs-tx-steering") {
		t.Fatalf("err = %v, want a missing port-hint-ovs-tx-steering error", err)
	}
}

// upstream _validate_pvlan_port and _validate_pvlan_network_port; none of the
// rejected writes reaches neutron.
func TestRunPortCreate_PVLANValidation(t *testing.T) {
	for name, tc := range map[string]struct {
		pz       portPostZedFlags
		flags    fakeFlags
		insecure bool
		pvlanNet string
		wantErr  string
	}{
		"port security off": {
			pz: portPostZedFlags{pvlanType: "isolated"}, flags: fakeFlags{flagPortPVLANType: true, flagDisablePortSecurity: true},
			insecure: true, wantErr: "port security is disabled",
		},
		"community needs a name": {
			pz: portPostZedFlags{pvlanType: "community"}, flags: fakeFlags{flagPortPVLANType: true},
			wantErr: "--pvlan-community is required",
		},
		"unknown type": {
			pz: portPostZedFlags{pvlanType: "private"}, flags: fakeFlags{flagPortPVLANType: true},
			wantErr: "invalid --pvlan-type",
		},
		"network without pvlan": {
			pz: portPostZedFlags{pvlanCommunity: "blue"}, flags: fakeFlags{flagPortPVLANCommunity: true},
			pvlanNet: "false", wantErr: "does not have PVLAN enabled",
		},
	} {
		t.Run(name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()
			emptyLookup(t, fakeServer, "/networks", "networks")
			if tc.pvlanNet != "" {
				serveNetwork(t, fakeServer, "/networks/net-1", tc.pvlanNet)
			}
			fakeServer.Mux.HandleFunc("/ports", func(_ http.ResponseWriter, r *http.Request) {
				t.Errorf("unexpected %s /ports", r.Method)
			})
			f := &portCreateFlags{network: "net-1", disablePortSecurity: tc.insecure,
				portAttrFlags: portAttrFlags{portPostZedFlags: tc.pz}}
			err := runPortCreate(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatValue},
				"p1", f, tc.flags, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

// --pvlan-type/--pvlan-community are server-side filters, --pvlan filters the
// result client-side, and any of them adds upstream's PVLAN columns.
func TestExec_PortList_PVLANFilters(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/v2.0/ports", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		want := map[string][]string{"pvlan_type": {"community"}, "pvlan_community": {"blue"}}
		if q := map[string][]string(r.URL.Query()); !reflect.DeepEqual(q, want) {
			t.Errorf("query = %v, want %v", q, want)
		}
		writeJSON(t, w, http.StatusOK, `{"ports":[
		  {"id":"port-a","name":"a","pvlan_type":"community","pvlan_community":"blue"},
		  {"id":"port-b","name":"b","pvlan_type":null,"pvlan_community":null}]}`)
	})
	out, err := execNetwork(t, fakeServer, "port", "list", "--pvlan-type", "community",
		"--pvlan-community", "blue", "--pvlan")
	if err != nil {
		t.Fatalf("port list: %v (output %q)", err, out)
	}
	for _, want := range []string{"PVLAN Type", "PVLAN Community", "port-a", "community", "blue"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "port-b") {
		t.Errorf("--pvlan kept a port without pvlan_type:\n%s", out)
	}
}

func TestRunPortList_NoPVLANKeepsOnlyPlainPorts(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/ports", func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Query()) != 0 {
			t.Errorf("--no-pvlan sent a query: %v", r.URL.Query())
		}
		writeJSON(t, w, http.StatusOK, `{"ports":[
		  {"id":"port-a","pvlan_type":"isolated"},{"id":"port-b"}]}`)
	})
	var buf bytes.Buffer
	if err := runPortList(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatValue},
		&portListFlags{noPVLAN: true}, portListDeps{}, &buf); err != nil {
		t.Fatalf("runPortList: %v", err)
	}
	if strings.Contains(buf.String(), "port-a") || !strings.Contains(buf.String(), "port-b") {
		t.Errorf("--no-pvlan output:\n%s", buf.String())
	}
}

// A 400 on a cloud without the extensions names them: the value-keyed socket
// policy, the typed-opts uplink attribute, and the port-specific dns_domain.
func TestRunPortCreate_ExplainsMissingExtensions(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	emptyLookup(t, fakeServer, "/networks", "networks")
	serveExtensions(t, fakeServer, "", "port-numa-affinity-policy", "dns-integration", "binding")
	fakeServer.Mux.HandleFunc("/ports", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		writeJSON(t, w, http.StatusBadRequest, `{"NeutronError":{"message":"Unrecognized attribute(s) 'trusted'"}}`)
	})
	f := &portCreateFlags{network: "net-1", portAttrFlags: portAttrFlags{
		dnsDomain: "example.com.", vnicType: "direct", enableUplink: true,
		portPostZedFlags: portPostZedFlags{trusted: true, numaSocket: true},
	}}
	err := runPortCreate(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatValue},
		"p1", f, fakeFlags{flagDNSDomain: true, flagPortEnableUplink: true}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected the 400")
	}
	msg := err.Error()
	for _, want := range []string{
		"creating port:", "port-trusted-vif (for trusted)",
		"port-numa-affinity-policy-socket (for numa_affinity_policy=socket)",
		"uplink-status-propagation (for propagate_uplink_status)",
		"dns-domain-ports (for port.dns_domain)",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error missing %q:\n%s", want, msg)
		}
	}
	for _, blamed := range []string{"port-numa-affinity-policy (", "dns-integration", "binding ("} {
		if strings.Contains(msg, blamed) {
			t.Errorf("error blames an enabled extension %q:\n%s", blamed, msg)
		}
	}
}

// set and unset share the update path, so both are explained.
func TestRunPortUnset_ExplainsMissingExtension(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	emptyLookup(t, fakeServer, "/ports", "ports")
	serveExtensions(t, fakeServer, "")
	fakeServer.Mux.HandleFunc("/ports/port-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeJSON(t, w, http.StatusOK, `{"port":{"id":"port-1","revision_number":1}}`)
			return
		}
		writeJSON(t, w, http.StatusBadRequest, `{"NeutronError":{"message":"Unrecognized attribute(s) 'hints'"}}`)
	})
	err := runPortUnset(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatValue},
		"port-1", &portUnsetFlags{hints: true, host: true}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected the 400")
	}
	for _, want := range []string{"updating port port-1:", "port-hints (for hints)", "binding (for binding:host_id)"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q:\n%s", want, err)
		}
	}
}

func TestRunPortShow_RendersPostZedAttributes(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	emptyLookup(t, fakeServer, "/ports", "ports")
	fakeServer.Mux.HandleFunc("/ports/port-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		writeJSON(t, w, http.StatusOK, `{"port":{"id":"port-1","trusted":false,
		  "hardware_offload_type":"switchdev","numa_affinity_policy":"legacy","device_profile":"dp-1",
		  "hints":{"openvswitch":{"other_config":{"tx-steering":"thread"}}},
		  "pvlan_type":"promiscuous","pvlan_community":null,"ip_allocation":"immediate",
		  "resource_request":{"request_groups":[]}}}`)
	})
	var buf bytes.Buffer
	if err := runPortShow(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatJSON},
		"port-1", &buf); err != nil {
		t.Fatalf("runPortShow: %v", err)
	}
	for _, want := range []string{
		`"trusted": false`, `"hardware_offload_type": "switchdev"`, `"numa_affinity_policy": "legacy"`,
		`"device_profile": "dp-1"`, `"tx-steering": "thread"`, `"pvlan_type": "promiscuous"`,
		`"pvlan_community": null`, `"ip_allocation": "immediate"`, `"request_groups"`,
	} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %s:\n%s", want, buf.String())
		}
	}
}

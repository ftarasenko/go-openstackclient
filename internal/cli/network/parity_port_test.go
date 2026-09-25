package network

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Every attribute upstream's CreatePort sends that koc lacked, in one body;
// tags follow as the sub-resource PUT.
func TestRunPortCreate_SendsEveryNewAttributeThenTags(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	emptyLookup(t, fakeServer, "/networks", "networks")
	emptyLookup(t, fakeServer, "/qos/policies", "policies")
	fakeServer.Mux.HandleFunc("/ports", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		th.TestJSONRequest(t, r, `{"port":{
		  "name":"p1","network_id":"net-1","device_id":"dev-1","project_id":"proj-1",
		  "propagate_uplink_status":false,
		  "binding:host_id":"cmp-1","binding:vnic_type":"direct",
		  "binding:profile":{"pci_slot":"0000:03:00.1","physical_network":"physnet1","capabilities":["switchdev"]},
		  "qos_policy_id":"qos-1","dns_name":"web","dns_domain":"example.com.",
		  "extra_dhcp_opts":[
		    {"opt_name":"domain-search","opt_value":"a.example.com,b.example.com","ip_version":4},
		    {"opt_name":"bootfile-name"}],
		  "fixed_ips":[],
		  "custom":true}}`)
		writeJSON(t, w, http.StatusCreated, `{"port":{"id":"port-1","name":"p1","network_id":"net-1",
		  "binding:vnic_type":"direct","binding:host_id":"cmp-1","dns_name":"web","tags":[]}}`)
	})
	fakeServer.Mux.HandleFunc("/ports/port-1/tags", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"tags":["blue","red"]}`)
		writeJSON(t, w, http.StatusOK, `{"tags":["blue","red"]}`)
	})

	f := &portCreateFlags{
		network: "net-1", device: "dev-1", host: "cmp-1", projectID: "proj-1", noFixedIP: true,
		portAttrFlags: portAttrFlags{
			vnicType: "direct", qosPolicy: "qos-1", dnsName: "web", dnsDomain: "example.com.",
			disableUplink: true,
			bindingProfile: []string{
				"pci_slot=0000:03:00.1",
				`{"physical_network":"physnet1","capabilities":["switchdev"]}`,
			},
			extraDHCPOption: []string{
				"name=domain-search,value=a.example.com,b.example.com,ip-version=4",
				"name=bootfile-name",
			},
			extraProperty: []string{"type=bool,name=custom,value=true"},
		},
		tagWriteFlags: tagWriteFlags{tags: []string{"red", "blue"}},
	}
	flags := fakeFlags{flagDNSName: true, flagDNSDomain: true, flagPortDisableUplink: true}
	o := &output.Options{Format: output.FormatJSON}
	var buf bytes.Buffer
	if err := runPortCreate(context.Background(), networkClient(fakeServer), o, "p1", f, flags, &buf); err != nil {
		t.Fatalf("runPortCreate: %v", err)
	}
	for _, want := range []string{`"binding_vnic_type": "direct"`, `"binding_host_id": "cmp-1"`, `"dns_name": "web"`, `"blue"`} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %s:\n%s", want, buf.String())
		}
	}
}

func TestRunPortCreate_RejectsUnknownVNICType(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	emptyLookup(t, fakeServer, "/networks", "networks")
	f := &portCreateFlags{network: "net-1", portAttrFlags: portAttrFlags{vnicType: "fast"}}
	err := runPortCreate(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatValue},
		"p1", f, fakeFlags{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "invalid --vnic-type") {
		t.Fatalf("err = %v, want an invalid --vnic-type error", err)
	}
}

// The RunE-wired flags — --project in particular, which RunE resolves before the
// seam — reach the request body.
func TestExec_PortCreate_FlagsReachTheBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	const netID = "11111111-1111-1111-1111-111111111111"
	const projectID = "22222222-2222-2222-2222-222222222222"
	emptyLookup(t, fakeServer, "/v2.0/networks", "networks")
	fakeServer.Mux.HandleFunc("/v2.0/ports", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		th.TestJSONRequest(t, r, `{"port":{"name":"p1","network_id":"`+netID+`","project_id":"`+projectID+`",
		  "device_id":"dev-1","propagate_uplink_status":true,
		  "binding:host_id":"cmp-1","binding:vnic_type":"macvtap","binding:profile":{"k":"v"},
		  "dns_name":"","extra_dhcp_opts":[{"opt_name":"mtu","opt_value":"9000","ip_version":6}]}}`)
		writeJSON(t, w, http.StatusCreated, `{"port":{"id":"port-1","name":"p1","tags":[]}}`)
	})
	fakeServer.Mux.HandleFunc("/v2.0/ports/port-1/tags", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"tags":["a"]}`)
		writeJSON(t, w, http.StatusOK, `{"tags":["a"]}`)
	})
	out, err := execNetwork(t, fakeServer, "port", "create", "p1", "--network", netID,
		"--project", projectID, "--device", "dev-1", "--host", "cmp-1", "--vnic-type", "macvtap",
		"--binding-profile", "k=v", "--dns-name", "", "--enable-uplink-status-propagation",
		"--extra-dhcp-option", "name=mtu,value=9000,ip-version=6", "--tag", "a")
	if err != nil {
		t.Fatalf("port create: %v (output %q)", err, out)
	}
}

func TestExec_PortCreate_FixedIPAndNoFixedIPConflict(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	_, err := execNetwork(t, fakeServer, "port", "create", "p1", "--network", "n",
		"--fixed-ip", "subnet=s", "--no-fixed-ip")
	if err == nil || !strings.Contains(err.Error(), "no-fixed-ip") {
		t.Fatalf("err = %v, want a mutual-exclusion error", err)
	}
}

// upstream SetPort extends fixed IPs, security groups and allowed address pairs
// and merges binding:profile, starting from the port it just read; the PUT is
// pinned to that revision.
func TestRunPortSet_ExtendsListsAndMergesProfile(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	emptyLookup(t, fakeServer, "/ports", "ports")
	emptyLookup(t, fakeServer, "/subnets", "subnets")
	emptyLookup(t, fakeServer, "/security-groups", "security_groups")
	var gotIfMatch string
	puts := 0
	fakeServer.Mux.HandleFunc("/ports/port-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeJSON(t, w, http.StatusOK, `{"port":{"id":"port-1","revision_number":7,
			  "fixed_ips":[{"subnet_id":"sub-1","ip_address":"10.0.0.5"}],
			  "security_groups":["sg-1"],
			  "allowed_address_pairs":[{"ip_address":"10.0.0.50"}],
			  "binding:profile":{"keep":"me","over":"old"}}}`)
			return
		}
		puts++
		th.TestMethod(t, r, http.MethodPut)
		gotIfMatch = r.Header.Get("If-Match")
		th.TestJSONRequest(t, r, `{"port":{
		  "fixed_ips":[{"subnet_id":"sub-1","ip_address":"10.0.0.5"},{"subnet_id":"sub-2"}],
		  "security_groups":["sg-1","sg-2"],
		  "allowed_address_pairs":[{"ip_address":"10.0.0.50"},{"ip_address":"10.0.0.51"}],
		  "binding:profile":{"keep":"me","over":"new","add":"x"}}}`)
		writeJSON(t, w, http.StatusOK, `{"port":{"id":"port-1"}}`)
	})
	f := &portSetFlags{
		fixedIP:        []string{"subnet=sub-2"},
		securityGroup:  []string{"sg-2"},
		allowedAddress: []string{"ip-address=10.0.0.51"},
		portAttrFlags:  portAttrFlags{bindingProfile: []string{"over=new", `{"add":"x"}`}},
	}
	flags := fakeFlags{flagFixedIP: true, flagSecurityGroup: true, flagAllowedAddress: true}
	var buf bytes.Buffer
	if err := runPortSet(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatValue},
		"port-1", f, flags, &buf); err != nil {
		t.Fatalf("runPortSet: %v", err)
	}
	if puts != 1 {
		t.Errorf("port PUTs = %d, want 1", puts)
	}
	if gotIfMatch != "revision_number=7" {
		t.Errorf("If-Match = %q, want revision_number=7", gotIfMatch)
	}
}

// Each --no-* flag clears first, so combined with its list flag it overwrites,
// and alone it clears — neither needs to read the port.
func TestRunPortSet_NoFlagsOverwriteWithoutReading(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	emptyLookup(t, fakeServer, "/ports", "ports")
	emptyLookup(t, fakeServer, "/security-groups", "security_groups")
	fakeServer.Mux.HandleFunc("/ports/port-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		if got := r.Header.Get("If-Match"); got != "" {
			t.Errorf("If-Match = %q, want none when nothing was read", got)
		}
		th.TestJSONRequest(t, r, `{"port":{"fixed_ips":[],"security_groups":["sg-9"],
		  "allowed_address_pairs":[],"binding:profile":{"only":"this"}}}`)
		writeJSON(t, w, http.StatusOK, `{"port":{"id":"port-1"}}`)
	})
	f := &portSetFlags{
		noFixedIP: true, noSecurityGroup: true, securityGroup: []string{"sg-9"},
		noAllowedAddress: true, noBindingProfile: true,
		portAttrFlags: portAttrFlags{bindingProfile: []string{"only=this"}},
	}
	flags := fakeFlags{flagSecurityGroup: true}
	if err := runPortSet(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatValue},
		"port-1", f, flags, &bytes.Buffer{}); err != nil {
		t.Fatalf("runPortSet: %v", err)
	}
}

func TestRunPortSet_SendsScalarAndExtensionAttributes(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	emptyLookup(t, fakeServer, "/ports", "ports")
	emptyLookup(t, fakeServer, "/qos/policies", "policies")
	fakeServer.Mux.HandleFunc("/ports/port-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"port":{
		  "mac_address":"fa:16:3e:00:00:01","propagate_uplink_status":true,
		  "binding:vnic_type":"normal","qos_policy_id":"qos-1","dns_name":"db","dns_domain":"",
		  "extra_dhcp_opts":[{"opt_name":"mtu","opt_value":"1450"}],
		  "data_plane_status":"DOWN","custom":["a","b"]}}`)
		writeJSON(t, w, http.StatusOK, `{"port":{"id":"port-1","data_plane_status":"DOWN",
		  "qos_policy_id":"qos-1","propagate_uplink_status":true,
		  "extra_dhcp_opts":[{"opt_name":"mtu","opt_value":"1450","ip_version":4}]}}`)
	})
	f := &portSetFlags{
		macAddress: "fa:16:3e:00:00:01", dataPlaneStatus: "down",
		portAttrFlags: portAttrFlags{
			vnicType: "normal", qosPolicy: "qos-1", dnsName: "db", enableUplink: true,
			extraDHCPOption: []string{"name=mtu,value=1450"},
			extraProperty:   []string{"type=list,name=custom,value=a;b"},
		},
	}
	flags := fakeFlags{flagMACAddress: true, flagDNSName: true, flagDNSDomain: true, flagPortEnableUplink: true}
	var buf bytes.Buffer
	if err := runPortSet(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatTable},
		"port-1", f, flags, &buf); err != nil {
		t.Fatalf("runPortSet: %v", err)
	}
	for _, want := range []string{"data_plane_status", "DOWN", "qos-1", `"opt_name":"mtu"`} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q:\n%s", want, buf.String())
		}
	}
}

func TestRunPortSet_RejectsBadDataPlaneStatus(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	emptyLookup(t, fakeServer, "/ports", "ports")
	f := &portSetFlags{dataPlaneStatus: "BUILD"}
	err := runPortSet(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatValue},
		"port-1", f, fakeFlags{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "ACTIVE or DOWN") {
		t.Fatalf("err = %v, want a data-plane-status error", err)
	}
}

// Upstream skips the port PUT when tags are the only change.
func TestRunPortSet_TagsOnlySkipsTheUpdate(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	emptyLookup(t, fakeServer, "/ports", "ports")
	fakeServer.Mux.HandleFunc("/ports/port-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		writeJSON(t, w, http.StatusOK, `{"port":{"id":"port-1","tags":["old","gone"]}}`)
	})
	fakeServer.Mux.HandleFunc("/ports/port-1/tags", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"tags":["new"]}`)
		writeJSON(t, w, http.StatusOK, `{"tags":["new"]}`)
	})
	f := &portSetFlags{tagWriteFlags: tagWriteFlags{noTag: true, tags: []string{"new"}}}
	var buf bytes.Buffer
	if err := runPortSet(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatValue},
		"port-1", f, fakeFlags{}, &buf); err != nil {
		t.Fatalf("runPortSet: %v", err)
	}
	if !strings.Contains(buf.String(), "new") {
		t.Errorf("output does not show the new tag set:\n%s", buf.String())
	}
}

// --security-group and --no-security-group used to be mutually exclusive on
// set; upstream combines them to overwrite.
func TestExec_PortSet_NoSecurityGroupCombinesWithSecurityGroup(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	const portID = "33333333-3333-3333-3333-333333333333"
	emptyLookup(t, fakeServer, "/v2.0/ports", "ports")
	emptyLookup(t, fakeServer, "/v2.0/security-groups", "security_groups")
	fakeServer.Mux.HandleFunc("/v2.0/ports/"+portID, func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"port":{"security_groups":["sg-1"],"binding:vnic_type":"baremetal","data_plane_status":"ACTIVE"}}`)
		writeJSON(t, w, http.StatusOK, `{"port":{"id":"`+portID+`"}}`)
	})
	out, err := execNetwork(t, fakeServer, "port", "set", portID, "--no-security-group", "--security-group", "sg-1",
		"--vnic-type", "baremetal", "--data-plane-status", "ACTIVE")
	if err != nil {
		t.Fatalf("port set: %v (output %q)", err, out)
	}
}

func TestRunPortUnset_ClearsNewAttributesAndTags(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	emptyLookup(t, fakeServer, "/ports", "ports")
	var gotIfMatch string
	fakeServer.Mux.HandleFunc("/ports/port-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeJSON(t, w, http.StatusOK, `{"port":{"id":"port-1","revision_number":3,
			  "binding:profile":{"a":"1","b":"2","c":"3"},"tags":["x","y"]}}`)
			return
		}
		th.TestMethod(t, r, http.MethodPut)
		gotIfMatch = r.Header.Get("If-Match")
		th.TestJSONRequest(t, r, `{"port":{"device_id":"","device_owner":"",
		  "binding:profile":{"b":"2"},"qos_policy_id":null,"data_plane_status":null,"custom":null}}`)
		writeJSON(t, w, http.StatusOK, `{"port":{"id":"port-1","tags":["x","y"]}}`)
	})
	fakeServer.Mux.HandleFunc("/ports/port-1/tags", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"tags":["y"]}`)
		writeJSON(t, w, http.StatusOK, `{"tags":["y"]}`)
	})
	f := &portUnsetFlags{
		bindingProfile: []string{"a", "c"}, device: true, deviceOwner: true,
		qosPolicy: true, dataPlaneStatus: true, extraProperty: []string{"name=custom"},
		tagWriteFlags: tagWriteFlags{tags: []string{"x"}},
	}
	if err := runPortUnset(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatValue},
		"port-1", f, &bytes.Buffer{}); err != nil {
		t.Fatalf("runPortUnset: %v", err)
	}
	if gotIfMatch != "revision_number=3" {
		t.Errorf("If-Match = %q, want revision_number=3", gotIfMatch)
	}
}

// The old hand-written wrapper hid the revision guard whenever --host was
// given; the guarded adapter keeps it.
func TestRunPortUnset_HostKeepsTheRevisionGuard(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	emptyLookup(t, fakeServer, "/ports", "ports")
	var gotIfMatch string
	fakeServer.Mux.HandleFunc("/ports/port-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeJSON(t, w, http.StatusOK, `{"port":{"id":"port-1","revision_number":5}}`)
			return
		}
		gotIfMatch = r.Header.Get("If-Match")
		th.TestJSONRequest(t, r, `{"port":{"binding:host_id":""}}`)
		writeJSON(t, w, http.StatusOK, `{"port":{"id":"port-1"}}`)
	})
	if err := runPortUnset(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatValue},
		"port-1", &portUnsetFlags{host: true}, &bytes.Buffer{}); err != nil {
		t.Fatalf("runPortUnset: %v", err)
	}
	if gotIfMatch != "revision_number=5" {
		t.Errorf("If-Match = %q, want revision_number=5", gotIfMatch)
	}
}

func TestRunPortUnset_TagsOnlySkipsTheUpdate(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	emptyLookup(t, fakeServer, "/ports", "ports")
	fakeServer.Mux.HandleFunc("/ports/port-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		writeJSON(t, w, http.StatusOK, `{"port":{"id":"port-1","tags":["a"]}}`)
	})
	fakeServer.Mux.HandleFunc("/ports/port-1/tags", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"tags":[]}`)
		writeJSON(t, w, http.StatusOK, `{"tags":[]}`)
	})
	f := &portUnsetFlags{tagWriteFlags: tagWriteFlags{allTag: true}}
	if err := runPortUnset(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatValue},
		"port-1", f, &bytes.Buffer{}); err != nil {
		t.Fatalf("runPortUnset: %v", err)
	}
}

func TestRunPortUnset_MissingBindingProfileKeyErrors(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	emptyLookup(t, fakeServer, "/ports", "ports")
	fakeServer.Mux.HandleFunc("/ports/port-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		writeJSON(t, w, http.StatusOK, `{"port":{"id":"port-1","binding:profile":{"a":"1"}}}`)
	})
	f := &portUnsetFlags{bindingProfile: []string{"nope"}}
	err := runPortUnset(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatValue},
		"port-1", f, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), `binding:profile key "nope"`) {
		t.Fatalf("err = %v, want a missing-key error", err)
	}
}

// The tag filters now come from the shared helper; this proves the flags it
// registers reach the query through RunE.
func TestExec_PortList_TagFiltersReachTheQuery(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/v2.0/ports", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		q := r.URL.Query()
		want := map[string][]string{
			"tags": {"a,b"}, "tags-any": {"c"}, "not-tags": {"d"}, "not-tags-any": {"e"},
			"binding:host_id": {"cmp-1"},
		}
		if !reflect.DeepEqual(map[string][]string(q), want) {
			t.Errorf("query = %v, want %v", q, want)
		}
		writeJSON(t, w, http.StatusOK, `{"ports":[]}`)
	})
	out, err := execNetwork(t, fakeServer, "port", "list", "--tags", "a,b", "--any-tags", "c",
		"--not-tags", "d", "--not-any-tags", "e", "--host", "cmp-1")
	if err != nil {
		t.Fatalf("port list: %v (output %q)", err, out)
	}
}

func TestRunPortShow_RendersExtensionAttributes(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	emptyLookup(t, fakeServer, "/ports", "ports")
	fakeServer.Mux.HandleFunc("/ports/port-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		writeJSON(t, w, http.StatusOK, `{"port":{"id":"port-1",
		  "binding:host_id":"cmp-1","binding:vnic_type":"direct","binding:vif_type":"ovs",
		  "binding:profile":{"pci_slot":"0000:03:00.1"},
		  "qos_policy_id":"qos-1","dns_name":"web","dns_domain":"example.com.",
		  "dns_assignment":[{"hostname":"web","ip_address":"192.0.2.10","fqdn":"web.example.com."}],
		  "propagate_uplink_status":true,"data_plane_status":"ACTIVE","revision_number":4}}`)
	})
	o := &output.Options{Format: output.FormatJSON}
	var buf bytes.Buffer
	if err := runPortShow(context.Background(), networkClient(fakeServer), o, "port-1", &buf); err != nil {
		t.Fatalf("runPortShow: %v", err)
	}
	for _, want := range []string{
		`"binding_host_id": "cmp-1"`, `"binding_vnic_type": "direct"`, `"binding_vif_type": "ovs"`,
		`"pci_slot": "0000:03:00.1"`, `"qos_policy_id": "qos-1"`, `"dns_domain": "example.com."`,
		`"fqdn": "web.example.com."`, `"propagate_uplink_status": true`, `"data_plane_status": "ACTIVE"`,
		`"revision_number": 4`, `"extra_dhcp_opts": null`,
	} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %s:\n%s", want, buf.String())
		}
	}
}

func TestParseBindingProfile(t *testing.T) {
	got, err := parseBindingProfile([]string{"a=1", `{"b":2,"a":"x"}`, "c=d=e"})
	if err != nil {
		t.Fatalf("parseBindingProfile: %v", err)
	}
	// A JSON number stays a json.Number, so neutron gets 2, not "2" or 2.0.
	if got["a"] != "x" || got["c"] != "d=e" || got["b"] != json.Number("2") {
		t.Errorf("parseBindingProfile = %#v", got)
	}
	if _, err := parseBindingProfile([]string{"novalue"}); err == nil {
		t.Error("parseBindingProfile accepted a value that is neither key=value nor JSON")
	}
}

func TestParseExtraDHCPOptions_Errors(t *testing.T) {
	for _, spec := range []string{"value=x", "name=a,ip-version=5", "name=a,colour=red", "leading", "name=a,=b"} {
		if _, err := parseExtraDHCPOptions([]string{spec}); err == nil {
			t.Errorf("parseExtraDHCPOptions(%q) accepted a bad spec", spec)
		}
	}
}

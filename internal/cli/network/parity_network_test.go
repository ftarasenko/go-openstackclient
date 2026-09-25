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

// --- network list ----------------------------------------------------------

func TestRunNetworkList_SendsNewFilters(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/networks", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		q := r.URL.Query()
		want := map[string][]string{
			"router:external": {"false"},
			"admin_state_up":  {"false"},
			"project_id":      {"p1"},
			"tags":            {"a,b"},
			"tags-any":        {"c"},
			"not-tags":        {"d"},
			"not-tags-any":    {"e,f"},
		}
		for k, v := range want {
			if !reflect.DeepEqual(q[k], v) {
				t.Errorf("query %s = %v, want %v", k, q[k], v)
			}
		}
		if len(q) != len(want) {
			t.Errorf("query has %d keys, want %d: %v", len(q), len(want), q)
		}
		writeJSON(t, w, http.StatusOK, `{"networks":[{"id":"net-1","name":"private","status":"DOWN",
		  "admin_state_up":false,"subnets":["sub-1"],"router:external":false,
		  "availability_zones":["nova"],"tags":["a","b"]}]}`)
	})

	f := &networkListFlags{
		internal:   true,
		adminState: boolPtr(false),
		long:       true,
		tagFilterFlags: tagFilterFlags{
			tags: []string{"a", "b"}, anyTags: []string{"c"},
			notTags: []string{"d"}, notAnyTags: []string{"e", "f"},
		},
	}
	o := &output.Options{Format: output.FormatCSV}
	var buf bytes.Buffer
	if err := runNetworkList(context.Background(), networkClient(fakeServer), o, f, "p1", &buf); err != nil {
		t.Fatalf("runNetworkList: %v", err)
	}
	lines := strings.SplitN(buf.String(), "\n", 2)
	wantHeader := "ID,Name,Status,Project,State,Shared,Subnets,Network Type,Router Type,Availability Zones,Tags"
	if lines[0] != wantHeader {
		t.Errorf("header = %s\nwant     %s", lines[0], wantHeader)
	}
	for _, want := range []string{"DOWN", "Internal", "nova"} {
		if !strings.Contains(lines[1], want) {
			t.Errorf("row missing %q:\n%s", want, lines[1])
		}
	}
}

// --agent lists what a DHCP agent hosts and, as upstream, ignores the other
// filters and --long.
func TestRunNetworkList_AgentListsDHCPNetworks(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/networks", func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("--agent must not query /networks (got %s %s)", r.Method, r.URL)
	})
	fakeServer.Mux.HandleFunc("/agents/agent-1/dhcp-networks", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		writeJSON(t, w, http.StatusOK, `{"networks":[{"id":"net-1","name":"private","subnets":["sub-1"]}]}`)
	})

	f := &networkListFlags{agent: "agent-1", long: true, name: "ignored"}
	o := &output.Options{Format: output.FormatCSV}
	var buf bytes.Buffer
	if err := runNetworkList(context.Background(), networkClient(fakeServer), o, f, "", &buf); err != nil {
		t.Fatalf("runNetworkList --agent: %v", err)
	}
	if got := strings.TrimSpace(buf.String()); got != "ID,Name,Subnets\nnet-1,private,sub-1" {
		t.Errorf("output = %q", got)
	}
}

// --- network create --------------------------------------------------------

func TestRunNetworkCreate_SendsExtensionAttributesThenTags(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	emptyLookup(t, fakeServer, "/qos/policies", "policies")
	fakeServer.Mux.HandleFunc("/networks", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		th.TestJSONRequest(t, r, `{"network":{
		  "name":"n1","description":"d","admin_state_up":true,"shared":true,
		  "project_id":"p1","availability_zone_hints":["az1","az2"],
		  "router:external":true,"is_default":true,"port_security_enabled":true,
		  "vlan_transparent":true,"qos_policy_id":"qos-1","dns_domain":"example.com.",
		  "provider:network_type":"vlan","provider:physical_network":"physnet1",
		  "provider:segmentation_id":"100","mtu":1400,"custom":7}}`)
		writeJSON(t, w, http.StatusCreated, `{"network":{"id":"net-1","name":"n1",
		  "qos_policy_id":"qos-1","dns_domain":"example.com.","is_default":true,
		  "port_security_enabled":true,"vlan_transparent":true,"tags":[]}}`)
	})
	fakeServer.Mux.HandleFunc("/networks/net-1/tags", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"tags":["blue","red"]}`)
		writeJSON(t, w, http.StatusOK, `{"tags":["blue","red"]}`)
	})

	f := &networkCreateFlags{
		description: "d", share: true, projectID: "p1", azHints: []string{"az1", "az2"},
		external: true, defaultNet: true, enablePortSecurity: true, transparentVLAN: true,
		qosPolicy: "qos-1", dnsDomain: "example.com.",
		providerType: "vlan", providerPhysNet: "physnet1", providerSegment: "100", mtu: 1400,
		extraProperty: []string{"type=int,name=custom,value=7"},
		tagWriteFlags: tagWriteFlags{tags: []string{"red", "blue"}},
	}
	o := &output.Options{Format: output.FormatJSON}
	var buf bytes.Buffer
	if err := runNetworkCreate(context.Background(), networkClient(fakeServer), o, "n1", f, &buf); err != nil {
		t.Fatalf("runNetworkCreate: %v", err)
	}
	for _, want := range []string{`"qos_policy_id": "qos-1"`, `"dns_domain": "example.com."`,
		`"is_default": true`, `"port_security_enabled": true`, `"is_vlan_transparent": true`, `"blue"`} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %s:\n%s", want, buf.String())
		}
	}
}

// The "off" half of every pair is sent explicitly as false, as upstream does;
// no tag flag means no tag request.
func TestRunNetworkCreate_NegativePairsSendFalse(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/networks", func(w http.ResponseWriter, r *http.Request) {
		th.TestJSONRequest(t, r, `{"network":{
		  "name":"n2","admin_state_up":false,"shared":false,"router:external":false,
		  "is_default":false,"port_security_enabled":false,"vlan_transparent":false}}`)
		writeJSON(t, w, http.StatusCreated, `{"network":{"id":"net-2","name":"n2"}}`)
	})
	fakeServer.Mux.HandleFunc("/networks/net-2/tags", func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected tag request %s %s", r.Method, r.URL)
	})

	f := &networkCreateFlags{
		disable: true, noShare: true, internal: true, noDefault: true,
		disablePortSecurity: true, noTransparentVLAN: true,
	}
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	if err := runNetworkCreate(context.Background(), networkClient(fakeServer), o, "n2", f, &buf); err != nil {
		t.Fatalf("runNetworkCreate: %v", err)
	}
}

func TestRunNetworkCreate_ProviderSegmentNeedsType(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/networks", func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s %s", r.Method, r.URL)
	})
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	err := runNetworkCreate(context.Background(), networkClient(fakeServer), o, "n3",
		&networkCreateFlags{providerSegment: "100"}, &buf)
	if err == nil || !strings.Contains(err.Error(), "--provider-network-type") {
		t.Fatalf("err = %v, want a --provider-network-type requirement", err)
	}
}

// --- network set -----------------------------------------------------------

func TestRunNetworkSet_SendsNewAttributes(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	emptyLookup(t, fakeServer, "/networks", "networks")
	emptyLookup(t, fakeServer, "/qos/policies", "policies")
	fakeServer.Mux.HandleFunc("/networks/net-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"network":{
		  "description":"d","shared":false,"router:external":true,"is_default":true,
		  "port_security_enabled":false,"qos_policy_id":"qos-1","dns_domain":"",
		  "provider:network_type":"vlan","provider:physical_network":"physnet1",
		  "provider:segmentation_id":"200","custom":"x"}}`)
		writeJSON(t, w, http.StatusOK, `{"network":{"id":"net-1","tags":["a"]}}`)
	})
	fakeServer.Mux.HandleFunc("/networks/net-1/tags", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"tags":["a","b"]}`)
		writeJSON(t, w, http.StatusOK, `{"tags":["a","b"]}`)
	})

	f := &networkSetFlags{
		description: "d", noShare: true, external: true, defaultNet: true,
		disablePortSecurity: true, qosPolicy: "qos-1", dnsDomain: "",
		providerType: "vlan", providerPhysNet: "physnet1", providerSegment: "200",
		extraProperty: []string{"name=custom,value=x"},
		tagWriteFlags: tagWriteFlags{tags: []string{"b"}},
	}
	flags := fakeFlags{
		flagDescription: true, flagNoShare: true, netFlagExternal: true, flagDefault: true,
		flagDisablePortSecurity: true, flagDNSDomain: true,
	}
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	if err := runNetworkSet(context.Background(), networkClient(fakeServer), o, "net-1", f, flags, &buf); err != nil {
		t.Fatalf("runNetworkSet: %v", err)
	}
}

func TestRunNetworkSet_NoQoSPolicyAndInternal(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	emptyLookup(t, fakeServer, "/networks", "networks")
	fakeServer.Mux.HandleFunc("/networks/net-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestJSONRequest(t, r, `{"network":{"qos_policy_id":null,"router:external":false,
		  "is_default":false,"port_security_enabled":true}}`)
		writeJSON(t, w, http.StatusOK, `{"network":{"id":"net-1"}}`)
	})
	f := &networkSetFlags{noQoSPolicy: true, internal: true, noDefault: true, enablePortSecurity: true}
	flags := fakeFlags{flagNoQoSPolicy: true, netFlagInternal: true, flagNoDefault: true, flagEnablePortSecurity: true}
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	if err := runNetworkSet(context.Background(), networkClient(fakeServer), o, "net-1", f, flags, &buf); err != nil {
		t.Fatalf("runNetworkSet: %v", err)
	}
}

// Upstream skips the network PUT when only tags change; so must koc.
func TestRunNetworkSet_TagsOnlySkipsTheNetworkPut(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	emptyLookup(t, fakeServer, "/networks", "networks")
	fakeServer.Mux.HandleFunc("/networks/net-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		writeJSON(t, w, http.StatusOK, `{"network":{"id":"net-1","tags":["old"]}}`)
	})
	fakeServer.Mux.HandleFunc("/networks/net-1/tags", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"tags":["new"]}`)
		writeJSON(t, w, http.StatusOK, `{"tags":["new"]}`)
	})
	f := &networkSetFlags{tagWriteFlags: tagWriteFlags{noTag: true, tags: []string{"new"}}}
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	if err := runNetworkSet(context.Background(), networkClient(fakeServer), o, "net-1", f, fakeFlags{}, &buf); err != nil {
		t.Fatalf("runNetworkSet: %v", err)
	}
	if !strings.Contains(buf.String(), "new") {
		t.Errorf("output missing the new tag:\n%s", buf.String())
	}
}

// --- network unset ---------------------------------------------------------

func TestRunNetworkUnset_ExtraPropertyShareAndTags(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	emptyLookup(t, fakeServer, "/networks", "networks")
	fakeServer.Mux.HandleFunc("/networks/net-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"network":{"shared":false,"dns_domain":null}}`)
		writeJSON(t, w, http.StatusOK, `{"network":{"id":"net-1","tags":["a","b"]}}`)
	})
	fakeServer.Mux.HandleFunc("/networks/net-1/tags", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"tags":["b"]}`)
		writeJSON(t, w, http.StatusOK, `{"tags":["b"]}`)
	})
	f := &networkUnsetFlags{
		share: true, extraProperty: []string{"name=dns_domain"},
		tagWriteFlags: tagWriteFlags{tags: []string{"a"}},
	}
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	if err := runNetworkUnset(context.Background(), networkClient(fakeServer), o, "net-1", f, &buf); err != nil {
		t.Fatalf("runNetworkUnset: %v", err)
	}
}

func TestRunNetworkUnset_AllTagOnlySkipsTheNetworkPut(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	emptyLookup(t, fakeServer, "/networks", "networks")
	fakeServer.Mux.HandleFunc("/networks/net-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		writeJSON(t, w, http.StatusOK, `{"network":{"id":"net-1","tags":["a","b"]}}`)
	})
	fakeServer.Mux.HandleFunc("/networks/net-1/tags", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"tags":[]}`)
		writeJSON(t, w, http.StatusOK, `{"tags":[]}`)
	})
	f := &networkUnsetFlags{tagWriteFlags: tagWriteFlags{allTag: true}}
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	if err := runNetworkUnset(context.Background(), networkClient(fakeServer), o, "net-1", f, &buf); err != nil {
		t.Fatalf("runNetworkUnset: %v", err)
	}
}

// --- network show ----------------------------------------------------------

func TestRunNetworkShow_RendersExtensionAttributes(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	emptyLookup(t, fakeServer, "/networks", "networks")
	fakeServer.Mux.HandleFunc("/networks/net-1", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, `{"network":{"id":"net-1","qos_policy_id":"qos-1",
		  "dns_domain":"example.com.","is_default":false,"port_security_enabled":true,
		  "provider:network_type":"vlan","provider:segmentation_id":42,
		  "availability_zones":["nova"],"ipv4_address_scope":"as-4"}}`)
	})
	o := &output.Options{Format: output.FormatJSON}
	var buf bytes.Buffer
	if err := runNetworkShow(context.Background(), networkClient(fakeServer), o, "net-1", &buf); err != nil {
		t.Fatalf("runNetworkShow: %v", err)
	}
	for _, want := range []string{`"qos_policy_id": "qos-1"`, `"dns_domain": "example.com."`,
		`"is_default": false`, `"port_security_enabled": true`, `"provider:segmentation_id": "42"`,
		`"ipv4_address_scope": "as-4"`, `"nova"`} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %s:\n%s", want, buf.String())
		}
	}
}

// --- cobra wiring ----------------------------------------------------------

// The pairs and repeatable flags are translated in RunE / by cobra, which the
// seam tests above cannot see.
func TestExec_NetworkCreate_FlagsReachTheBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/v2.0/networks", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		th.TestJSONRequest(t, r, `{"network":{"name":"n1","admin_state_up":true,"shared":false,
		  "router:external":false,"availability_zone_hints":["az1","az2"],
		  "project_id":"11111111-2222-3333-4444-555555555555"}}`)
		writeJSON(t, w, http.StatusCreated, `{"network":{"id":"net-1","name":"n1","tags":[]}}`)
	})
	var tagged bool
	fakeServer.Mux.HandleFunc("/v2.0/networks/net-1/tags", func(w http.ResponseWriter, r *http.Request) {
		tagged = true
		th.TestJSONRequest(t, r, `{"tags":["x"]}`)
		writeJSON(t, w, http.StatusOK, `{"tags":["x"]}`)
	})
	out, err := execNetwork(t, fakeServer, "network", "create", "n1", "--no-share", "--internal",
		"--availability-zone-hint", "az1", "--availability-zone-hint", "az2",
		"--project", "11111111-2222-3333-4444-555555555555", "--tag", "x")
	if err != nil {
		t.Fatalf("network create: %v (%s)", err, out)
	}
	if !tagged {
		t.Error("--tag did not reach the tag request")
	}
}

func TestExec_NetworkCreate_PairsAreExclusive(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	for _, pair := range [][]string{
		{"--external", "--internal"}, {"--share", "--no-share"}, {"--default", "--no-default"},
		{"--transparent-vlan", "--no-transparent-vlan"}, {"--tag", "x", "--no-tag"},
	} {
		argv := append([]string{"network", "create", "n1"}, pair...)
		if _, err := execNetwork(t, fakeServer, argv...); err == nil {
			t.Errorf("%v: expected a mutual-exclusion error", pair)
		}
	}
}

func TestExec_NetworkList_NewFlagsReachTheQuery(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/v2.0/networks", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("router:external") != "false" || q.Get("admin_state_up") != "true" || q.Get("not-tags") != "a,b" {
			t.Errorf("query = %v", q)
		}
		writeJSON(t, w, http.StatusOK, `{"networks":[]}`)
	})
	if out, err := execNetwork(t, fakeServer, "network", "list", "--internal", "--enable", "--not-tags", "a,b"); err != nil {
		t.Fatalf("network list: %v (%s)", err, out)
	}
	if _, err := execNetwork(t, fakeServer, "network", "list", "--external", "--internal"); err == nil {
		t.Error("--external with --internal: expected a mutual-exclusion error")
	}
}

func TestExec_NetworkSet_NoShareAndInternal(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/v2.0/networks", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, `{"networks":[]}`)
	})
	fakeServer.Mux.HandleFunc("/v2.0/networks/net-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"network":{"shared":false,"router:external":false,"dns_domain":"example.com."}}`)
		writeJSON(t, w, http.StatusOK, `{"network":{"id":"net-1"}}`)
	})
	if out, err := execNetwork(t, fakeServer, "network", "set", "net-1", "--no-share", "--internal",
		"--dns-domain", "example.com."); err != nil {
		t.Fatalf("network set: %v (%s)", err, out)
	}
}

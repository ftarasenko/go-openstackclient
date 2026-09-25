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

// Tests for the agent/trunk batch of the network parity pass: the agent
// scheduling verbs, agent list --network/--router/--long, agent set
// --description, service provider list, trunk unset, trunk set --subport,
// trunk create --project, the network subport list alias and the two accepted
// shapes of trunk subport add/remove.

// trunkBareBody is a trunk as add_subports/remove_subports return it: neutron
// answers those actions with the trunk unwrapped, no {"trunk": …} envelope.
const trunkBareBody = `{"id": "t1", "name": "trunk-a", "port_id": "parent-1", "status": "ACTIVE",
  "admin_state_up": true, "sub_ports": [{"port_id": "sub-1", "segmentation_type": "vlan", "segmentation_id": 101}]}`

const agentBody = `{"agent":{"id":"a1","agent_type":"DHCP agent","host":"net1","alive":true,"admin_state_up":true}}`

// agentGet registers GET /agents/a1 — every scheduling verb checks the agent
// exists first, as upstream's get_agent does.
func agentGet(t *testing.T, fakeServer th.FakeServer, prefix string) {
	t.Helper()
	fakeServer.Mux.HandleFunc(prefix+"/agents/a1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		writeJSON(t, w, http.StatusOK, agentBody)
	})
}

// --- agent list --------------------------------------------------------------

func TestRunAgentList_NetworkListsHostingDHCPAgents(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	echoLookup(t, fakeServer, "/networks", "networks")
	fakeServer.Mux.HandleFunc("/networks/n1/dhcp-agents", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		if len(r.URL.Query()) != 0 {
			t.Errorf("query = %v, want none: the hosting-agents collection takes no filter", r.URL.Query())
		}
		writeJSON(t, w, http.StatusOK, `{"agents":[{"id":"a1","agent_type":"DHCP agent","host":"net1",
		  "availability_zone":"nova","alive":true,"admin_state_up":false,"binary":"neutron-dhcp-agent"}]}`)
	})

	var buf bytes.Buffer
	o := &output.Options{Format: output.FormatCSV}
	// --agent-type/--host do not apply to the hosting-agents collection.
	f := &agentListFlags{network: "n1", agentType: "l3", host: "ignored"}
	if err := runAgentList(context.Background(), networkClient(fakeServer), o, f, &buf); err != nil {
		t.Fatalf("runAgentList: %v", err)
	}
	want := "ID,Agent Type,Host,Availability Zone,Alive,State,Binary\n" +
		"a1,DHCP agent,net1,nova,:-),DOWN,neutron-dhcp-agent\n"
	if buf.String() != want {
		t.Errorf("output =\n%s\nwant\n%s", buf.String(), want)
	}
}

func TestRunAgentList_RouterLongAddsHAState(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	echoLookup(t, fakeServer, "/routers", "routers")
	fakeServer.Mux.HandleFunc("/routers/r1/l3-agents", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		writeJSON(t, w, http.StatusOK, `{"agents":[{"id":"a2","agent_type":"L3 agent","host":"net2",
		  "alive":false,"admin_state_up":true,"binary":"neutron-l3-agent","ha_state":"active"}]}`)
	})

	o := &output.Options{Format: output.FormatCSV}
	var long bytes.Buffer
	if err := runAgentList(context.Background(), networkClient(fakeServer), o,
		&agentListFlags{router: "r1", long: true}, &long); err != nil {
		t.Fatalf("runAgentList --router --long: %v", err)
	}
	want := "ID,Agent Type,Host,Availability Zone,Alive,State,Binary,HA State\n" +
		"a2,L3 agent,net2,,XXX,UP,neutron-l3-agent,active\n"
	if long.String() != want {
		t.Errorf("output =\n%s\nwant\n%s", long.String(), want)
	}

	var short bytes.Buffer
	if err := runAgentList(context.Background(), networkClient(fakeServer), o,
		&agentListFlags{router: "r1"}, &short); err != nil {
		t.Fatalf("runAgentList --router: %v", err)
	}
	if strings.Contains(short.String(), "HA State") {
		t.Errorf("HA State shown without --long:\n%s", short.String())
	}
}

func TestExec_AgentList_NetworkAndRouterAreMutuallyExclusive(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	_, err := execNetwork(t, fakeServer, "network", "agent", "list", "--network", "n1", "--router", "r1")
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("err = %v, want a mutual-exclusion error", err)
	}
}

// --- agent set ---------------------------------------------------------------

func TestRunAgentSet_DescriptionOnly(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/agents/a1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"agent":{"description":""}}`)
		writeJSON(t, w, http.StatusOK, agentBody)
	})
	var buf bytes.Buffer
	o := &output.Options{Format: output.FormatValue}
	// An empty --description is a deliberate clear, as upstream's "is not None".
	if err := runAgentSet(context.Background(), networkClient(fakeServer), o, "a1",
		&agentSetFlags{}, fakeFlags{"description": true}, &buf); err != nil {
		t.Fatalf("runAgentSet: %v", err)
	}
}

// --- agent add/remove network ------------------------------------------------

func TestRunAgentAddNetwork_DHCP(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	agentGet(t, fakeServer, "")
	echoLookup(t, fakeServer, "/networks", "networks")
	fakeServer.Mux.HandleFunc("/agents/a1/dhcp-networks", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		th.TestJSONRequest(t, r, `{"network_id":"n1"}`)
		w.WriteHeader(http.StatusCreated)
	})
	var buf bytes.Buffer
	if err := runAgentAddNetwork(context.Background(), networkClient(fakeServer), "a1", "n1", true, &buf); err != nil {
		t.Fatalf("runAgentAddNetwork: %v", err)
	}
	if got := buf.String(); got != "Added network n1 to DHCP agent a1\n" {
		t.Errorf("output = %q", got)
	}
}

// Without --dhcp upstream looks the agent and network up and does nothing; koc
// mirrors it, so the only requests are the two lookups.
func TestRunAgentAddNetwork_WithoutDHCPOnlyLooksUp(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	agentGet(t, fakeServer, "")
	echoLookup(t, fakeServer, "/networks", "networks")
	fakeServer.Mux.HandleFunc("/agents/a1/dhcp-networks", func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected %s %s without --dhcp", r.Method, r.URL.Path)
	})
	var buf bytes.Buffer
	if err := runAgentAddNetwork(context.Background(), networkClient(fakeServer), "a1", "n1", false, &buf); err != nil {
		t.Fatalf("runAgentAddNetwork: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("output = %q, want none", buf.String())
	}
}

func TestRunAgentAddNetwork_MissingAgentFails(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	// No /agents/a1 handler: the mux 404s, and nothing else may be called.
	err := runAgentAddNetwork(context.Background(), networkClient(fakeServer), "a1", "n1", true, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "getting network agent a1") {
		t.Fatalf("err = %v, want the agent lookup to fail", err)
	}
}

func TestRunAgentRemoveNetwork_DHCP(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	agentGet(t, fakeServer, "")
	echoLookup(t, fakeServer, "/networks", "networks")
	fakeServer.Mux.HandleFunc("/agents/a1/dhcp-networks/n1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodDelete)
		w.WriteHeader(http.StatusNoContent)
	})
	var buf bytes.Buffer
	if err := runAgentRemoveNetwork(context.Background(), networkClient(fakeServer), "a1", "n1", true, &buf); err != nil {
		t.Fatalf("runAgentRemoveNetwork: %v", err)
	}
	if got := buf.String(); got != "Removed network n1 from DHCP agent a1\n" {
		t.Errorf("output = %q", got)
	}
}

// --- agent add/remove router, agent router set ------------------------------

func TestRunAgentAddRouter_Body(t *testing.T) {
	tests := []struct {
		name string
		f    agentAddRouterFlags
		want string
	}{
		{name: "no priority", f: agentAddRouterFlags{l3: true}, want: `{"router_id":"r1"}`},
		// 0 is a valid priority: given explicitly it is sent.
		{name: "priority zero", f: agentAddRouterFlags{l3: true, prioritySet: true},
			want: `{"router_id":"r1","ha_chassis_priority":0}`},
		{name: "priority", f: agentAddRouterFlags{l3: true, priority: 7, prioritySet: true},
			want: `{"router_id":"r1","ha_chassis_priority":7}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()
			agentGet(t, fakeServer, "")
			echoLookup(t, fakeServer, "/routers", "routers")
			called := false
			fakeServer.Mux.HandleFunc("/agents/a1/l3-routers", func(w http.ResponseWriter, r *http.Request) {
				called = true
				th.TestMethod(t, r, http.MethodPost)
				th.TestJSONRequest(t, r, tc.want)
				w.WriteHeader(http.StatusCreated)
			})
			var buf bytes.Buffer
			if err := runAgentAddRouter(context.Background(), networkClient(fakeServer), "a1", "r1", &tc.f, &buf); err != nil {
				t.Fatalf("runAgentAddRouter: %v", err)
			}
			if !called {
				t.Fatal("POST /agents/a1/l3-routers was not sent")
			}
			if got := buf.String(); got != "Added router r1 to L3 agent a1\n" {
				t.Errorf("output = %q", got)
			}
		})
	}
}

func TestExec_AgentAddRouter_PriorityReachesTheBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	agentGet(t, fakeServer, "/v2.0")
	echoLookup(t, fakeServer, "/v2.0/routers", "routers")
	fakeServer.Mux.HandleFunc("/v2.0/agents/a1/l3-routers", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		th.TestJSONRequest(t, r, `{"router_id":"r1","ha_chassis_priority":0}`)
		w.WriteHeader(http.StatusCreated)
	})
	out, err := execNetwork(t, fakeServer, "network", "agent", "add", "router", "--l3",
		"--ha-chassis-priority", "0", "a1", "r1")
	if err != nil {
		t.Fatalf("agent add router: %v (%s)", err, out)
	}
}

func TestRunAgentRemoveRouter_L3(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	agentGet(t, fakeServer, "")
	echoLookup(t, fakeServer, "/routers", "routers")
	fakeServer.Mux.HandleFunc("/agents/a1/l3-routers/r1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodDelete)
		w.WriteHeader(http.StatusNoContent)
	})
	var buf bytes.Buffer
	if err := runAgentRemoveRouter(context.Background(), networkClient(fakeServer), "a1", "r1", true, &buf); err != nil {
		t.Fatalf("runAgentRemoveRouter: %v", err)
	}
	if got := buf.String(); got != "Removed router r1 from L3 agent a1\n" {
		t.Errorf("output = %q", got)
	}
}

func TestRunAgentRouterSet_PutsPriority(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	agentGet(t, fakeServer, "")
	echoLookup(t, fakeServer, "/routers", "routers")
	fakeServer.Mux.HandleFunc("/agents/a1/l3-routers/r1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		// No envelope: openstacksdk's update_router_in_agent sends the bare key.
		th.TestJSONRequest(t, r, `{"ha_chassis_priority":5}`)
		writeJSON(t, w, http.StatusOK, `{}`)
	})
	var buf bytes.Buffer
	if err := runAgentRouterSet(context.Background(), networkClient(fakeServer), "a1", "r1", 5, &buf); err != nil {
		t.Fatalf("runAgentRouterSet: %v", err)
	}
	if got := buf.String(); got != "Set HA chassis priority 5 for router r1 on L3 agent a1\n" {
		t.Errorf("output = %q", got)
	}
}

func TestExec_AgentRouterSet_RequiresPriority(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	_, err := execNetwork(t, fakeServer, "network", "agent", "router", "set", "a1", "r1")
	if err == nil || !strings.Contains(err.Error(), "--ha-chassis-priority is required") {
		t.Fatalf("err = %v, want the required-flag error", err)
	}
}

// --- service provider list ---------------------------------------------------

func TestRunServiceProviderList(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/service-providers", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		writeJSON(t, w, http.StatusOK, `{"service_providers":[
		  {"service_type":"L3_ROUTER_NAT","name":"ovn","default":true},
		  {"service_type":"QOS","name":"qos","default":false}]}`)
	})
	var buf bytes.Buffer
	o := &output.Options{Format: output.FormatCSV}
	if err := runServiceProviderList(context.Background(), networkClient(fakeServer), o, &buf); err != nil {
		t.Fatalf("runServiceProviderList: %v", err)
	}
	want := "Service Type,Name,Default\n" +
		"L3_ROUTER_NAT,ovn,true\n" +
		"QOS,qos,false\n"
	if buf.String() != want {
		t.Errorf("output =\n%s\nwant\n%s", buf.String(), want)
	}
}

func TestExec_ServiceProviderList_Wired(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/v2.0/service-providers", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, `{"service_providers":[{"service_type":"QOS","name":"qos","default":true}]}`)
	})
	out, err := execNetwork(t, fakeServer, "network", "service", "provider", "list")
	if err != nil || !strings.Contains(out, "QOS") {
		t.Fatalf("service provider list: err=%v out=%s", err, out)
	}
}

// --- trunk create / set / list ------------------------------------------------

func TestRunTrunkCreate_ProjectAndPortOnlySubport(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	echoLookup(t, fakeServer, "/ports", "ports")
	fakeServer.Mux.HandleFunc("/trunks", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		th.TestJSONRequest(t, r, `{"trunk":{
		  "name":"tr","port_id":"parent-1","project_id":"p1",
		  "sub_ports":[{"port_id":"sub-1"},{"port_id":"sub-2","segmentation_type":"inherit"}]}}`)
		writeJSON(t, w, http.StatusCreated, trunkShowBody)
	})
	f := &trunkCreateFlags{
		parentPort: "parent-1",
		subports:   []string{"port=sub-1", "port=sub-2,segmentation-type=inherit"},
		projectID:  "p1",
	}
	var buf bytes.Buffer
	if err := runTrunkCreate(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatValue},
		"tr", f, &buf); err != nil {
		t.Fatalf("runTrunkCreate: %v", err)
	}
}

// A subport-only set must not PUT the trunk: upstream's SDK skips a commit with
// nothing dirty and only calls add_subports.
func TestRunTrunkSet_SubportOnlySkipsTrunkPut(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	echoLookup(t, fakeServer, "/trunks", "trunks")
	echoLookup(t, fakeServer, "/ports", "ports")
	fakeServer.Mux.HandleFunc("/trunks/t1", func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected %s /trunks/t1 for a subport-only set", r.Method)
	})
	fakeServer.Mux.HandleFunc("/trunks/t1/add_subports", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"sub_ports":[{"port_id":"sub-1","segmentation_type":"vlan","segmentation_id":101}]}`)
		writeJSON(t, w, http.StatusOK, trunkBareBody)
	})
	f := &trunkSetFlags{subports: []string{"port=sub-1,segmentation-type=vlan,segmentation-id=101"}}
	var buf bytes.Buffer
	if err := runTrunkSet(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatValue},
		"t1", f, &buf); err != nil {
		t.Fatalf("runTrunkSet: %v", err)
	}
	if !strings.Contains(buf.String(), "sub-1:vlan:101") {
		t.Errorf("output missing the trunk returned by add_subports:\n%s", buf.String())
	}
}

func TestRunTrunkSet_AttributesThenSubports(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	echoLookup(t, fakeServer, "/trunks", "trunks")
	echoLookup(t, fakeServer, "/ports", "ports")
	var order []string
	fakeServer.Mux.HandleFunc("/trunks/t1", func(w http.ResponseWriter, r *http.Request) {
		order = append(order, "put")
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"trunk":{"name":"renamed"}}`)
		writeJSON(t, w, http.StatusOK, trunkShowBody)
	})
	fakeServer.Mux.HandleFunc("/trunks/t1/add_subports", func(w http.ResponseWriter, r *http.Request) {
		order = append(order, "add")
		th.TestJSONRequest(t, r, `{"sub_ports":[{"port_id":"sub-2"}]}`)
		writeJSON(t, w, http.StatusOK, trunkBareBody)
	})
	f := &trunkSetFlags{name: "renamed", nameSet: true, subports: []string{"port=sub-2"}}
	if err := runTrunkSet(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatValue},
		"t1", f, &bytes.Buffer{}); err != nil {
		t.Fatalf("runTrunkSet: %v", err)
	}
	if strings.Join(order, ",") != "put,add" {
		t.Errorf("request order = %v, want [put add]", order)
	}
}

func TestExec_TrunkSet_SubportFlagReachesTheSeam(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	echoLookup(t, fakeServer, "/v2.0/trunks", "trunks")
	echoLookup(t, fakeServer, "/v2.0/ports", "ports")
	fakeServer.Mux.HandleFunc("/v2.0/trunks/t1/add_subports", func(w http.ResponseWriter, r *http.Request) {
		th.TestJSONRequest(t, r, `{"sub_ports":[{"port_id":"sub-1","segmentation_type":"vlan","segmentation_id":9}]}`)
		writeJSON(t, w, http.StatusOK, trunkBareBody)
	})
	if out, err := execNetwork(t, fakeServer, "network", "trunk", "set", "t1",
		"--subport", "port=sub-1,segmentation-type=vlan,segmentation-id=9"); err != nil {
		t.Fatalf("trunk set --subport: %v (%s)", err, out)
	}
}

func TestRunTrunkList_LongMatchesUpstreamColumns(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/trunks", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, `{"trunks":[{"id":"t1","name":"tr","port_id":"p1","status":"ACTIVE",
		  "admin_state_up":false,"project_id":"prj","sub_ports":[]}]}`)
	})
	var buf bytes.Buffer
	o := &output.Options{Format: output.FormatCSV}
	if err := runTrunkList(context.Background(), networkClient(fakeServer), o, &trunkListFlags{long: true}, "", &buf); err != nil {
		t.Fatalf("runTrunkList: %v", err)
	}
	header := strings.SplitN(buf.String(), "\n", 2)[0]
	wantHeader := "ID,Name,Parent Port,Description,Status,State,Created At,Updated At,Sub Ports,Project"
	if header != wantHeader {
		t.Errorf("header = %s\nwant     %s", header, wantHeader)
	}
	if !strings.Contains(buf.String(), "ACTIVE,DOWN") {
		t.Errorf("State column should render admin_state_up as UP/DOWN:\n%s", buf.String())
	}
}

// --- trunk unset / subport list alias ----------------------------------------

func TestExec_TrunkUnset_RemovesEachSubport(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	echoLookup(t, fakeServer, "/v2.0/trunks", "trunks")
	echoLookup(t, fakeServer, "/v2.0/ports", "ports")
	fakeServer.Mux.HandleFunc("/v2.0/trunks/t1/remove_subports", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"sub_ports":[{"port_id":"sub-1"},{"port_id":"sub-2"}]}`)
		writeJSON(t, w, http.StatusOK, trunkBareBody)
	})
	if out, err := execNetwork(t, fakeServer, "network", "trunk", "unset", "t1",
		"--subport", "sub-1", "--subport", "sub-2"); err != nil {
		t.Fatalf("trunk unset: %v (%s)", err, out)
	}
	if _, err := execNetwork(t, fakeServer, "network", "trunk", "unset", "t1"); err == nil ||
		!strings.Contains(err.Error(), "--subport is required") {
		t.Fatalf("trunk unset without --subport: err = %v", err)
	}
}

func TestExec_SubportList_AliasOfTrunkSubportList(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	echoLookup(t, fakeServer, "/v2.0/trunks", "trunks")
	fakeServer.Mux.HandleFunc("/v2.0/trunks/t1/get_subports", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		writeJSON(t, w, http.StatusOK, `{"sub_ports":[{"port_id":"sub-1","segmentation_type":"vlan","segmentation_id":101}]}`)
	})
	alias, err := execNetwork(t, fakeServer, "network", "subport", "list", "--trunk", "t1")
	if err != nil {
		t.Fatalf("network subport list: %v (%s)", err, alias)
	}
	canonical, err := execNetwork(t, fakeServer, "network", "trunk", "subport", "list", "t1")
	if err != nil {
		t.Fatalf("network trunk subport list: %v", err)
	}
	if alias != canonical || !strings.Contains(alias, "sub-1") {
		t.Errorf("alias output differs from the canonical one:\n%s\n---\n%s", alias, canonical)
	}
	if _, err := execNetwork(t, fakeServer, "network", "subport", "list"); err == nil ||
		!strings.Contains(err.Error(), "--trunk is required") {
		t.Fatalf("subport list without --trunk: err = %v", err)
	}
}

// --- trunk subport add/remove: both shapes -----------------------------------

func TestExec_TrunkSubportAdd_BothShapes(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		want string
	}{
		{
			name: "upstream positional with segmentation flags",
			argv: []string{"t1", "sub-1", "--segmentation-type", "vlan", "--segmentation-id", "5"},
			want: `{"sub_ports":[{"port_id":"sub-1","segmentation_type":"vlan","segmentation_id":5}]}`,
		},
		{
			name: "upstream positional, port only",
			argv: []string{"t1", "sub-1"},
			want: `{"sub_ports":[{"port_id":"sub-1"}]}`,
		},
		{
			name: "koc --subport, repeated",
			argv: []string{"t1", "--subport", "port=sub-1,segmentation-type=vlan,segmentation-id=5", "--subport", "port=sub-2"},
			want: `{"sub_ports":[{"port_id":"sub-1","segmentation_type":"vlan","segmentation_id":5},{"port_id":"sub-2"}]}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()
			echoLookup(t, fakeServer, "/v2.0/trunks", "trunks")
			echoLookup(t, fakeServer, "/v2.0/ports", "ports")
			called := false
			fakeServer.Mux.HandleFunc("/v2.0/trunks/t1/add_subports", func(w http.ResponseWriter, r *http.Request) {
				called = true
				th.TestMethod(t, r, http.MethodPut)
				th.TestJSONRequest(t, r, tc.want)
				writeJSON(t, w, http.StatusOK, trunkBareBody)
			})
			argv := append([]string{"network", "trunk", "subport", "add"}, tc.argv...)
			if out, err := execNetwork(t, fakeServer, argv...); err != nil {
				t.Fatalf("%v: %v (%s)", argv, err, out)
			}
			if !called {
				t.Fatal("add_subports was not called")
			}
		})
	}
}

func TestExec_TrunkSubportAdd_RejectsMixedShapes(t *testing.T) {
	tests := []struct {
		name    string
		argv    []string
		wantErr string
	}{
		{name: "port and --subport", argv: []string{"t1", "sub-1", "--subport", "port=sub-2"},
			wantErr: "mutually exclusive"},
		{name: "neither", argv: []string{"t1"}, wantErr: "a sub-port is required"},
		{name: "segmentation flag with --subport", argv: []string{"t1", "--subport", "port=sub-2", "--segmentation-id", "4"},
			wantErr: "apply to <port>"},
		{name: "too many positionals", argv: []string{"t1", "sub-1", "sub-2"}, wantErr: "accepts between 1 and 2 arg(s)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()
			// No handlers: the shape check must fail before any request.
			argv := append([]string{"network", "trunk", "subport", "add"}, tc.argv...)
			_, err := execNetwork(t, fakeServer, argv...)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want one containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestExec_TrunkSubportRemove_BothShapes(t *testing.T) {
	for _, argv := range [][]string{
		{"t1", "sub-1", "sub-2"},
		{"t1", "--subport", "sub-1", "--subport", "sub-2"},
	} {
		t.Run(strings.Join(argv, " "), func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()
			echoLookup(t, fakeServer, "/v2.0/trunks", "trunks")
			echoLookup(t, fakeServer, "/v2.0/ports", "ports")
			fakeServer.Mux.HandleFunc("/v2.0/trunks/t1/remove_subports", func(w http.ResponseWriter, r *http.Request) {
				th.TestMethod(t, r, http.MethodPut)
				th.TestJSONRequest(t, r, `{"sub_ports":[{"port_id":"sub-1"},{"port_id":"sub-2"}]}`)
				writeJSON(t, w, http.StatusOK, trunkBareBody)
			})
			full := append([]string{"network", "trunk", "subport", "remove"}, argv...)
			if out, err := execNetwork(t, fakeServer, full...); err != nil {
				t.Fatalf("%v: %v (%s)", full, err, out)
			}
		})
	}
}

func TestExec_TrunkSubportRemove_RejectsMixedShapes(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	_, err := execNetwork(t, fakeServer, "network", "trunk", "subport", "remove", "t1", "sub-1", "--subport", "sub-2")
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("err = %v, want a mutual-exclusion error", err)
	}
	_, err = execNetwork(t, fakeServer, "network", "trunk", "subport", "remove", "t1")
	if err == nil || !strings.Contains(err.Error(), "a sub-port is required") {
		t.Fatalf("err = %v, want the missing-port error", err)
	}
}

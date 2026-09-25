package network

import (
	"bytes"
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	th "github.com/gophercloud/gophercloud/v2/testhelper"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Parity for the remaining network nouns: QoS policy/rule, RBAC, segments,
// floating-IP port forwarding, address scope/group and IP availability.

// execNetworkWithIdentity is execNetwork with keystone reachable too: the
// identity client is served from <mock>/v3/, so a --project name resolves
// against the same fake server.
func execNetworkWithIdentity(t *testing.T, fakeServer th.FakeServer, argv ...string) (string, error) {
	t.Helper()
	a := &auth.Options{}
	provider := &gophercloud.ProviderClient{
		TokenID:      "fake-token",
		IdentityBase: fakeServer.Server.URL + "/",
		EndpointLocator: func(gophercloud.EndpointOpts) (string, error) {
			return fakeServer.Server.URL + "/", nil
		},
	}
	a.SetAuthenticatorForTest(func(context.Context) (*auth.Client, error) {
		return a.NewClientForTest(provider, gophercloud.EndpointOpts{}), nil
	})
	root := &cobra.Command{Use: "koc"}
	root.AddCommand(NewCommand(a, &output.Options{Format: "table"})...)
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs(argv)
	root.SilenceUsage = true
	root.SilenceErrors = true
	err := root.ExecuteContext(context.Background())
	return buf.String(), err
}

const (
	miscDomainID  = "d0d0d0d0-0000-0000-0000-d0d0d0d0d0d0"
	miscProjectID = "a1a1a1a1-1111-1111-1111-a1a1a1a1a1a1"
	miscOwnerID   = "b2b2b2b2-2222-2222-2222-b2b2b2b2b2b2"
)

// handleProjectLookup serves keystone's domain and project lookups: every
// project name in names maps to its ID, and a domain filter must be the one
// --project-domain named.
func handleProjectLookup(t *testing.T, fakeServer th.FakeServer, names map[string]string) {
	t.Helper()
	fakeServer.Mux.HandleFunc("/v3/domains", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		th.AssertEquals(t, "dom", r.URL.Query().Get("name"))
		writeJSON(t, w, http.StatusOK, `{"domains":[{"id":"`+miscDomainID+`","name":"dom"}]}`)
	})
	fakeServer.Mux.HandleFunc("/v3/projects", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		q := r.URL.Query()
		if d := q.Get("domain_id"); d != "" && d != miscDomainID {
			t.Errorf("project lookup domain_id = %q, want %q", d, miscDomainID)
		}
		id, ok := names[q.Get("name")]
		if !ok {
			writeJSON(t, w, http.StatusOK, `{"projects":[]}`)
			return
		}
		writeJSON(t, w, http.StatusOK, `{"projects":[{"id":"`+id+`","name":"`+q.Get("name")+`"}]}`)
	})
}

func csvHeader(s string) string { return strings.SplitN(s, "\n", 2)[0] }

// --- network qos policy ------------------------------------------------------

// Upstream sends --no-share / --no-default as an explicit false, and the
// extra property is merged last.
func TestRunQoSPolicyCreate_ExplicitFalsesProjectAndExtraProperty(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/qos/policies", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		th.TestJSONRequest(t, r, `{"policy":{"name":"gold","description":"d","project_id":"`+miscProjectID+`",
		  "shared":false,"is_default":false,"custom":3}}`)
		writeJSON(t, w, http.StatusCreated, `{"policy":{"id":"`+qosPolicyID+`","name":"gold"}}`)
	})
	f := &qosPolicyCreateFlags{description: "d", projectID: miscProjectID, noShare: true, noDefault: true,
		extraProperty: []string{"type=int,name=custom,value=3"}}
	var buf bytes.Buffer
	if err := runQoSPolicyCreate(context.Background(), networkClient(fakeServer), &output.Options{Format: "value"},
		"gold", f, &buf); err != nil {
		t.Fatalf("runQoSPolicyCreate: %v", err)
	}
	if !strings.Contains(buf.String(), qosPolicyID) {
		t.Errorf("output missing the policy ID:\n%s", buf.String())
	}
}

func TestRunQoSPolicySet_ExtraProperty(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	echoLookup(t, fakeServer, "/qos/policies", "policies")
	fakeServer.Mux.HandleFunc("/qos/policies/"+qosPolicyID, func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"policy":{"name":"silver","custom":true}}`)
		writeJSON(t, w, http.StatusOK, `{"policy":{"id":"`+qosPolicyID+`","name":"silver"}}`)
	})
	f := &qosPolicySetFlags{name: "silver", extraProperty: []string{"type=bool,name=custom,value=True"}}
	var buf bytes.Buffer
	if err := runQoSPolicySet(context.Background(), networkClient(fakeServer), &output.Options{Format: "value"},
		qosPolicyID, f, &buf); err != nil {
		t.Fatalf("runQoSPolicySet: %v", err)
	}
}

// --project on "qos policy list/create" is resolved through keystone, narrowed
// by --project-domain, and only the ID reaches neutron.
func TestExec_QoSPolicyListAndCreate_ResolveProjectInDomain(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	handleProjectLookup(t, fakeServer, map[string]string{"demo": miscProjectID})
	fakeServer.Mux.HandleFunc("/v2.0/qos/policies", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			th.AssertEquals(t, miscProjectID, r.URL.Query().Get("project_id"))
			writeJSON(t, w, http.StatusOK, `{"policies":[]}`)
		case http.MethodPost:
			th.TestJSONRequest(t, r, `{"policy":{"name":"gold","project_id":"`+miscProjectID+`"}}`)
			writeJSON(t, w, http.StatusCreated, `{"policy":{"id":"`+qosPolicyID+`","name":"gold"}}`)
		}
	})
	if out, err := execNetworkWithIdentity(t, fakeServer, "network", "qos", "policy", "list",
		"--project", "demo", "--project-domain", "dom"); err != nil {
		t.Fatalf("qos policy list: %v (%s)", err, out)
	}
	if out, err := execNetworkWithIdentity(t, fakeServer, "network", "qos", "policy", "create", "gold",
		"--project", "demo", "--project-domain", "dom"); err != nil {
		t.Fatalf("qos policy create: %v (%s)", err, out)
	}
}

// --- network qos rule --------------------------------------------------------

func TestQoSRuleFlagsBody_UpstreamDirectionSpellings(t *testing.T) {
	mbw, _ := qosRuleKindByCLIType("minimum-bandwidth")
	mpr, _ := qosRuleKindByCLIType("minimum-packet-rate")
	bwl, _ := qosRuleKindByCLIType("bandwidth-limit")

	cases := []struct {
		name string
		k    qosRuleKind
		f    qosRuleFlags
		want map[string]any
	}{
		{"ingress", mbw, qosRuleFlags{minKBps: 10, ingress: true,
			changed: changedFlags{"min-kbps": true, flagQoSIngress: true}},
			map[string]any{"min_kbps": 10, "direction": "ingress"}},
		{"egress", bwl, qosRuleFlags{maxKBps: 5, egress: true,
			changed: changedFlags{"max-kbps": true, flagQoSEgress: true}},
			map[string]any{"max_kbps": 5, "direction": "egress"}},
		{"any on minimum-packet-rate", mpr, qosRuleFlags{minKpps: 7, anyDirection: true,
			changed: changedFlags{"min-kpps": true, flagQoSAny: true}},
			map[string]any{"min_kpps": 7, "direction": "any"}},
		{"koc --direction still works", mbw, qosRuleFlags{direction: "egress",
			changed: changedFlags{flagQoSDirection: true}},
			map[string]any{"direction": "egress"}},
		{"extra property wins", bwl, qosRuleFlags{maxKBps: 5,
			extraProperty: []string{"type=int,name=max_kbps,value=9", "name=note,value=x"},
			changed:       changedFlags{"max-kbps": true}},
			map[string]any{"max_kbps": 9, "note": "x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.f.body(tc.k)
			if err != nil {
				t.Fatalf("body: %v", err)
			}
			th.AssertDeepEquals(t, tc.want, got)
		})
	}
}

// Upstream: 'Direction "any" can only be used with minimum-packet-rate rule type'.
func TestQoSRuleFlagsBody_AnyRejectedOutsideMinimumPacketRate(t *testing.T) {
	for _, typ := range []string{"bandwidth-limit", "minimum-bandwidth", "dscp-marking"} {
		k, _ := qosRuleKindByCLIType(typ)
		f := &qosRuleFlags{anyDirection: true, changed: changedFlags{flagQoSAny: true}}
		if _, err := f.body(k); err == nil {
			t.Errorf("%s: --any was accepted", typ)
		}
	}
}

func TestExec_QoSRuleCreate_AnyAndExtraPropertyReachTheBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/v2.0/qos/policies", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, `{"policies":[{"id":"`+qosPolicyID+`","name":"gold"}]}`)
	})
	fakeServer.Mux.HandleFunc("/v2.0/qos/policies/"+qosPolicyID+"/minimum_packet_rate_rules",
		func(w http.ResponseWriter, r *http.Request) {
			th.TestMethod(t, r, http.MethodPost)
			th.TestJSONRequest(t, r, `{"minimum_packet_rate_rule":{"min_kpps":1000,"direction":"any","x":"y"}}`)
			writeJSON(t, w, http.StatusCreated, `{"minimum_packet_rate_rule":{"id":"`+qosRuleID+`"}}`)
		})
	out, err := execNetwork(t, fakeServer, "network", "qos", "rule", "create", "gold",
		"--type", "minimum-packet-rate", "--min-kpps", "1000", "--any", "--extra-property", "name=x,value=y")
	if err != nil {
		t.Fatalf("qos rule create: %v (%s)", err, out)
	}
}

func TestExec_QoSRuleCreate_DirectionSpellingsAreExclusive(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	for _, pair := range [][]string{{"--direction", "egress", "--ingress"}, {"--ingress", "--egress"}, {"--egress", "--any"}} {
		argv := append([]string{"network", "qos", "rule", "create", "gold", "--type", "minimum-bandwidth"}, pair...)
		if _, err := execNetwork(t, fakeServer, argv...); err == nil {
			t.Errorf("%v was accepted", pair)
		}
	}
}

// --- network rbac ------------------------------------------------------------

func TestRunRBACCreate_OwnerProjectAndExtraProperty(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/rbac-policies", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		th.TestJSONRequest(t, r, `{"rbac_policy":{"action":"access_as_shared","object_type":"network",
		  "object_id":"`+extNetworkID+`","target_tenant":"`+miscProjectID+`","project_id":"`+miscOwnerID+`","k":"v"}}`)
		writeJSON(t, w, http.StatusCreated, `{"rbac_policy":{"id":"`+extRBACID+`","project_id":"`+miscOwnerID+`"}}`)
	})
	f := &rbacCreateFlags{action: "access_as_shared", objectType: "network", targetProject: miscProjectID,
		projectID: miscOwnerID, extraProperty: []string{"name=k,value=v"}}
	var buf bytes.Buffer
	if err := runRBACCreate(context.Background(), networkClient(fakeServer), &output.Options{Format: "value"},
		extNetworkID, f, &buf); err != nil {
		t.Fatalf("runRBACCreate: %v", err)
	}
	if !strings.Contains(buf.String(), miscOwnerID) {
		t.Errorf("output missing the owner project:\n%s", buf.String())
	}
}

// target_tenant is tagged required in UpdateOpts; an extra-property-only set
// must still build a body, without it.
func TestRunRBACSet_ExtraPropertyOnly(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/rbac-policies/"+extRBACID, func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"rbac_policy":{"k":"v"}}`)
		writeJSON(t, w, http.StatusOK, `{"rbac_policy":{"id":"`+extRBACID+`"}}`)
	})
	var buf bytes.Buffer
	if err := runRBACSet(context.Background(), networkClient(fakeServer), &output.Options{Format: "value"},
		extRBACID, &rbacSetFlags{extraProperty: []string{"name=k,value=v"}}, &buf); err != nil {
		t.Fatalf("runRBACSet: %v", err)
	}
}

func TestRunRBACSet_TargetAndExtraProperty(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/rbac-policies/"+extRBACID, func(w http.ResponseWriter, r *http.Request) {
		th.TestJSONRequest(t, r, `{"rbac_policy":{"target_tenant":"`+miscProjectID+`","k":"v"}}`)
		writeJSON(t, w, http.StatusOK, `{"rbac_policy":{"id":"`+extRBACID+`"}}`)
	})
	var buf bytes.Buffer
	if err := runRBACSet(context.Background(), networkClient(fakeServer), &output.Options{Format: "value"},
		extRBACID, &rbacSetFlags{targetProject: miscProjectID, extraProperty: []string{"name=k,value=v"}}, &buf); err != nil {
		t.Fatalf("runRBACSet: %v", err)
	}
}

func TestRunRBACList_ColumnsMatchUpstream(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/rbac-policies", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, `{"rbac_policies":[{"id":"`+extRBACID+`","object_type":"network",
		  "object_id":"`+extNetworkID+`","action":"access_as_shared","target_tenant":"*"}]}`)
	})
	for _, tc := range []struct {
		long bool
		want string
	}{
		{false, "ID,Object Type,Object ID"},
		{true, "ID,Object Type,Object ID,Action,Target Project"},
	} {
		var buf bytes.Buffer
		if err := runRBACList(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatCSV},
			"", "", "", tc.long, &buf); err != nil {
			t.Fatalf("runRBACList: %v", err)
		}
		if got := csvHeader(buf.String()); got != tc.want {
			t.Errorf("long=%v header = %s, want %s", tc.long, got, tc.want)
		}
	}
}

// Both project pairs on "rbac create" resolve through keystone: the target
// (--target-project-domain) and the owner (--project-domain).
func TestExec_RBACCreate_ResolvesTargetAndOwnerProjects(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	handleProjectLookup(t, fakeServer, map[string]string{"tgt": miscProjectID, "own": miscOwnerID})
	fakeServer.Mux.HandleFunc("/v2.0/rbac-policies", func(w http.ResponseWriter, r *http.Request) {
		th.TestJSONRequest(t, r, `{"rbac_policy":{"action":"access_as_shared","object_type":"network",
		  "object_id":"`+extNetworkID+`","target_tenant":"`+miscProjectID+`","project_id":"`+miscOwnerID+`"}}`)
		writeJSON(t, w, http.StatusCreated, `{"rbac_policy":{"id":"`+extRBACID+`"}}`)
	})
	out, err := execNetworkWithIdentity(t, fakeServer, "network", "rbac", "create", extNetworkID,
		"--type", "network", "--action", "access_as_shared",
		"--target-project", "tgt", "--target-project-domain", "dom",
		"--project", "own", "--project-domain", "dom")
	if err != nil {
		t.Fatalf("rbac create: %v (%s)", err, out)
	}
}

// "*" is neutron's wildcard, not a project name: it must not reach keystone.
func TestExec_RBACList_WildcardTargetSkipsKeystone(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/v3/projects", func(w http.ResponseWriter, _ *http.Request) {
		t.Error("keystone was asked to resolve the * wildcard")
		writeJSON(t, w, http.StatusOK, `{"projects":[]}`)
	})
	fakeServer.Mux.HandleFunc("/v2.0/rbac-policies", func(w http.ResponseWriter, r *http.Request) {
		th.AssertEquals(t, "*", r.URL.Query().Get("target_tenant"))
		writeJSON(t, w, http.StatusOK, `{"rbac_policies":[]}`)
	})
	if out, err := execNetworkWithIdentity(t, fakeServer, "network", "rbac", "list", "--target-project", "*", "--long"); err != nil {
		t.Fatalf("rbac list: %v (%s)", err, out)
	}
}

func TestExec_RBACSet_RequiresSomething(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	if _, err := execNetwork(t, fakeServer, "network", "rbac", "set", extRBACID); err == nil {
		t.Fatal("an empty rbac set was accepted")
	}
}

// --- network segment ---------------------------------------------------------

func TestRunSegmentCreateAndSet_ExtraProperty(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	echoLookup(t, fakeServer, "/networks", "networks")
	fakeServer.Mux.HandleFunc("/segments", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		th.TestJSONRequest(t, r, `{"segment":{"name":"s1","network_id":"`+extNetworkID+`",
		  "network_type":"vlan","segmentation_id":42,"k":"v"}}`)
		writeJSON(t, w, http.StatusCreated, `{"segment":{"id":"`+extSegmentID+`"}}`)
	})
	fakeServer.Mux.HandleFunc("/segments/"+extSegmentID, func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"segment":{"name":"s2","k":["a","b"]}}`)
		writeJSON(t, w, http.StatusOK, `{"segment":{"id":"`+extSegmentID+`"}}`)
	})
	o := &output.Options{Format: "value"}
	var buf bytes.Buffer
	if err := runSegmentCreate(context.Background(), networkClient(fakeServer), o, "s1",
		&segmentCreateFlags{network: extNetworkID, networkType: "vlan", segmentationID: 42,
			extraProperty: []string{"name=k,value=v"}}, &buf); err != nil {
		t.Fatalf("runSegmentCreate: %v", err)
	}
	if err := runSegmentSet(context.Background(), networkClient(fakeServer), o, extSegmentID,
		&segmentSetFlags{name: "s2", nameSet: true, extraProperty: []string{"type=list,name=k,value=a;b"}}, &buf); err != nil {
		t.Fatalf("runSegmentSet: %v", err)
	}
}

func TestRunSegmentList_ColumnsMatchUpstream(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/segments", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, `{"segments":[{"id":"`+extSegmentID+`","name":"s1","network_id":"`+extNetworkID+`",
		  "network_type":"vlan","segmentation_id":42,"physical_network":"physnet1"}]}`)
	})
	for _, tc := range []struct {
		long bool
		want string
	}{
		{false, "ID,Name,Network,Network Type,Segment"},
		{true, "ID,Name,Network,Network Type,Segment,Physical Network"},
	} {
		var buf bytes.Buffer
		if err := runSegmentList(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatCSV},
			"", "", "", tc.long, &buf); err != nil {
			t.Fatalf("runSegmentList: %v", err)
		}
		if got := csvHeader(buf.String()); got != tc.want {
			t.Errorf("long=%v header = %s, want %s", tc.long, got, tc.want)
		}
		if tc.long && !strings.Contains(buf.String(), "physnet1") {
			t.Errorf("--long output missing the physical network:\n%s", buf.String())
		}
	}
}

// --- floating ip port forwarding ---------------------------------------------

func TestRunPortForwardingList_SendsEveryFilter(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	echoLookup(t, fakeServer, "/ports", "ports")
	var queries []map[string][]string
	fakeServer.Mux.HandleFunc("/floatingips/"+extFIPID+"/port_forwardings", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		queries = append(queries, r.URL.Query())
		writeJSON(t, w, http.StatusOK, `{"port_forwardings":[{"id":"`+extPFID+`","protocol":"tcp",
		  "internal_port_id":"`+extPortID+`","internal_ip_address":"192.0.2.10","internal_port":22,
		  "external_port":2222,"description":"ssh"}]}`)
	})
	o := &output.Options{Format: output.FormatCSV}
	var buf bytes.Buffer
	if err := runPortForwardingList(context.Background(), networkClient(fakeServer), o, extFIPID,
		&portForwardingListFlags{port: extPortID, externalPort: "2222", protocol: "tcp"}, &buf); err != nil {
		t.Fatalf("runPortForwardingList: %v", err)
	}
	if err := runPortForwardingList(context.Background(), networkClient(fakeServer), o, extFIPID,
		&portForwardingListFlags{externalPort: "2000:2010"}, &bytes.Buffer{}); err != nil {
		t.Fatalf("runPortForwardingList (range): %v", err)
	}
	want := []map[string][]string{
		{"internal_port_id": {extPortID}, "external_port": {"2222"}, "protocol": {"tcp"}},
		{"external_port_range": {"2000:2010"}},
	}
	if !reflect.DeepEqual(queries, want) {
		t.Errorf("queries = %v\nwant      %v", queries, want)
	}
	wantHeader := "ID,Internal Port ID,Internal IP Address,Internal Port,Internal Port Range," +
		"External Port,External Port Range,Protocol,Description"
	if got := csvHeader(buf.String()); got != wantHeader {
		t.Errorf("header = %s\nwant     %s", got, wantHeader)
	}
	if !strings.Contains(buf.String(), "ssh") {
		t.Errorf("output missing the description:\n%s", buf.String())
	}
}

func TestRunPortForwardingList_RejectsABadExternalPort(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	err := runPortForwardingList(context.Background(), networkClient(fakeServer), &output.Options{Format: "value"},
		extFIPID, &portForwardingListFlags{externalPort: "ssh"}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("a non-numeric --external-protocol-port was accepted")
	}
}

func TestRunPortForwardingCreateAndSet_ExtraProperty(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	echoLookup(t, fakeServer, "/ports", "ports")
	fakeServer.Mux.HandleFunc("/floatingips/"+extFIPID+"/port_forwardings", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		th.TestJSONRequest(t, r, `{"port_forwarding":{"internal_port_id":"`+extPortID+`",
		  "internal_ip_address":"192.0.2.10","internal_port":22,"external_port":2222,"protocol":"tcp","k":"v"}}`)
		writeJSON(t, w, http.StatusCreated, `{"port_forwarding":{"id":"`+extPFID+`"}}`)
	})
	fakeServer.Mux.HandleFunc("/floatingips/"+extFIPID+"/port_forwardings/"+extPFID, func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"port_forwarding":{"k":1}}`)
		writeJSON(t, w, http.StatusOK, `{"port_forwarding":{"id":"`+extPFID+`"}}`)
	})
	o := &output.Options{Format: "value"}
	if err := runPortForwardingCreate(context.Background(), networkClient(fakeServer), o, extFIPID,
		&portForwardingFlags{port: extPortID, internalIP: "192.0.2.10", internalPort: 22, externalPort: 2222,
			protocol: "tcp", extraProperty: []string{"name=k,value=v"}}, &bytes.Buffer{}); err != nil {
		t.Fatalf("runPortForwardingCreate: %v", err)
	}
	if err := runPortForwardingSet(context.Background(), networkClient(fakeServer), o, extFIPID, extPFID,
		&portForwardingFlags{extraProperty: []string{"type=int,name=k,value=1"}}, &bytes.Buffer{}); err != nil {
		t.Fatalf("runPortForwardingSet: %v", err)
	}
}

func TestExec_PortForwardingList_FlagsReachTheQuery(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	echoLookup(t, fakeServer, "/v2.0/ports", "ports")
	fakeServer.Mux.HandleFunc("/v2.0/floatingips/"+extFIPID+"/port_forwardings", func(w http.ResponseWriter, r *http.Request) {
		want := map[string][]string{"internal_port_id": {extPortID}, "external_port_range": {"80:90"}, "protocol": {"udp"}}
		if got := map[string][]string(r.URL.Query()); !reflect.DeepEqual(got, want) {
			t.Errorf("query = %v, want %v", got, want)
		}
		writeJSON(t, w, http.StatusOK, `{"port_forwardings":[]}`)
	})
	out, err := execNetwork(t, fakeServer, "floating", "ip", "port", "forwarding", "list", extFIPID,
		"--port", extPortID, "--external-protocol-port", "80:90", "--protocol", "udp")
	if err != nil {
		t.Fatalf("port forwarding list: %v (%s)", err, out)
	}
}

// --- address scope -----------------------------------------------------------

func TestRunAddressScopeCreate_NoShareProjectAndExtraProperty(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/address-scopes", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		th.TestJSONRequest(t, r, `{"address_scope":{"name":"as1","ip_version":4,"shared":false,
		  "project_id":"`+miscProjectID+`","k":"v"}}`)
		writeJSON(t, w, http.StatusCreated, `{"address_scope":{"id":"as-1","name":"as1"}}`)
	})
	f := &addressScopeCreateFlags{ipVersion: 4, noShare: true, projectID: miscProjectID,
		extraProperty: []string{"name=k,value=v"}}
	if err := runAddressScopeCreate(context.Background(), networkClient(fakeServer), &output.Options{Format: "value"},
		"as1", f, &bytes.Buffer{}); err != nil {
		t.Fatalf("runAddressScopeCreate: %v", err)
	}
}

func TestRunAddressScopeSet_ExtraProperty(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	const scopeID = "a5a5a5a5-5555-5555-5555-a5a5a5a5a5a5"
	fakeServer.Mux.HandleFunc("/address-scopes/"+scopeID, func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"address_scope":{"shared":true,"k":"v"}}`)
		writeJSON(t, w, http.StatusOK, `{"address_scope":{"id":"`+scopeID+`"}}`)
	})
	if err := runAddressScopeSet(context.Background(), networkClient(fakeServer), &output.Options{Format: "value"},
		scopeID, &addressScopeSetFlags{share: true, extraProperty: []string{"name=k,value=v"}}, &bytes.Buffer{}); err != nil {
		t.Fatalf("runAddressScopeSet: %v", err)
	}
}

func TestRunAddressScopeList_ProjectFilterAndHeader(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/address-scopes", func(w http.ResponseWriter, r *http.Request) {
		th.AssertEquals(t, miscProjectID, r.URL.Query().Get("project_id"))
		writeJSON(t, w, http.StatusOK, `{"address_scopes":[{"id":"as-1","name":"as1","ip_version":4,"project_id":"`+miscProjectID+`"}]}`)
	})
	var buf bytes.Buffer
	if err := runAddressScopeList(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatCSV},
		&addressScopeListFlags{projectID: miscProjectID}, &buf); err != nil {
		t.Fatalf("runAddressScopeList: %v", err)
	}
	if got := csvHeader(buf.String()); got != "ID,Name,IP Version,Shared,Project" {
		t.Errorf("header = %s", got)
	}
}

func TestExec_AddressScopeList_ResolvesProjectInDomain(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	handleProjectLookup(t, fakeServer, map[string]string{"demo": miscProjectID})
	fakeServer.Mux.HandleFunc("/v2.0/address-scopes", func(w http.ResponseWriter, r *http.Request) {
		th.AssertEquals(t, miscProjectID, r.URL.Query().Get("project_id"))
		writeJSON(t, w, http.StatusOK, `{"address_scopes":[]}`)
	})
	if out, err := execNetworkWithIdentity(t, fakeServer, "address", "scope", "list",
		"--project", "demo", "--project-domain", "dom"); err != nil {
		t.Fatalf("address scope list: %v (%s)", err, out)
	}
}

func TestExec_AddressScopeCreate_ShareAndNoShareAreExclusive(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	if _, err := execNetwork(t, fakeServer, "address", "scope", "create", "as1", "--share", "--no-share"); err == nil {
		t.Fatal("--share with --no-share was accepted")
	}
}

// --- address group -----------------------------------------------------------

const miscGroupID = "a6a6a6a6-6666-6666-6666-a6a6a6a6a6a6"

func TestRunAddressGroupCreate_ProjectAndExtraProperty(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/address-groups", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		th.TestJSONRequest(t, r, `{"address_group":{"name":"ag1","project_id":"`+miscProjectID+`",
		  "addresses":["192.0.2.0/24"],"k":"v"}}`)
		writeJSON(t, w, http.StatusCreated, `{"address_group":{"id":"`+miscGroupID+`"}}`)
	})
	f := &addressGroupCreateFlags{projectID: miscProjectID, addresses: []string{"192.0.2.0/24"},
		extraProperty: []string{"name=k,value=v"}}
	if err := runAddressGroupCreate(context.Background(), networkClient(fakeServer), &output.Options{Format: "value"},
		"ag1", f, &bytes.Buffer{}); err != nil {
		t.Fatalf("runAddressGroupCreate: %v", err)
	}
}

// An extra property alone is an attribute change, so the group is PUT; an
// --address alone is not, so only add_addresses is called (upstream "if attrs").
func TestRunAddressGroupSet_ExtraPropertyTriggersTheUpdate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		f       addressGroupSetFlags
		wantPut bool
	}{
		{"extra property", addressGroupSetFlags{extraProperty: []string{"name=k,value=v"}}, true},
		{"address only", addressGroupSetFlags{addresses: []string{"198.51.100.1/32"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()
			var put bool
			fakeServer.Mux.HandleFunc("/address-groups/"+miscGroupID, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPut {
					put = true
					th.TestJSONRequest(t, r, `{"address_group":{"k":"v"}}`)
				}
				writeJSON(t, w, http.StatusOK, `{"address_group":{"id":"`+miscGroupID+`"}}`)
			})
			fakeServer.Mux.HandleFunc("/address-groups/"+miscGroupID+"/add_addresses", func(w http.ResponseWriter, r *http.Request) {
				th.TestJSONRequest(t, r, `{"addresses":["198.51.100.1/32"]}`)
				writeJSON(t, w, http.StatusOK, `{"address_group":{"id":"`+miscGroupID+`"}}`)
			})
			if err := runAddressGroupSet(context.Background(), networkClient(fakeServer), &output.Options{Format: "value"},
				miscGroupID, &tc.f, &bytes.Buffer{}); err != nil {
				t.Fatalf("runAddressGroupSet: %v", err)
			}
			if put != tc.wantPut {
				t.Errorf("PUT sent = %v, want %v", put, tc.wantPut)
			}
		})
	}
}

func TestRunAddressGroupList_ColumnsMatchUpstream(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/address-groups", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, `{"address_groups":[{"id":"`+miscGroupID+`","name":"ag1",
		  "project_id":"`+miscProjectID+`","addresses":["192.0.2.0/24"]}]}`)
	})
	var buf bytes.Buffer
	if err := runAddressGroupList(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatCSV},
		"", "", &buf); err != nil {
		t.Fatalf("runAddressGroupList: %v", err)
	}
	if got := csvHeader(buf.String()); got != "ID,Name,Description,Project,Addresses" {
		t.Errorf("header = %s", got)
	}
	if !strings.Contains(buf.String(), miscProjectID) {
		t.Errorf("output missing the project:\n%s", buf.String())
	}
}

func TestExec_AddressGroupCreateAndList_ResolveProjectInDomain(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	handleProjectLookup(t, fakeServer, map[string]string{"demo": miscProjectID})
	fakeServer.Mux.HandleFunc("/v2.0/address-groups", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			th.AssertEquals(t, miscProjectID, r.URL.Query().Get("project_id"))
			writeJSON(t, w, http.StatusOK, `{"address_groups":[]}`)
		case http.MethodPost:
			th.TestJSONRequest(t, r, `{"address_group":{"name":"ag1","project_id":"`+miscProjectID+`","addresses":[]}}`)
			writeJSON(t, w, http.StatusCreated, `{"address_group":{"id":"`+miscGroupID+`"}}`)
		}
	})
	if out, err := execNetworkWithIdentity(t, fakeServer, "address", "group", "list",
		"--project", "demo", "--project-domain", "dom"); err != nil {
		t.Fatalf("address group list: %v (%s)", err, out)
	}
	if out, err := execNetworkWithIdentity(t, fakeServer, "address", "group", "create", "ag1",
		"--project", "demo", "--project-domain", "dom"); err != nil {
		t.Fatalf("address group create: %v (%s)", err, out)
	}
}

// --- ip availability ---------------------------------------------------------

func TestExec_IPAvailabilityList_ResolvesProjectInDomain(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	handleProjectLookup(t, fakeServer, map[string]string{"demo": miscProjectID})
	fakeServer.Mux.HandleFunc("/v2.0/network-ip-availabilities", func(w http.ResponseWriter, r *http.Request) {
		want := map[string][]string{"project_id": {miscProjectID}, "ip_version": {"6"}}
		if got := map[string][]string(r.URL.Query()); !reflect.DeepEqual(got, want) {
			t.Errorf("query = %v, want %v", got, want)
		}
		writeJSON(t, w, http.StatusOK, `{"network_ip_availabilities":[]}`)
	})
	if out, err := execNetworkWithIdentity(t, fakeServer, "ip", "availability", "list",
		"--project", "demo", "--project-domain", "dom", "--ip-version", "6"); err != nil {
		t.Fatalf("ip availability list: %v (%s)", err, out)
	}
}

package network

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

const (
	qosPolicyID = "dddd1010-1010-1010-1010-dddddddddddd"
	qosRuleID   = "eeee2020-2020-2020-2020-eeeeeeeeeeee"
)

// qosPolicyBody is the policy as neutron returns it: the rules are inline, and
// each carries its own "type", which is how the rule verbs find their endpoint.
const qosPolicyBody = `{"policy": {
  "id": "` + qosPolicyID + `", "name": "gold", "description": "", "shared": false,
  "is_default": false, "project_id": "99999999-9999-9999-9999-999999999999",
  "rules": [
    {"id": "` + qosRuleID + `", "type": "bandwidth_limit", "max_kbps": 1000,
     "max_burst_kbps": 0, "direction": "egress"}
  ]}}`

func handleQoSPolicyLookup(t *testing.T, fakeServer th.FakeServer) {
	t.Helper()
	fakeServer.Mux.HandleFunc("/qos/policies", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"policies": [{"id": "` + qosPolicyID + `", "name": "gold"}]}`))
	})
	fakeServer.Mux.HandleFunc("/qos/policies/"+qosPolicyID, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(qosPolicyBody))
	})
}

func TestRunQoSPolicySet_ShareAndDefaultReachNeutronExplicitly(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/qos/policies", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"policies": [{"id": "` + qosPolicyID + `", "name": "gold"}]}`))
	})
	var body map[string]any
	fakeServer.Mux.HandleFunc("/qos/policies/"+qosPolicyID, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decoding request body: %v", err)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(qosPolicyBody))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	// --no-share plus --no-default: both are false, and both must still be sent.
	err := runQoSPolicySet(context.Background(), networkClient(fakeServer), o, "gold",
		"", "", false, true, false, true, false, &out)
	if err != nil {
		t.Fatalf("runQoSPolicySet returned error: %v", err)
	}
	policy := body["policy"].(map[string]any)
	th.AssertEquals(t, false, policy["shared"])
	th.AssertEquals(t, false, policy["is_default"])
	if _, present := policy["description"]; present {
		t.Errorf("description sent although --description was not given: %#v", policy)
	}
}

func TestRunQoSRuleList_ReadsRulesOffThePolicy(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	handleQoSPolicyLookup(t, fakeServer)

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	if err := runQoSRuleList(context.Background(), networkClient(fakeServer), o, "gold", &out); err != nil {
		t.Fatalf("runQoSRuleList returned error: %v", err)
	}
	// Neutron has no combined rule collection, so the policy's inline rules are
	// the list — including the settings of types koc does not model.
	got := out.String()
	if !strings.Contains(got, qosRuleID) || !strings.Contains(got, "bandwidth_limit") {
		t.Errorf("rule row missing from output:\n%s", got)
	}
	if !strings.Contains(got, "max_kbps=1000") {
		t.Errorf("rule properties missing from output:\n%s", got)
	}
}

func TestRunQoSRuleCreate_PostsToTheTypeSpecificCollection(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	handleQoSPolicyLookup(t, fakeServer)

	var gotPath string
	var body map[string]any
	fakeServer.Mux.HandleFunc("/qos/policies/"+qosPolicyID+"/minimum_bandwidth_rules",
		func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			th.AssertEquals(t, http.MethodPost, r.Method)
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decoding request body: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"minimum_bandwidth_rule": {"id": "` + qosRuleID + `",
			  "min_kbps": 500, "direction": "egress"}}`))
		})

	f := &qosRuleFlags{minKBps: 500, direction: "egress"}
	k, err := qosRuleKindByCLIType("minimum-bandwidth")
	if err != nil {
		t.Fatalf("qosRuleKindByCLIType returned error: %v", err)
	}
	attrs := f.body(k, changedFlags{"min-kbps": true, "direction": true})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	if err := runQoSRuleCreate(context.Background(), networkClient(fakeServer), o, "gold", k, attrs, &out); err != nil {
		t.Fatalf("runQoSRuleCreate returned error: %v", err)
	}
	th.AssertEquals(t, "/qos/policies/"+qosPolicyID+"/minimum_bandwidth_rules", gotPath)
	rule := body["minimum_bandwidth_rule"].(map[string]any)
	th.AssertEquals(t, float64(500), rule["min_kbps"])
	th.AssertEquals(t, "egress", rule["direction"])
}

func TestRunQoSRuleSet_DiscoversTheRuleTypeFromThePolicy(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	handleQoSPolicyLookup(t, fakeServer)

	var gotPath string
	var body map[string]any
	fakeServer.Mux.HandleFunc("/qos/policies/"+qosPolicyID+"/bandwidth_limit_rules/"+qosRuleID,
		func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			th.AssertEquals(t, http.MethodPut, r.Method)
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decoding request body: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"bandwidth_limit_rule": {"id": "` + qosRuleID + `",
			  "max_kbps": 2000, "max_burst_kbps": 0, "direction": "egress"}}`))
		})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	f := &qosRuleFlags{maxKBps: 2000}
	err := runQoSRuleSet(context.Background(), networkClient(fakeServer), o, "gold", qosRuleID,
		f, changedFlags{"max-kbps": true}, &out)
	if err != nil {
		t.Fatalf("runQoSRuleSet returned error: %v", err)
	}
	// The caller never says which type the rule is: it comes from the policy's
	// inline rules, which is what picks bandwidth_limit_rules here.
	th.AssertEquals(t, "/qos/policies/"+qosPolicyID+"/bandwidth_limit_rules/"+qosRuleID, gotPath)
	rule := body["bandwidth_limit_rule"].(map[string]any)
	th.AssertEquals(t, float64(2000), rule["max_kbps"])
	if _, present := rule["direction"]; present {
		t.Errorf("direction sent although --direction was not given: %#v", rule)
	}
}

func TestRunQoSRuleSet_RejectsAnEmptyUpdate(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	handleQoSPolicyLookup(t, fakeServer)

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	err := runQoSRuleSet(context.Background(), networkClient(fakeServer), o, "gold", qosRuleID,
		&qosRuleFlags{}, changedFlags{}, &out)
	if err == nil {
		t.Fatalf("a set with no property flags was accepted")
	}
}

func TestQoSRuleFlagsBody_IgnoresFlagsFromOtherRuleTypes(t *testing.T) {
	k, err := qosRuleKindByCLIType("dscp-marking")
	if err != nil {
		t.Fatalf("qosRuleKindByCLIType returned error: %v", err)
	}
	f := &qosRuleFlags{dscpMark: 26, maxKBps: 1000, direction: "egress"}
	// A dscp-marking rule has no bandwidth or direction attribute; sending one
	// would be a 400 from neutron.
	attrs := f.body(k, changedFlags{"dscp-mark": true, "max-kbps": true, "direction": true})
	th.AssertDeepEquals(t, map[string]any{"dscp_mark": 26}, attrs)
}

func TestQoSRuleKindByCLIType_ErrorNamesTheValidTypes(t *testing.T) {
	_, err := qosRuleKindByCLIType("bandwidth_limit")
	if err == nil {
		t.Fatalf("neutron's own spelling was accepted as a --type value")
	}
	if !strings.Contains(err.Error(), "minimum-packet-rate") {
		t.Errorf("error does not list the valid types: %v", err)
	}
}

// The body builder keys off flag names, so a rename on the command would
// silently stop sending the attribute. Pin the two together.
func TestQoSRuleFlags_RegisteredNamesMatchTheBodyBuilder(t *testing.T) {
	fl := newQoSRuleCreateCommand(nil, nil).Flags()
	for _, name := range []string{"type", "max-kbps", "max-burst-kbits", "min-kbps", "min-kpps", "dscp-mark", "direction"} {
		if fl.Lookup(name) == nil {
			t.Errorf("qos rule create is missing the --%s flag", name)
		}
	}
}

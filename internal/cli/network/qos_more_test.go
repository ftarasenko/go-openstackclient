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

// --- qos policy list/show/create/delete -------------------------------------

func TestRunQoSPolicyList_ShareFilterAndOutput(t *testing.T) {
	for _, tc := range []struct {
		name             string
		share, noShare   bool
		wantQuerySubstr  string
		wantNoQueryShare bool
	}{
		{name: "share", share: true, wantQuerySubstr: "shared=true"},
		{name: "no-share", noShare: true, wantQuerySubstr: "shared=false"},
		{name: "neither", wantNoQueryShare: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()

			var gotQuery string
			fakeServer.Mux.HandleFunc("/qos/policies", func(w http.ResponseWriter, r *http.Request) {
				gotQuery = r.URL.RawQuery
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"policies": [{"id": "` + qosPolicyID + `", "name": "gold",
				  "shared": true, "is_default": false, "project_id": "p1"}]}`))
			})

			var out bytes.Buffer
			o := &output.Options{Format: output.FormatTable}
			err := runQoSPolicyList(context.Background(), networkClient(fakeServer), o, "", tc.share, tc.noShare, &out)
			if err != nil {
				t.Fatalf("runQoSPolicyList returned error: %v", err)
			}
			if tc.wantNoQueryShare {
				if strings.Contains(gotQuery, "shared") {
					t.Errorf("no filter requested but query was %q", gotQuery)
				}
			} else if !strings.Contains(gotQuery, tc.wantQuerySubstr) {
				t.Errorf("query %q is missing %q", gotQuery, tc.wantQuerySubstr)
			}
			for _, want := range []string{"ID", "Name", qosPolicyID, "gold"} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("policy list output missing %q\n---\n%s", want, out.String())
				}
			}
		})
	}
}

func TestRunQoSPolicyList_ListError(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/qos/policies", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	var out bytes.Buffer
	o := &output.Options{Format: output.FormatTable}
	err := runQoSPolicyList(context.Background(), networkClient(fakeServer), o, "", false, false, &out)
	if err == nil {
		t.Fatal("expected an error when the policy list request fails")
	}
	if !strings.Contains(err.Error(), "listing network QoS policies") {
		t.Errorf("error does not name the failing operation: %v", err)
	}
}

func TestRunQoSPolicyShow_ResolvesNameAndRendersFields(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	handleQoSPolicyLookup(t, fakeServer)

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	if err := runQoSPolicyShow(context.Background(), networkClient(fakeServer), o, "gold", &out); err != nil {
		t.Fatalf("runQoSPolicyShow returned error: %v", err)
	}
	if !strings.Contains(out.String(), qosPolicyID) {
		t.Errorf("output is missing the resolved policy ID:\n%s", out.String())
	}
}

func TestRunQoSPolicyShow_NotFound(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	// The name filter matches nothing, so the ref falls back to a literal ID,
	// and that ID does not exist either.
	fakeServer.Mux.HandleFunc("/qos/policies", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"policies": []}`))
	})
	fakeServer.Mux.HandleFunc("/qos/policies/missing", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	err := runQoSPolicyShow(context.Background(), networkClient(fakeServer), o, "missing", &out)
	if err == nil {
		t.Fatal("expected an error for a QoS policy that does not exist")
	}
	if !strings.Contains(err.Error(), "showing network QoS policy missing") {
		t.Errorf("error does not name the ref: %v", err)
	}
}

func TestRunQoSPolicyCreate_RequestBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var body map[string]any
	fakeServer.Mux.HandleFunc("/qos/policies", func(w http.ResponseWriter, r *http.Request) {
		th.AssertEquals(t, http.MethodPost, r.Method)
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(qosPolicyBody))
	})

	f := &qosPolicyCreateFlags{
		description: "gold tier",
		project:     "p1",
		share:       true,
		isDefault:   true,
	}
	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	if err := runQoSPolicyCreate(context.Background(), networkClient(fakeServer), o, "gold", f, &out); err != nil {
		t.Fatalf("runQoSPolicyCreate returned error: %v", err)
	}
	policy := body["policy"].(map[string]any)
	th.AssertEquals(t, "gold", policy["name"])
	th.AssertEquals(t, "gold tier", policy["description"])
	th.AssertEquals(t, "p1", policy["project_id"])
	th.AssertEquals(t, true, policy["shared"])
	th.AssertEquals(t, true, policy["is_default"])
}

func TestRunQoSPolicyCreate_Error(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/qos/policies", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	err := runQoSPolicyCreate(context.Background(), networkClient(fakeServer), o, "gold", &qosPolicyCreateFlags{}, &out)
	if err == nil {
		t.Fatal("expected an error when policy creation fails")
	}
	if !strings.Contains(err.Error(), `creating network QoS policy "gold"`) {
		t.Errorf("error does not name the policy: %v", err)
	}
}

// TestRunQoSPolicyDelete_AggregatesFailures confirms a mid-batch failure does
// not abort the remaining deletes and that the returned error names the
// failed ref, mirroring the trunk-delete batching contract.
func TestRunQoSPolicyDelete_AggregatesFailures(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	// The name lookup always misses, so every ref falls back to a literal ID.
	fakeServer.Mux.HandleFunc("/qos/policies", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"policies": []}`))
	})

	var deleted []string
	fakeServer.Mux.HandleFunc("/qos/policies/p1", func(w http.ResponseWriter, _ *http.Request) {
		deleted = append(deleted, "p1")
		w.WriteHeader(http.StatusNoContent)
	})
	fakeServer.Mux.HandleFunc("/qos/policies/bad", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
	})
	fakeServer.Mux.HandleFunc("/qos/policies/p2", func(w http.ResponseWriter, _ *http.Request) {
		deleted = append(deleted, "p2")
		w.WriteHeader(http.StatusNoContent)
	})

	err := runQoSPolicyDelete(context.Background(), networkClient(fakeServer), []string{"p1", "bad", "p2"})
	if err == nil {
		t.Fatal("runQoSPolicyDelete returned nil error; want a failure for the bad ref")
	}
	if !strings.Contains(err.Error(), "bad") {
		t.Errorf("error missing failed ref %q: %v", "bad", err)
	}
	if len(deleted) != 2 || deleted[0] != "p1" || deleted[1] != "p2" {
		t.Errorf("deleted = %v, want both [p1 p2] attempted despite the failure between them", deleted)
	}
}

// --- qos rule show/delete -----------------------------------------------------

func TestRunQoSRuleShow_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	handleQoSPolicyLookup(t, fakeServer)

	var gotPath string
	fakeServer.Mux.HandleFunc("/qos/policies/"+qosPolicyID+"/bandwidth_limit_rules/"+qosRuleID,
		func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			th.AssertEquals(t, http.MethodGet, r.Method)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"bandwidth_limit_rule": {"id": "` + qosRuleID + `",
			  "max_kbps": 1000, "direction": "egress"}}`))
		})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	if err := runQoSRuleShow(context.Background(), networkClient(fakeServer), o, "gold", qosRuleID, &out); err != nil {
		t.Fatalf("runQoSRuleShow returned error: %v", err)
	}
	// The rule's type is discovered from the policy's inline rules, which is
	// what picks bandwidth_limit_rules here without the caller saying so.
	if gotPath != "/qos/policies/"+qosPolicyID+"/bandwidth_limit_rules/"+qosRuleID {
		t.Errorf("path = %q, want the bandwidth_limit_rules endpoint", gotPath)
	}
	if !strings.Contains(out.String(), "1000") {
		t.Errorf("output missing the rule's max_kbps:\n%s", out.String())
	}
}

func TestRunQoSRuleShow_UnknownRuleID(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	handleQoSPolicyLookup(t, fakeServer)

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	err := runQoSRuleShow(context.Background(), networkClient(fakeServer), o, "gold", "no-such-rule", &out)
	if err == nil {
		t.Fatal("expected an error for a rule the policy does not have")
	}
	if !strings.Contains(err.Error(), "no rule no-such-rule") {
		t.Errorf("error does not explain the missing rule: %v", err)
	}
}

func TestRunQoSRuleDelete_UsesTheRuleTypeSpecificEndpoint(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	handleQoSPolicyLookup(t, fakeServer)

	var gotMethod, gotPath string
	fakeServer.Mux.HandleFunc("/qos/policies/"+qosPolicyID+"/bandwidth_limit_rules/"+qosRuleID,
		func(w http.ResponseWriter, r *http.Request) {
			gotMethod, gotPath = r.Method, r.URL.Path
			w.WriteHeader(http.StatusNoContent)
		})

	err := runQoSRuleDelete(context.Background(), networkClient(fakeServer), "gold", []string{qosRuleID})
	if err != nil {
		t.Fatalf("runQoSRuleDelete returned error: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", gotMethod)
	}
	if gotPath != "/qos/policies/"+qosPolicyID+"/bandwidth_limit_rules/"+qosRuleID {
		t.Errorf("path = %q, want the bandwidth_limit_rules endpoint", gotPath)
	}
}

// TestRunQoSRuleDelete_AggregatesFailures confirms one bad rule ID in a batch
// does not stop the rest from being attempted.
func TestRunQoSRuleDelete_AggregatesFailures(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	handleQoSPolicyLookup(t, fakeServer)

	var deleted []string
	fakeServer.Mux.HandleFunc("/qos/policies/"+qosPolicyID+"/bandwidth_limit_rules/"+qosRuleID,
		func(w http.ResponseWriter, _ *http.Request) {
			deleted = append(deleted, qosRuleID)
			w.WriteHeader(http.StatusNoContent)
		})

	err := runQoSRuleDelete(context.Background(), networkClient(fakeServer), "gold", []string{qosRuleID, "no-such-rule"})
	if err == nil {
		t.Fatal("expected an error for the unknown rule ID")
	}
	if !strings.Contains(err.Error(), "no-such-rule") {
		t.Errorf("error missing the failed ref: %v", err)
	}
	if len(deleted) != 1 {
		t.Errorf("deleted = %v, want the valid rule to still be attempted", deleted)
	}
}

// --- qos rule type show -------------------------------------------------------

const qosRuleTypeBody = `{"rule_type": {"type": "bandwidth_limit", "drivers": [
  {"name": "linuxbridge", "supported_parameters": [
    {"parameter_name": "max_kbps", "parameter_type": "range", "parameter_values": {"start": 0, "end": 2147483647}}
  ]}
]}}`

func TestRunQoSRuleTypeShow_AcceptsCLIAndAPISpelling(t *testing.T) {
	for _, name := range []string{"bandwidth-limit", "bandwidth_limit"} {
		t.Run(name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()

			var gotPath string
			fakeServer.Mux.HandleFunc("/qos/rule-types/bandwidth_limit", func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				th.AssertEquals(t, http.MethodGet, r.Method)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(qosRuleTypeBody))
			})

			var out bytes.Buffer
			o := &output.Options{Format: "value"}
			if err := runQoSRuleTypeShow(context.Background(), networkClient(fakeServer), o, name, &out); err != nil {
				t.Fatalf("runQoSRuleTypeShow(%q) returned error: %v", name, err)
			}
			if gotPath != "/qos/rule-types/bandwidth_limit" {
				t.Errorf("path = %q, want the underscored API spelling", gotPath)
			}
			for _, want := range []string{"bandwidth_limit", "linuxbridge", "max_kbps"} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("rule type show output missing %q\n---\n%s", want, out.String())
				}
			}
		})
	}
}

func TestRunQoSRuleTypeShow_NotFound(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/qos/rule-types/minimum_packet_rate", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	err := runQoSRuleTypeShow(context.Background(), networkClient(fakeServer), o, "minimum-packet-rate", &out)
	if err == nil {
		t.Fatal("expected an error for a rule type the cloud does not support")
	}
	if !strings.Contains(err.Error(), "showing QoS rule type minimum_packet_rate") {
		t.Errorf("error does not name the rule type: %v", err)
	}
}

// --- address scope create/delete ---------------------------------------------

func TestRunAddressScopeCreate_RequestBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var body map[string]any
	fakeServer.Mux.HandleFunc("/address-scopes", func(w http.ResponseWriter, r *http.Request) {
		th.AssertEquals(t, http.MethodPost, r.Method)
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"address_scope": {"id": "` + addressScopeID + `", "name": "public",
		  "ip_version": 6, "shared": true, "project_id": "p1"}}`))
	})

	f := &addressScopeCreateFlags{ipVersion: 6, share: true, project: "p1"}
	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	if err := runAddressScopeCreate(context.Background(), networkClient(fakeServer), o, "public", f, &out); err != nil {
		t.Fatalf("runAddressScopeCreate returned error: %v", err)
	}
	scope := body["address_scope"].(map[string]any)
	th.AssertEquals(t, "public", scope["name"])
	th.AssertEquals(t, float64(6), scope["ip_version"])
	th.AssertEquals(t, true, scope["shared"])
	th.AssertEquals(t, "p1", scope["project_id"])
	if !strings.Contains(out.String(), addressScopeID) {
		t.Errorf("output missing the created scope's ID:\n%s", out.String())
	}
}

func TestRunAddressScopeCreate_Error(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/address-scopes", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	err := runAddressScopeCreate(context.Background(), networkClient(fakeServer), o, "public", &addressScopeCreateFlags{}, &out)
	if err == nil {
		t.Fatal("expected an error when address scope creation fails")
	}
	if !strings.Contains(err.Error(), `creating address scope "public"`) {
		t.Errorf("error does not name the scope: %v", err)
	}
}

func TestRunAddressScopeDelete_AggregatesFailures(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var deleted []string
	fakeServer.Mux.HandleFunc("/address-scopes/"+addressScopeID, func(w http.ResponseWriter, _ *http.Request) {
		deleted = append(deleted, addressScopeID)
		w.WriteHeader(http.StatusNoContent)
	})
	fakeServer.Mux.HandleFunc("/address-scopes/00000000-0000-0000-0000-000000000bad", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
	})

	// Both refs are UUIDs, so resolveAddressScopeID short-circuits without a
	// list call for either.
	err := runAddressScopeDelete(context.Background(), networkClient(fakeServer),
		[]string{addressScopeID, "00000000-0000-0000-0000-000000000bad"})
	if err == nil {
		t.Fatal("expected an error for the failed delete")
	}
	if !strings.Contains(err.Error(), "00000000-0000-0000-0000-000000000bad") {
		t.Errorf("error missing the failed ref: %v", err)
	}
	if len(deleted) != 1 {
		t.Errorf("deleted = %v, want the good ref to still be attempted", deleted)
	}
}

// --- address group list/delete ------------------------------------------------

func TestRunAddressGroupList_NameAndProjectFilters(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/address-groups", func(w http.ResponseWriter, r *http.Request) {
		th.TestFormValues(t, r, map[string]string{"name": "trusted", "project_id": "p1"})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"address_groups": [{"id": "` + addressGroupID + `", "name": "trusted",
		  "description": "trusted hosts", "addresses": ["192.0.2.0/24"]}]}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: output.FormatTable}
	if err := runAddressGroupList(context.Background(), networkClient(fakeServer), o, "trusted", "p1", &out); err != nil {
		t.Fatalf("runAddressGroupList returned error: %v", err)
	}
	for _, want := range []string{"ID", "Name", addressGroupID, "trusted", "192.0.2.0/24"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("address group list output missing %q\n---\n%s", want, out.String())
		}
	}
}

func TestRunAddressGroupList_Error(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/address-groups", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	var out bytes.Buffer
	o := &output.Options{Format: output.FormatTable}
	err := runAddressGroupList(context.Background(), networkClient(fakeServer), o, "", "", &out)
	if err == nil {
		t.Fatal("expected an error when the address group list request fails")
	}
	if !strings.Contains(err.Error(), "listing address groups") {
		t.Errorf("error does not name the failing operation: %v", err)
	}
}

func TestRunAddressGroupDelete_AggregatesFailures(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var deleted []string
	fakeServer.Mux.HandleFunc("/address-groups/"+addressGroupID, func(w http.ResponseWriter, _ *http.Request) {
		deleted = append(deleted, addressGroupID)
		w.WriteHeader(http.StatusNoContent)
	})
	fakeServer.Mux.HandleFunc("/address-groups/00000000-0000-0000-0000-0000000000ba", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
	})

	err := runAddressGroupDelete(context.Background(), networkClient(fakeServer),
		[]string{addressGroupID, "00000000-0000-0000-0000-0000000000ba"})
	if err == nil {
		t.Fatal("expected an error for the failed delete")
	}
	if len(deleted) != 1 {
		t.Errorf("deleted = %v, want the good ref to still be attempted", deleted)
	}
}

// --- subnet pool show/delete --------------------------------------------------

const subnetPoolShowBody = `{"subnetpool": {
  "id": "sp1", "name": "shared-v4", "project_id": "p1",
  "prefixes": ["10.0.0.0/8"], "default_prefixlen": "24", "min_prefixlen": "8",
  "max_prefixlen": "32", "ip_version": 4, "shared": true, "is_default": true
}}`

func TestRunSubnetPoolShow_ResolvesNameAndRendersFields(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var listQuery string
	fakeServer.Mux.HandleFunc("/subnetpools", func(w http.ResponseWriter, r *http.Request) {
		listQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"subnetpools": [{"id": "sp1", "name": "shared-v4",
		  "default_prefixlen": "24", "min_prefixlen": "8", "max_prefixlen": "32"}]}`))
	})
	fakeServer.Mux.HandleFunc("/subnetpools/sp1", func(w http.ResponseWriter, r *http.Request) {
		th.AssertEquals(t, http.MethodGet, r.Method)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(subnetPoolShowBody))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	if err := runSubnetPoolShow(context.Background(), networkClient(fakeServer), o, "shared-v4", &out); err != nil {
		t.Fatalf("runSubnetPoolShow returned error: %v", err)
	}
	if !strings.Contains(listQuery, "name=shared-v4") {
		t.Errorf("query %q did not filter by name", listQuery)
	}
	if !strings.Contains(out.String(), "sp1") {
		t.Errorf("output missing the resolved pool ID:\n%s", out.String())
	}
}

func TestRunSubnetPoolShow_NotFound(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/subnetpools", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"subnetpools": []}`))
	})
	fakeServer.Mux.HandleFunc("/subnetpools/missing", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	err := runSubnetPoolShow(context.Background(), networkClient(fakeServer), o, "missing", &out)
	if err == nil {
		t.Fatal("expected an error for a subnet pool that does not exist")
	}
	if !strings.Contains(err.Error(), `showing subnet pool "missing"`) {
		t.Errorf("error does not name the ref: %v", err)
	}
}

func TestRunSubnetPoolDelete_AggregatesFailuresAndReportsSuccesses(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/subnetpools", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"subnetpools": []}`))
	})
	var deleted []string
	fakeServer.Mux.HandleFunc("/subnetpools/sp1", func(w http.ResponseWriter, _ *http.Request) {
		deleted = append(deleted, "sp1")
		w.WriteHeader(http.StatusNoContent)
	})
	fakeServer.Mux.HandleFunc("/subnetpools/bad", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
	})

	var buf bytes.Buffer
	err := runSubnetPoolDelete(context.Background(), networkClient(fakeServer), []string{"sp1", "bad"}, &buf)
	if err == nil {
		t.Fatal("expected an error for the failed delete")
	}
	if !strings.Contains(err.Error(), "bad") {
		t.Errorf("error missing the failed ref: %v", err)
	}
	if !strings.Contains(buf.String(), "Deleted subnet pool sp1") {
		t.Errorf("output missing the success message for sp1:\n%s", buf.String())
	}
	if len(deleted) != 1 {
		t.Errorf("deleted = %v, want exactly sp1", deleted)
	}
}

// --- trunk show/set/subport add -----------------------------------------------

const trunkShowBody = `{"trunk": {
  "id": "t1", "name": "trunk-a", "port_id": "parent-1", "status": "ACTIVE",
  "admin_state_up": true, "description": "vm-a trunk",
  "sub_ports": [{"port_id": "sub-1", "segmentation_type": "vlan", "segmentation_id": 101}]
}}`

func TestRunTrunkShow_ResolvesNameAndRendersFields(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/trunks", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"trunks": [{"id": "t1", "name": "trunk-a"}]}`))
	})
	fakeServer.Mux.HandleFunc("/trunks/t1", func(w http.ResponseWriter, r *http.Request) {
		th.AssertEquals(t, http.MethodGet, r.Method)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(trunkShowBody))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	if err := runTrunkShow(context.Background(), networkClient(fakeServer), o, "trunk-a", &out); err != nil {
		t.Fatalf("runTrunkShow returned error: %v", err)
	}
	for _, want := range []string{"t1", "parent-1", "sub-1:vlan:101"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("trunk show output missing %q\n---\n%s", want, out.String())
		}
	}
}

func TestRunTrunkShow_NotFound(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/trunks", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"trunks": []}`))
	})
	fakeServer.Mux.HandleFunc("/trunks/missing", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	err := runTrunkShow(context.Background(), networkClient(fakeServer), o, "missing", &out)
	if err == nil {
		t.Fatal("expected an error for a trunk that does not exist")
	}
}

// runTrunkSet must send only the attributes actually given: an unrelated
// --description cannot silently touch the trunk's admin state.
func TestRunTrunkSet_OnlySendsGivenAttributes(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/trunks", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"trunks": []}`))
	})
	var body map[string]any
	fakeServer.Mux.HandleFunc("/trunks/t1", func(w http.ResponseWriter, r *http.Request) {
		th.AssertEquals(t, http.MethodPut, r.Method)
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(trunkShowBody))
	})

	f := &trunkSetFlags{description: "new description", descSet: true}
	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	if err := runTrunkSet(context.Background(), networkClient(fakeServer), o, "t1", f, &out); err != nil {
		t.Fatalf("runTrunkSet returned error: %v", err)
	}
	trunk := body["trunk"].(map[string]any)
	th.AssertEquals(t, "new description", trunk["description"])
	if _, present := trunk["name"]; present {
		t.Errorf("name sent although --name was not given: %#v", trunk)
	}
	if _, present := trunk["admin_state_up"]; present {
		t.Errorf("admin_state_up sent although neither --enable nor --disable was given: %#v", trunk)
	}
}

func TestRunTrunkSet_EnableSendsAdminStateUp(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/trunks", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"trunks": []}`))
	})
	var body map[string]any
	fakeServer.Mux.HandleFunc("/trunks/t1", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(trunkShowBody))
	})

	up := true
	f := &trunkSetFlags{adminStateUp: &up}
	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	if err := runTrunkSet(context.Background(), networkClient(fakeServer), o, "t1", f, &out); err != nil {
		t.Fatalf("runTrunkSet returned error: %v", err)
	}
	trunk := body["trunk"].(map[string]any)
	th.AssertEquals(t, true, trunk["admin_state_up"])
	if _, present := trunk["description"]; present {
		t.Errorf("description sent although --description was not given: %#v", trunk)
	}
}

func TestRunTrunkSubportAdd_RequestBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/trunks", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"trunks": []}`))
	})
	fakeServer.Mux.HandleFunc("/ports", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ports": []}`))
	})
	var gotMethod string
	var body map[string]any
	fakeServer.Mux.HandleFunc("/trunks/t1/add_subports", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(trunkShowBody))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	specs := []string{"port=sub-1,segmentation-type=vlan,segmentation-id=101"}
	if err := runTrunkSubportAdd(context.Background(), networkClient(fakeServer), o, "t1", specs, &out); err != nil {
		t.Fatalf("runTrunkSubportAdd returned error: %v", err)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
	subports, ok := body["sub_ports"].([]any)
	if !ok || len(subports) != 1 {
		t.Fatalf("request body sub_ports = %#v, want one entry", body["sub_ports"])
	}
	sp := subports[0].(map[string]any)
	th.AssertEquals(t, "sub-1", sp["port_id"])
	th.AssertEquals(t, "vlan", sp["segmentation_type"])
	th.AssertEquals(t, float64(101), sp["segmentation_id"])
}

func TestRunTrunkSubportAdd_InvalidSpecIsRejectedBeforeAnyRequest(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/trunks", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"trunks": []}`))
	})
	// No /ports handler: parseSubports must fail validating the spec before it
	// ever tries to resolve a port, so any port HTTP call would 404 the test.

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	err := runTrunkSubportAdd(context.Background(), networkClient(fakeServer), o, "t1",
		[]string{"port=sub-1,segmentation-type=vlan"}, &out)
	if err == nil || !strings.Contains(err.Error(), "requires port, segmentation-type") {
		t.Fatalf("expected a validation error, got %v", err)
	}
}

// --- router unset / remove port -----------------------------------------------

func TestRunRouterUnset_RequiresExternalGatewayFlag(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	// No handlers: the flag check must fail before any request is made.

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	err := runRouterUnset(context.Background(), networkClient(fakeServer), o, "r1", false, &out)
	if err == nil || !strings.Contains(err.Error(), "requires --external-gateway") {
		t.Fatalf("expected a missing-flag error, got %v", err)
	}
}

func TestRunRouterUnset_ClearsTheExternalGateway(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/routers", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"routers": []}`))
	})
	var gotMethod string
	var body map[string]any
	fakeServer.Mux.HandleFunc("/routers/r1", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"router": {"id": "r1", "name": "gw", "external_gateway_info": null}}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	if err := runRouterUnset(context.Background(), networkClient(fakeServer), o, "r1", true, &out); err != nil {
		t.Fatalf("runRouterUnset returned error: %v", err)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
	router := body["router"].(map[string]any)
	gw, present := router["external_gateway_info"]
	if !present {
		t.Fatalf("external_gateway_info omitted from the request body: %#v", router)
	}
	th.AssertDeepEquals(t, map[string]any{}, gw)
}

func TestRunRouterUnset_Error(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/routers", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"routers": []}`))
	})
	fakeServer.Mux.HandleFunc("/routers/r1", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	err := runRouterUnset(context.Background(), networkClient(fakeServer), o, "r1", true, &out)
	if err == nil {
		t.Fatal("expected an error when clearing the gateway fails")
	}
	if !strings.Contains(err.Error(), "clearing external gateway on router r1") {
		t.Errorf("error does not name the router: %v", err)
	}
}

func TestRunRouterRemovePort_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/routers", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"routers": []}`))
	})
	fakeServer.Mux.HandleFunc("/ports", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ports": []}`))
	})
	var gotMethod string
	var body map[string]any
	fakeServer.Mux.HandleFunc("/routers/router-1/remove_router_interface", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id": "router-1", "port_id": "port-1", "subnet_id": "subnet-1"}`))
	})

	var out bytes.Buffer
	if err := runRouterRemovePort(context.Background(), networkClient(fakeServer), "router-1", "port-1", &out); err != nil {
		t.Fatalf("runRouterRemovePort returned error: %v", err)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
	th.AssertEquals(t, "port-1", body["port_id"])
	if !strings.Contains(out.String(), "Removed interface for port port-1 from router router-1") {
		t.Errorf("unexpected output: %q", out.String())
	}
}

func TestRunRouterRemovePort_Error(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/routers", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"routers": []}`))
	})
	fakeServer.Mux.HandleFunc("/ports", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ports": []}`))
	})
	fakeServer.Mux.HandleFunc("/routers/router-1/remove_router_interface", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
	})

	var out bytes.Buffer
	err := runRouterRemovePort(context.Background(), networkClient(fakeServer), "router-1", "port-1", &out)
	if err == nil {
		t.Fatal("expected an error when removing the interface fails")
	}
	if !strings.Contains(err.Error(), "removing port port-1 from router router-1") {
		t.Errorf("error does not name router/port: %v", err)
	}
}

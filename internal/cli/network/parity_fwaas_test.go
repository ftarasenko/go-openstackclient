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

// Synthetic IDs; UUIDs so every resolver short-circuits without a lookup.
const (
	fwGroupID   = "aaaaaaaa-0000-4000-8000-000000000001"
	fwGroup2ID  = "aaaaaaaa-0000-4000-8000-000000000002"
	fwPolicyID  = "bbbbbbbb-0000-4000-8000-000000000001"
	fwPolicy2ID = "bbbbbbbb-0000-4000-8000-000000000002"
	fwRuleID    = "cccccccc-0000-4000-8000-000000000001"
	fwRule2ID   = "cccccccc-0000-4000-8000-000000000002"
	fwRule3ID   = "cccccccc-0000-4000-8000-000000000003"
	fwPortA     = "dddddddd-0000-4000-8000-00000000000a"
	fwPortB     = "dddddddd-0000-4000-8000-00000000000b"
	fwPortC     = "dddddddd-0000-4000-8000-00000000000c"
	fwProjectID = "eeeeeeee-0000-4000-8000-000000000001"
)

// fwJSON renders a seam's single-resource output as JSON and decodes it.
func fwJSON(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("decoding output %q: %v", buf.String(), err)
	}
	return m
}

func fwHeader(buf *bytes.Buffer) string {
	return strings.SplitN(buf.String(), "\n", 2)[0]
}

// --- firewall group -----------------------------------------------------------

func TestRunFirewallGroupList_ColumnsMatchUpstream(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/fwaas/firewall_groups", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		if r.URL.RawQuery != "" {
			t.Errorf("list sent query %q", r.URL.RawQuery)
		}
		writeJSON(t, w, http.StatusOK, `{"firewall_groups":[{"id":"`+fwGroupID+`","name":"g1",
		  "ingress_firewall_policy_id":"`+fwPolicyID+`","egress_firewall_policy_id":null,
		  "description":"d","status":"ACTIVE","ports":["`+fwPortA+`"],"admin_state_up":false,
		  "shared":true,"project_id":"`+fwProjectID+`"}]}`)
	})
	client := networkClient(fakeServer)
	o := &output.Options{Format: output.FormatCSV}

	var buf bytes.Buffer
	if err := runFirewallGroupList(context.Background(), client, o, false, &buf); err != nil {
		t.Fatalf("list: %v", err)
	}
	if got := fwHeader(&buf); got != "ID,Name,Ingress Policy ID,Egress Policy ID" {
		t.Errorf("default header = %s", got)
	}
	buf.Reset()
	if err := runFirewallGroupList(context.Background(), client, o, true, &buf); err != nil {
		t.Fatalf("list --long: %v", err)
	}
	if got := fwHeader(&buf); got != "ID,Name,Ingress Policy ID,Egress Policy ID,Description,Status,Ports,State,Shared,Project" {
		t.Errorf("long header = %s", got)
	}
	for _, want := range []string{"g1", fwPolicyID, "ACTIVE", fwPortA, "DOWN", fwProjectID} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("long output missing %q:\n%s", want, buf.String())
		}
	}
}

func TestRunFirewallGroupCreate_SendsUpstreamBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/fwaas/firewall_groups", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		// Ports are de-duplicated and sorted, --no-egress sends null, --disable false.
		th.TestJSONRequest(t, r, `{"firewall_group":{
		  "name":"g1","description":"d","ingress_firewall_policy_id":"`+fwPolicyID+`",
		  "egress_firewall_policy_id":null,"shared":true,"admin_state_up":false,
		  "ports":["`+fwPortA+`","`+fwPortB+`"],"project_id":"`+fwProjectID+`"}}`)
		writeJSON(t, w, http.StatusCreated, `{"firewall_group":{"id":"`+fwGroupID+`","name":"g1",
		  "admin_state_up":false,"ports":["`+fwPortA+`","`+fwPortB+`"],"status":"INACTIVE"}}`)
	})
	f := &fwGroupFlags{
		name: "g1", description: "d", ingress: fwPolicyID, noEgress: true,
		share: true, disable: true, ports: []string{fwPortB, fwPortA, fwPortB}, projectID: fwProjectID,
	}
	var buf bytes.Buffer
	if err := runFirewallGroupCreate(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatJSON}, f, &buf); err != nil {
		t.Fatalf("create: %v", err)
	}
	m := fwJSON(t, &buf)
	want := []string{"Description", "Egress Policy ID", "ID", "Ingress Policy ID", "Name", "Ports", "Project", "Shared", "State", "Status"}
	if len(m) != len(want) {
		t.Errorf("show fields = %v, want %v", m, want)
	}
	if m["ID"] != fwGroupID || m["State"] != "DOWN" || m["Status"] != "INACTIVE" {
		t.Errorf("rendered group = %v", m)
	}
}

func TestRunFirewallGroupCreate_NoPortSendsEmptyList(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/fwaas/firewall_groups", func(w http.ResponseWriter, r *http.Request) {
		th.TestJSONRequest(t, r, `{"firewall_group":{"ports":[]}}`)
		writeJSON(t, w, http.StatusCreated, `{"firewall_group":{"id":"`+fwGroupID+`"}}`)
	})
	if err := runFirewallGroupCreate(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatJSON},
		&fwGroupFlags{noPort: true}, &bytes.Buffer{}); err != nil {
		t.Fatalf("create: %v", err)
	}
}

// --port alone on set adds to the group's current ports; upstream reads them
// first.
func TestRunFirewallGroupSet_PortAddsToCurrentPorts(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	var put int
	fakeServer.Mux.HandleFunc("/fwaas/firewall_groups/"+fwGroupID, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(t, w, http.StatusOK, `{"firewall_group":{"id":"`+fwGroupID+`","ports":["`+fwPortC+`","`+fwPortA+`"]}}`)
		case http.MethodPut:
			put++
			th.TestJSONRequest(t, r, `{"firewall_group":{
			  "ingress_firewall_policy_id":null,"egress_firewall_policy_id":"`+fwPolicy2ID+`",
			  "name":"renamed","admin_state_up":true,"shared":false,
			  "ports":["`+fwPortA+`","`+fwPortB+`","`+fwPortC+`"]}}`)
			writeJSON(t, w, http.StatusOK, `{"firewall_group":{"id":"`+fwGroupID+`","name":"renamed"}}`)
		default:
			t.Errorf("unexpected %s", r.Method)
		}
	})
	f := &fwGroupFlags{
		name: "renamed", noIngress: true, egress: fwPolicy2ID, enable: true, noShare: true,
		ports: []string{fwPortB},
	}
	if err := runFirewallGroupSet(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatJSON}, fwGroupID, f, &bytes.Buffer{}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if put != 1 {
		t.Errorf("PUT count = %d, want 1", put)
	}
}

// --port with --no-port replaces the ports without reading the current ones.
func TestRunFirewallGroupSet_PortWithNoPortReplaces(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/fwaas/firewall_groups/"+fwGroupID, func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"firewall_group":{"ports":["`+fwPortB+`"]}}`)
		writeJSON(t, w, http.StatusOK, `{"firewall_group":{"id":"`+fwGroupID+`"}}`)
	})
	f := &fwGroupFlags{ports: []string{fwPortB}, noPort: true}
	if err := runFirewallGroupSet(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatJSON}, fwGroupID, f, &bytes.Buffer{}); err != nil {
		t.Fatalf("set: %v", err)
	}
}

func TestRunFirewallGroupSet_NothingToSetSendsNothing(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/", func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
	})
	err := runFirewallGroupSet(context.Background(), networkClient(fakeServer), &output.Options{}, fwGroupID, &fwGroupFlags{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "requires at least one attribute flag") {
		t.Errorf("err = %v", err)
	}
}

func TestRunFirewallGroupUnset_RemovesPortsAndClears(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/fwaas/firewall_groups/"+fwGroupID, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(t, w, http.StatusOK, `{"firewall_group":{"id":"`+fwGroupID+`","ports":["`+fwPortC+`","`+fwPortA+`","`+fwPortB+`"]}}`)
		case http.MethodPut:
			th.TestJSONRequest(t, r, `{"firewall_group":{
			  "ingress_firewall_policy_id":null,"egress_firewall_policy_id":null,
			  "shared":false,"admin_state_up":false,"ports":["`+fwPortA+`","`+fwPortC+`"]}}`)
			writeJSON(t, w, http.StatusOK, `{"firewall_group":{"id":"`+fwGroupID+`"}}`)
		}
	})
	f := &fwGroupUnsetFlags{ports: []string{fwPortB}, ingress: true, egress: true, share: true, enable: true}
	if err := runFirewallGroupUnset(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatJSON}, fwGroupID, f, &bytes.Buffer{}); err != nil {
		t.Fatalf("unset: %v", err)
	}
}

func TestRunFirewallGroupUnset_AllPort(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/fwaas/firewall_groups/"+fwGroupID, func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"firewall_group":{"ports":[]}}`)
		writeJSON(t, w, http.StatusOK, `{"firewall_group":{"id":"`+fwGroupID+`"}}`)
	})
	if err := runFirewallGroupUnset(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatJSON},
		fwGroupID, &fwGroupUnsetFlags{allPort: true}, &bytes.Buffer{}); err != nil {
		t.Fatalf("unset: %v", err)
	}
}

func TestRunFirewallGroupShowAndDelete(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	var deleted []string
	for _, id := range []string{fwGroupID, fwGroup2ID} {
		fakeServer.Mux.HandleFunc("/fwaas/firewall_groups/"+id, func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet:
				writeJSON(t, w, http.StatusOK, `{"firewall_group":{"id":"`+id+`","name":"g","admin_state_up":true}}`)
			case http.MethodDelete:
				deleted = append(deleted, id)
				w.WriteHeader(http.StatusNoContent)
			}
		})
	}
	client := networkClient(fakeServer)
	var buf bytes.Buffer
	if err := runFirewallGroupShow(context.Background(), client, &output.Options{Format: output.FormatJSON}, fwGroupID, &buf); err != nil {
		t.Fatalf("show: %v", err)
	}
	if m := fwJSON(t, &buf); m["State"] != "UP" || m["Name"] != "g" {
		t.Errorf("show = %v", m)
	}
	if err := runFirewallGroupDelete(context.Background(), client, []string{fwGroupID, fwGroup2ID}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !reflect.DeepEqual(deleted, []string{fwGroupID, fwGroup2ID}) {
		t.Errorf("deleted = %v", deleted)
	}
}

// A cloud without the fwaas_v2 plugin answers 404 on its whole URL tree; the
// error says the service is not deployed rather than reading as a missing
// resource.
func TestRunFirewallGroupList_NamesTheMissingPlugin(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/extensions", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, `{"extensions":[{"alias":"router"}]}`)
	})
	fakeServer.Mux.HandleFunc("/fwaas/firewall_groups", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusNotFound, `{"NeutronError":{"message":"not found"}}`)
	})
	err := runFirewallGroupList(context.Background(), networkClient(fakeServer), &output.Options{}, false, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "does not enable the fwaas_v2 extension") {
		t.Errorf("err = %v", err)
	}
}

// --- firewall group policy ----------------------------------------------------

func TestRunFirewallPolicyList_ColumnsMatchUpstream(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/fwaas/firewall_policies", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		writeJSON(t, w, http.StatusOK, `{"firewall_policies":[{"id":"`+fwPolicyID+`","name":"p1",
		  "firewall_rules":["`+fwRuleID+`"],"description":"d","audited":true,"shared":false,"project_id":"`+fwProjectID+`"}]}`)
	})
	client := networkClient(fakeServer)
	o := &output.Options{Format: output.FormatCSV}
	var buf bytes.Buffer
	if err := runFirewallPolicyList(context.Background(), client, o, false, &buf); err != nil {
		t.Fatalf("list: %v", err)
	}
	if got := fwHeader(&buf); got != "ID,Name,Firewall Rules" {
		t.Errorf("default header = %s", got)
	}
	buf.Reset()
	if err := runFirewallPolicyList(context.Background(), client, o, true, &buf); err != nil {
		t.Fatalf("list --long: %v", err)
	}
	if got := fwHeader(&buf); got != "ID,Name,Firewall Rules,Description,Audited,Shared,Project" {
		t.Errorf("long header = %s", got)
	}
	if !strings.Contains(buf.String(), fwRuleID) || !strings.Contains(buf.String(), fwProjectID) {
		t.Errorf("long output:\n%s", buf.String())
	}
}

func TestRunFirewallPolicyCreate_SendsUpstreamBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/fwaas/firewall_policies", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		th.TestJSONRequest(t, r, `{"firewall_policy":{"name":"p1","description":"d","audited":true,
		  "shared":false,"firewall_rules":["`+fwRule2ID+`","`+fwRuleID+`"],"project_id":"`+fwProjectID+`"}}`)
		writeJSON(t, w, http.StatusCreated, `{"firewall_policy":{"id":"`+fwPolicyID+`","name":"p1",
		  "firewall_rules":["`+fwRule2ID+`","`+fwRuleID+`"],"audited":true}}`)
	})
	f := &fwPolicyFlags{
		name: "p1", description: "d", audited: true, noShare: true,
		firewallRules: []string{fwRule2ID, fwRuleID}, projectID: fwProjectID,
	}
	var buf bytes.Buffer
	if err := runFirewallPolicyCreate(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatJSON}, f, &buf); err != nil {
		t.Fatalf("create: %v", err)
	}
	m := fwJSON(t, &buf)
	if len(m) != 7 || m["ID"] != fwPolicyID || m["Audited"] != true {
		t.Errorf("rendered policy = %v", m)
	}
}

// --firewall-rule alone on set appends to the policy's current rules.
func TestRunFirewallPolicySet_FirewallRuleAppends(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/fwaas/firewall_policies/"+fwPolicyID, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(t, w, http.StatusOK, `{"firewall_policy":{"id":"`+fwPolicyID+`","firewall_rules":["`+fwRuleID+`"]}}`)
		case http.MethodPut:
			th.TestJSONRequest(t, r, `{"firewall_policy":{"firewall_rules":["`+fwRuleID+`","`+fwRule2ID+`"],
			  "audited":false,"name":"n","shared":true}}`)
			writeJSON(t, w, http.StatusOK, `{"firewall_policy":{"id":"`+fwPolicyID+`"}}`)
		}
	})
	f := &fwPolicyFlags{firewallRules: []string{fwRule2ID}, noAudited: true, name: "n", share: true}
	if err := runFirewallPolicySet(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatJSON}, fwPolicyID, f, &bytes.Buffer{}); err != nil {
		t.Fatalf("set: %v", err)
	}
}

func TestRunFirewallPolicySet_NoFirewallRuleEmpties(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/fwaas/firewall_policies/"+fwPolicyID, func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"firewall_policy":{"firewall_rules":[]}}`)
		writeJSON(t, w, http.StatusOK, `{"firewall_policy":{"id":"`+fwPolicyID+`"}}`)
	})
	if err := runFirewallPolicySet(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatJSON},
		fwPolicyID, &fwPolicyFlags{noFirewallRule: true}, &bytes.Buffer{}); err != nil {
		t.Fatalf("set: %v", err)
	}
}

func TestRunFirewallPolicyUnset_RemovesRulesAndClears(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/fwaas/firewall_policies/"+fwPolicyID, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(t, w, http.StatusOK, `{"firewall_policy":{"id":"`+fwPolicyID+`",
			  "firewall_rules":["`+fwRule3ID+`","`+fwRuleID+`","`+fwRule2ID+`"]}}`)
		case http.MethodPut:
			// Order of the survivors is kept.
			th.TestJSONRequest(t, r, `{"firewall_policy":{"firewall_rules":["`+fwRule3ID+`","`+fwRule2ID+`"],
			  "audited":false,"shared":false}}`)
			writeJSON(t, w, http.StatusOK, `{"firewall_policy":{"id":"`+fwPolicyID+`"}}`)
		}
	})
	f := &fwPolicyUnsetFlags{firewallRules: []string{fwRuleID}, audited: true, share: true}
	if err := runFirewallPolicyUnset(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatJSON}, fwPolicyID, f, &bytes.Buffer{}); err != nil {
		t.Fatalf("unset: %v", err)
	}
}

func TestRunFirewallPolicyShowAndDelete(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	var deleted bool
	fakeServer.Mux.HandleFunc("/fwaas/firewall_policies/"+fwPolicyID, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(t, w, http.StatusOK, `{"firewall_policy":{"id":"`+fwPolicyID+`","name":"p","firewall_rules":["`+fwRuleID+`"]}}`)
		case http.MethodDelete:
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		}
	})
	client := networkClient(fakeServer)
	var buf bytes.Buffer
	if err := runFirewallPolicyShow(context.Background(), client, &output.Options{Format: output.FormatJSON}, fwPolicyID, &buf); err != nil {
		t.Fatalf("show: %v", err)
	}
	if m := fwJSON(t, &buf); m["Name"] != "p" || !reflect.DeepEqual(m["Firewall Rules"], []any{fwRuleID}) {
		t.Errorf("show = %v", m)
	}
	if err := runFirewallPolicyDelete(context.Background(), client, []string{fwPolicyID}); err != nil || !deleted {
		t.Fatalf("delete: %v (deleted %v)", err, deleted)
	}
}

// Upstream always sends all three keys, the positions as "" when not given.
func TestRunFirewallPolicyAddRule_SendsAllThreeKeys(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/fwaas/firewall_policies/"+fwPolicyID+"/insert_rule", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"firewall_rule_id":"`+fwRuleID+`","insert_before":"","insert_after":"`+fwRule2ID+`"}`)
		writeJSON(t, w, http.StatusOK, `{"id":"`+fwPolicyID+`","firewall_rules":["`+fwRule2ID+`","`+fwRuleID+`"]}`)
	})
	var buf bytes.Buffer
	if err := runFirewallPolicyAddRule(context.Background(), networkClient(fakeServer), fwPolicyID, fwRuleID, "", fwRule2ID, &buf); err != nil {
		t.Fatalf("add rule: %v", err)
	}
	if want := "Inserted firewall rule " + fwRuleID + " in firewall policy " + fwPolicyID + "\n"; buf.String() != want {
		t.Errorf("output = %q, want %q", buf.String(), want)
	}
}

func TestRunFirewallPolicyRemoveRule(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/fwaas/firewall_policies/"+fwPolicyID+"/remove_rule", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"firewall_rule_id":"`+fwRuleID+`"}`)
		writeJSON(t, w, http.StatusOK, `{"id":"`+fwPolicyID+`","firewall_rules":[]}`)
	})
	var buf bytes.Buffer
	if err := runFirewallPolicyRemoveRule(context.Background(), networkClient(fakeServer), fwPolicyID, fwRuleID, &buf); err != nil {
		t.Fatalf("remove rule: %v", err)
	}
	if want := "Removed firewall rule " + fwRuleID + " from firewall policy " + fwPolicyID + "\n"; buf.String() != want {
		t.Errorf("output = %q", buf.String())
	}
}

// --- firewall group rule ------------------------------------------------------

const fwRuleListBody = `{"firewall_rules":[
  {"id":"` + fwRuleID + `","name":"web","enabled":true,"protocol":"tcp","action":"allow",
   "source_ip_address":"192.0.2.0/24","destination_port":"80:90","ip_version":4,
   "firewall_policy_id":["` + fwPolicyID + `"],"shared":false,"project_id":"` + fwProjectID + `",
   "source_firewall_group_id":"` + fwGroupID + `","destination_firewall_group_id":null},
  {"id":"` + fwRule2ID + `","name":"any","enabled":false,"protocol":null,"action":"deny",
   "firewall_policy_id":[]}]}`

func TestRunFirewallRuleList_SummaryAndLongColumns(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/fwaas/firewall_rules", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		writeJSON(t, w, http.StatusOK, fwRuleListBody)
	})
	client := networkClient(fakeServer)
	var buf bytes.Buffer
	if err := runFirewallRuleList(context.Background(), client, &output.Options{Format: output.FormatJSON}, false, &buf); err != nil {
		t.Fatalf("list: %v", err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rows); err != nil {
		t.Fatalf("decoding %q: %v", buf.String(), err)
	}
	if len(rows) != 2 || len(rows[0]) != 5 {
		t.Fatalf("rows = %v", rows)
	}
	wantSummary := "TCP,\n source(port): 192.0.2.0/24(none specified),\n dest(port): none specified(80:90),\n allow"
	if rows[0]["Summary"] != wantSummary {
		t.Errorf("summary = %q, want %q", rows[0]["Summary"], wantSummary)
	}
	if want := "ANY,\n source(port): none specified(none specified),\n dest(port): none specified(none specified),\n deny"; rows[1]["Summary"] != want {
		t.Errorf("summary = %q", rows[1]["Summary"])
	}

	buf.Reset()
	if err := runFirewallRuleList(context.Background(), client, &output.Options{Format: output.FormatCSV}, true, &buf); err != nil {
		t.Fatalf("list --long: %v", err)
	}
	wantHeader := "ID,Name,Enabled,Description,Firewall Policy,IP Version,Action,Protocol,Source IP Address," +
		"Source Port,Destination IP Address,Destination Port,Shared,Project,Source Firewall Group ID,Destination Firewall Group ID"
	if got := fwHeader(&buf); got != wantHeader {
		t.Errorf("long header = %s\nwant          %s", got, wantHeader)
	}
	for _, want := range []string{fwGroupID, fwPolicyID, "80:90", ",any,"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("long output missing %q:\n%s", want, buf.String())
		}
	}
}

func TestRunFirewallRuleCreate_SendsUpstreamBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/fwaas/firewall_rules", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		th.TestJSONRequest(t, r, `{"firewall_rule":{
		  "name":"web","description":"d","protocol":"tcp","action":"allow","ip_version":6,
		  "source_port":"80:90","source_ip_address":"2001:db8::/32","destination_ip_address":null,
		  "destination_port":null,"enabled":false,"shared":true,
		  "source_firewall_group_id":"`+fwGroupID+`","destination_firewall_group_id":null,
		  "project_id":"`+fwProjectID+`"}}`)
		writeJSON(t, w, http.StatusCreated, `{"firewall_rule":{"id":"`+fwRuleID+`","name":"web","protocol":"tcp",
		  "action":"allow","ip_version":6,"source_firewall_group_id":"`+fwGroupID+`","firewall_policy_id":[]}}`)
	})
	f := &fwRuleFlags{
		name: "web", description: "d", protocol: "TCP", action: "ALLOW", ipVersion: "6",
		sourcePort: "80:90", sourceIP: "2001:db8::/32", noDestIP: true, noDestPort: true,
		disableRule: true, share: true, sourceGroup: fwGroupID, noDestGroup: true, projectID: fwProjectID,
	}
	var buf bytes.Buffer
	if err := runFirewallRuleCreate(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatJSON}, f, &buf); err != nil {
		t.Fatalf("create: %v", err)
	}
	m := fwJSON(t, &buf)
	if len(m) != 17 || m["ID"] != fwRuleID || m["Source Firewall Group ID"] != fwGroupID || m["IP Version"] != float64(6) {
		t.Errorf("rendered rule = %v", m)
	}
}

// "any" sends a null protocol, and a null protocol reads "any" back.
func TestRunFirewallRuleCreate_AnyProtocolIsNull(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/fwaas/firewall_rules", func(w http.ResponseWriter, r *http.Request) {
		th.TestJSONRequest(t, r, `{"firewall_rule":{"protocol":null}}`)
		writeJSON(t, w, http.StatusCreated, `{"firewall_rule":{"id":"`+fwRuleID+`","protocol":null}}`)
	})
	var buf bytes.Buffer
	if err := runFirewallRuleCreate(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatJSON},
		&fwRuleFlags{protocol: "Any"}, &buf); err != nil {
		t.Fatalf("create: %v", err)
	}
	if m := fwJSON(t, &buf); m["Protocol"] != "any" {
		t.Errorf("Protocol = %v", m["Protocol"])
	}
}

func TestRunFirewallRuleCreate_RejectsBadChoices(t *testing.T) {
	for _, f := range []*fwRuleFlags{{action: "drop"}, {ipVersion: "5"}} {
		err := runFirewallRuleCreate(context.Background(), nil, &output.Options{}, f, &bytes.Buffer{})
		if err == nil {
			t.Errorf("%+v: expected an error", f)
		}
	}
}

func TestRunFirewallRuleSet_ClearsAndRenames(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/fwaas/firewall_rules/"+fwRuleID, func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"firewall_rule":{"name":"n","source_port":null,"destination_port":"443",
		  "source_ip_address":null,"enabled":true,"destination_firewall_group_id":"`+fwGroup2ID+`",
		  "source_firewall_group_id":null}}`)
		writeJSON(t, w, http.StatusOK, `{"firewall_rule":{"id":"`+fwRuleID+`","name":"n"}}`)
	})
	f := &fwRuleFlags{
		name: "n", noSourcePort: true, destPort: "443", noSourceIP: true, enableRule: true,
		destGroup: fwGroup2ID, noSourceGroup: true,
	}
	if err := runFirewallRuleSet(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatJSON}, fwRuleID, f, &bytes.Buffer{}); err != nil {
		t.Fatalf("set: %v", err)
	}
}

func TestRunFirewallRuleUnset_ClearsEverything(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/fwaas/firewall_rules/"+fwRuleID, func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"firewall_rule":{"source_ip_address":null,"destination_ip_address":null,
		  "source_port":null,"destination_port":null,"shared":false,"enabled":false,
		  "source_firewall_group_id":null,"destination_firewall_group_id":null}}`)
		writeJSON(t, w, http.StatusOK, `{"firewall_rule":{"id":"`+fwRuleID+`"}}`)
	})
	f := &fwRuleUnsetFlags{
		sourceIP: true, destIP: true, sourcePort: true, destPort: true,
		share: true, enableRule: true, sourceGroup: true, destGroup: true,
	}
	if err := runFirewallRuleUnset(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatJSON}, fwRuleID, f, &bytes.Buffer{}); err != nil {
		t.Fatalf("unset: %v", err)
	}
}

func TestRunFirewallRuleShowAndDelete(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	var deleted bool
	fakeServer.Mux.HandleFunc("/fwaas/firewall_rules/"+fwRuleID, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(t, w, http.StatusOK, `{"firewall_rule":{"id":"`+fwRuleID+`","name":"r","protocol":"udp",
			  "action":"reject","destination_port":"53","firewall_policy_id":["`+fwPolicyID+`"]}}`)
		case http.MethodDelete:
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		}
	})
	client := networkClient(fakeServer)
	var buf bytes.Buffer
	if err := runFirewallRuleShow(context.Background(), client, &output.Options{Format: output.FormatJSON}, fwRuleID, &buf); err != nil {
		t.Fatalf("show: %v", err)
	}
	m := fwJSON(t, &buf)
	summary, _ := m["Summary"].(string)
	if m["Protocol"] != "udp" || m["Action"] != "reject" || !strings.HasPrefix(summary, "UDP,") {
		t.Errorf("show = %v", m)
	}
	if err := runFirewallRuleDelete(context.Background(), client, []string{fwRuleID}); err != nil || !deleted {
		t.Fatalf("delete: %v (deleted %v)", err, deleted)
	}
}

// --- cobra wiring -------------------------------------------------------------

// The nested paths are upstream's words, and a flag registered on the leaf
// reaches the request.
func TestExec_FirewallPolicyAddRule_InsertBeforeReachesTheBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/v2.0/fwaas/firewall_policies/"+fwPolicyID+"/insert_rule", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"firewall_rule_id":"`+fwRuleID+`","insert_before":"`+fwRule2ID+`","insert_after":""}`)
		writeJSON(t, w, http.StatusOK, `{"id":"`+fwPolicyID+`"}`)
	})
	out, err := execNetwork(t, fakeServer, "firewall", "group", "policy", "add", "rule", fwPolicyID, fwRuleID, "--insert-before", fwRule2ID)
	if err != nil {
		t.Fatalf("add rule: %v (%s)", err, out)
	}
	if !strings.Contains(out, "Inserted firewall rule "+fwRuleID) {
		t.Errorf("output = %q", out)
	}
}

func TestExec_FirewallGroupRuleCreate_FlagsReachTheBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/v2.0/fwaas/firewall_rules", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		th.TestJSONRequest(t, r, `{"firewall_rule":{"name":"r1","protocol":"icmp","ip_version":4,"source_ip_address":null}}`)
		writeJSON(t, w, http.StatusCreated, `{"firewall_rule":{"id":"`+fwRuleID+`","name":"r1"}}`)
	})
	out, err := execNetwork(t, fakeServer, "firewall", "group", "rule", "create", "r1",
		"--protocol", "ICMP", "--ip-version", "4", "--no-source-ip-address")
	if err != nil {
		t.Fatalf("rule create: %v (%s)", err, out)
	}
}

func TestExec_FirewallGroupCreate_DeprecatedNameAndExclusions(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/v2.0/fwaas/firewall_groups", func(w http.ResponseWriter, r *http.Request) {
		th.TestJSONRequest(t, r, `{"firewall_group":{"name":"old","ports":["`+fwPortA+`"]}}`)
		writeJSON(t, w, http.StatusCreated, `{"firewall_group":{"id":"`+fwGroupID+`","name":"old"}}`)
	})
	out, err := execNetwork(t, fakeServer, "firewall", "group", "create", "--name", "old", "--port", fwPortA)
	if err != nil {
		t.Fatalf("create --name: %v (%s)", err, out)
	}
	if !strings.Contains(out, "deprecated") {
		t.Errorf("no deprecation warning:\n%s", out)
	}
	if _, err := execNetwork(t, fakeServer, "firewall", "group", "create", "g", "--name", "old"); err == nil {
		t.Error("positional name with --name: expected an error")
	}
	if _, err := execNetwork(t, fakeServer, "firewall", "group", "create", "g", "--port", fwPortA, "--no-port"); err == nil {
		t.Error("--port with --no-port on create: expected an error")
	}
}

func TestExec_FirewallGroupUnset_AllPortReachesTheBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/v2.0/fwaas/firewall_groups/"+fwGroupID, func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"firewall_group":{"ports":[],"egress_firewall_policy_id":null}}`)
		writeJSON(t, w, http.StatusOK, `{"firewall_group":{"id":"`+fwGroupID+`"}}`)
	})
	if out, err := execNetwork(t, fakeServer, "firewall", "group", "unset", fwGroupID, "--all-port", "--egress-firewall-policy"); err != nil {
		t.Fatalf("unset: %v (%s)", err, out)
	}
}

// Names resolve through the fwaas collections before the call; a policy and
// rules given by name reach the insert as their IDs.
func TestRunFirewallPolicyAddRule_ResolvesNames(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/fwaas/firewall_policies", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("name"); got != "edge" {
			t.Errorf("policy lookup name = %q", got)
		}
		writeJSON(t, w, http.StatusOK, `{"firewall_policies":[{"id":"`+fwPolicyID+`","name":"edge"}]}`)
	})
	fakeServer.Mux.HandleFunc("/fwaas/firewall_rules", func(w http.ResponseWriter, r *http.Request) {
		id := map[string]string{"web": fwRuleID, "ssh": fwRule2ID}[r.URL.Query().Get("name")]
		writeJSON(t, w, http.StatusOK, `{"firewall_rules":[{"id":"`+id+`"}]}`)
	})
	fakeServer.Mux.HandleFunc("/fwaas/firewall_policies/"+fwPolicyID+"/insert_rule", func(w http.ResponseWriter, r *http.Request) {
		th.TestJSONRequest(t, r, `{"firewall_rule_id":"`+fwRuleID+`","insert_before":"`+fwRule2ID+`","insert_after":""}`)
		writeJSON(t, w, http.StatusOK, `{"id":"`+fwPolicyID+`"}`)
	})
	var buf bytes.Buffer
	if err := runFirewallPolicyAddRule(context.Background(), networkClient(fakeServer), "edge", "web", "ssh", "", &buf); err != nil {
		t.Fatalf("add rule: %v", err)
	}
	if want := "Inserted firewall rule " + fwRuleID + " in firewall policy edge\n"; buf.String() != want {
		t.Errorf("output = %q", buf.String())
	}
}

// A name that matches nothing errors rather than being sent as an ID.
func TestRunFirewallGroupShow_UnknownNameErrors(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/fwaas/firewall_groups", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, `{"firewall_groups":[]}`)
	})
	err := runFirewallGroupShow(context.Background(), networkClient(fakeServer), &output.Options{}, "typo", &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "typo") {
		t.Errorf("err = %v", err)
	}
}

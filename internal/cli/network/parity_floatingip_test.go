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

// "floating ip list" took no filter at all, so `--project` — the report that
// started the network parity pass — was rejected as an unknown flag.
func TestRunFloatingIPList_SendsEveryFilter(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	emptyLookup(t, fakeServer, "/networks", "networks")
	emptyLookup(t, fakeServer, "/ports", "ports")
	emptyLookup(t, fakeServer, "/routers", "routers")
	fakeServer.Mux.HandleFunc("/floatingips", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		q := r.URL.Query()
		want := map[string][]string{
			"floating_network_id": {"ext-1", "ext-2"},
			"port_id":             {"port-1"},
			"router_id":           {"rtr-1", "rtr-2"},
			"fixed_ip_address":    {"10.0.0.5"},
			"floating_ip_address": {"203.0.113.7"},
			"status":              {"DOWN"},
			"project_id":          {"p1"},
			"tags":                {"a,b"},
			"tags-any":            {"c"},
			"not-tags":            {"d"},
			"not-tags-any":        {"e,f"},
		}
		for k, v := range want {
			if !reflect.DeepEqual(q[k], v) {
				t.Errorf("query %s = %v, want %v", k, q[k], v)
			}
		}
		if len(q) != len(want) {
			t.Errorf("query has %d keys, want %d: %v", len(q), len(want), q)
		}
		writeJSON(t, w, http.StatusOK, `{"floatingips":[{
		  "id":"fip-1","floating_ip_address":"203.0.113.7","fixed_ip_address":"10.0.0.5",
		  "port_id":"port-1","floating_network_id":"ext-1","project_id":"p1",
		  "router_id":"rtr-1","status":"DOWN","description":"web","tags":["a","b"],
		  "dns_name":"www","dns_domain":"example.com."}]}`)
	})

	f := &floatingIPListFlags{
		networks:          []string{"ext-1", "ext-2"},
		ports:             []string{"port-1"},
		routers:           []string{"rtr-1", "rtr-2"},
		fixedIPAddress:    "10.0.0.5",
		floatingIPAddress: "203.0.113.7",
		status:            "DOWN",
		long:              true,
		tagFilterFlags: tagFilterFlags{
			tags: []string{"a", "b"}, anyTags: []string{"c"},
			notTags: []string{"d"}, notAnyTags: []string{"e", "f"},
		},
	}
	o := &output.Options{Format: output.FormatCSV}
	var buf bytes.Buffer
	if err := runFloatingIPList(context.Background(), networkClient(fakeServer), o, f, "p1", &buf); err != nil {
		t.Fatalf("runFloatingIPList: %v", err)
	}
	header := strings.SplitN(buf.String(), "\n", 2)[0]
	wantHeader := "ID,Floating IP Address,Fixed IP Address,Port,Floating Network,Project," +
		"Router,Status,Description,Tags,DNS Name,DNS Domain"
	if header != wantHeader {
		t.Errorf("header = %s\nwant     %s", header, wantHeader)
	}
	for _, want := range []string{"ext-1", "p1", "rtr-1", "www", "example.com."} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q:\n%s", want, buf.String())
		}
	}
}

func TestRunFloatingIPList_DefaultColumnsMatchUpstream(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/floatingips", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			t.Errorf("unfiltered list sent query %q", r.URL.RawQuery)
		}
		writeJSON(t, w, http.StatusOK, `{"floatingips":[]}`)
	})
	o := &output.Options{Format: output.FormatCSV}
	var buf bytes.Buffer
	if err := runFloatingIPList(context.Background(), networkClient(fakeServer), o, &floatingIPListFlags{}, "", &buf); err != nil {
		t.Fatalf("runFloatingIPList: %v", err)
	}
	if got := strings.TrimSpace(buf.String()); got != "ID,Floating IP Address,Fixed IP Address,Port,Floating Network,Project" {
		t.Errorf("default header = %s", got)
	}
}

func TestRunFloatingIPCreate_SendsExtensionAttributesThenTags(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	emptyLookup(t, fakeServer, "/networks", "networks")
	emptyLookup(t, fakeServer, "/qos/policies", "policies")
	fakeServer.Mux.HandleFunc("/floatingips", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		th.TestJSONRequest(t, r, `{"floatingip":{
		  "floating_network_id":"ext-net","project_id":"p1","description":"d",
		  "qos_policy_id":"qos-1","dns_domain":"example.com.","dns_name":"www","custom":7}}`)
		writeJSON(t, w, http.StatusCreated, `{"floatingip":{"id":"fip-1","floating_network_id":"ext-net",
		  "qos_policy_id":"qos-1","dns_name":"www","dns_domain":"example.com.","tags":[]}}`)
	})
	fakeServer.Mux.HandleFunc("/floatingips/fip-1/tags", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"tags":["blue","red"]}`)
		writeJSON(t, w, http.StatusOK, `{"tags":["blue","red"]}`)
	})

	f := &floatingIPCreateFlags{
		description: "d", qosPolicy: "qos-1", dnsDomain: "example.com.", dnsName: "www",
		projectID: "p1", extraProperty: []string{"type=int,name=custom,value=7"},
		tagWriteFlags: tagWriteFlags{tags: []string{"red", "blue"}},
	}
	o := &output.Options{Format: output.FormatJSON}
	var buf bytes.Buffer
	if err := runFloatingIPCreate(context.Background(), networkClient(fakeServer), o, "ext-net", f, &buf); err != nil {
		t.Fatalf("runFloatingIPCreate: %v", err)
	}
	for _, want := range []string{`"qos_policy_id": "qos-1"`, `"dns_name": "www"`, `"blue"`} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %s:\n%s", want, buf.String())
		}
	}
}

func TestRunFloatingIPSet_QoSAndDescription(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	emptyLookup(t, fakeServer, "/floatingips", "floatingips")
	fakeServer.Mux.HandleFunc("/floatingips/fip-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"floatingip":{"description":"","qos_policy_id":null}}`)
		writeJSON(t, w, http.StatusOK, `{"floatingip":{"id":"fip-1"}}`)
	})
	f := &floatingIPSetFlags{noQoSPolicy: true}
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	if err := runFloatingIPSet(context.Background(), networkClient(fakeServer), o, "fip-1", f, fakeFlags{flagDescription: true}, &buf); err != nil {
		t.Fatalf("runFloatingIPSet: %v", err)
	}
}

// Upstream skips the resource PUT when tags are the only change; the current
// tags come from a GET instead.
func TestRunFloatingIPSet_TagsOnlySkipsTheUpdate(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	emptyLookup(t, fakeServer, "/floatingips", "floatingips")
	fakeServer.Mux.HandleFunc("/floatingips/fip-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		writeJSON(t, w, http.StatusOK, `{"floatingip":{"id":"fip-1","tags":["old"]}}`)
	})
	fakeServer.Mux.HandleFunc("/floatingips/fip-1/tags", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"tags":["new","old"]}`)
		writeJSON(t, w, http.StatusOK, `{"tags":["new","old"]}`)
	})
	f := &floatingIPSetFlags{tagWriteFlags: tagWriteFlags{tags: []string{"new"}}}
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	if err := runFloatingIPSet(context.Background(), networkClient(fakeServer), o, "fip-1", f, fakeFlags{}, &buf); err != nil {
		t.Fatalf("runFloatingIPSet: %v", err)
	}
	if !strings.Contains(buf.String(), "new") {
		t.Errorf("output does not show the new tag set:\n%s", buf.String())
	}
}

func TestRunFloatingIPUnset_QoSAndTags(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	emptyLookup(t, fakeServer, "/floatingips", "floatingips")
	fakeServer.Mux.HandleFunc("/floatingips/fip-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"floatingip":{"qos_policy_id":null,"custom":null}}`)
		writeJSON(t, w, http.StatusOK, `{"floatingip":{"id":"fip-1","tags":["a","b"]}}`)
	})
	fakeServer.Mux.HandleFunc("/floatingips/fip-1/tags", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"tags":[]}`)
		writeJSON(t, w, http.StatusOK, `{"tags":[]}`)
	})
	f := &floatingIPUnsetFlags{
		qosPolicy: true, extraProperty: []string{"name=custom"},
		tagWriteFlags: tagWriteFlags{allTag: true},
	}
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	if err := runFloatingIPUnset(context.Background(), networkClient(fakeServer), o, "fip-1", f, &buf); err != nil {
		t.Fatalf("runFloatingIPUnset: %v", err)
	}
}

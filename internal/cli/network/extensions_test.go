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
	extNetworkID = "cccc3333-3333-3333-3333-cccccccccccc"
	extRouterID  = "dddd4444-4444-4444-4444-dddddddddddd"
	extPortID    = "eeee5555-5555-5555-5555-eeeeeeeeeeee"
	extFIPID     = "ffff6666-6666-6666-6666-ffffffffffff"
	extPFID      = "aaaa7777-7777-7777-7777-aaaaaaaaaaaa"
	extRBACID    = "bbbb8888-8888-8888-8888-bbbbbbbbbbbb"
	extSegmentID = "cccc9999-9999-9999-9999-cccccccccccc"
)

// handleNetworkLookup answers the name→ID list the neutron resolvers issue
// before every by-name operation.
func handleNetworkLookup(fakeServer th.FakeServer, name, id string) {
	fakeServer.Mux.HandleFunc("/networks", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"networks": [{"id": "` + id + `", "name": "` + name + `"}]}`))
	})
}

func TestRunIPAvailabilityShow_RendersPerSubnetBreakdown(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	handleNetworkLookup(fakeServer, "public", extNetworkID)
	fakeServer.Mux.HandleFunc("/network-ip-availabilities/"+extNetworkID, func(w http.ResponseWriter, r *http.Request) {
		th.AssertEquals(t, http.MethodGet, r.Method)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"network_ip_availability": {
		  "network_id": "` + extNetworkID + `", "network_name": "public",
		  "total_ips": 253, "used_ips": 7,
		  "subnet_ip_availability": [
		    {"subnet_id": "11112222-1111-2222-1111-222211112222", "subnet_name": "public-v4",
		     "cidr": "192.0.2.0/24", "ip_version": 4, "total_ips": 253, "used_ips": 7}
		  ]}}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	if err := runIPAvailabilityShow(context.Background(), networkClient(fakeServer), o, "public", &out); err != nil {
		t.Fatalf("runIPAvailabilityShow returned error: %v", err)
	}
	// The per-subnet rows are the reason to look at one network rather than the
	// list, so they must survive into the output.
	if !strings.Contains(out.String(), "192.0.2.0/24") || !strings.Contains(out.String(), "253") {
		t.Errorf("subnet breakdown missing from output:\n%s", out.String())
	}
}

func TestRunRBACCreate_SendsTheFullTriple(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var body map[string]any
	fakeServer.Mux.HandleFunc("/rbac-policies", func(w http.ResponseWriter, r *http.Request) {
		th.AssertEquals(t, http.MethodPost, r.Method)
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"rbac_policy": {"id": "` + extRBACID + `", "object_type": "network",
		  "object_id": "` + extNetworkID + `", "action": "access_as_shared",
		  "target_tenant": "99999999-9999-9999-9999-999999999999"}}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	err := runRBACCreate(context.Background(), networkClient(fakeServer), o, extNetworkID,
		"access_as_shared", "network", "99999999-9999-9999-9999-999999999999", &out)
	if err != nil {
		t.Fatalf("runRBACCreate returned error: %v", err)
	}
	policy := body["rbac_policy"].(map[string]any)
	th.AssertEquals(t, "access_as_shared", policy["action"])
	th.AssertEquals(t, "network", policy["object_type"])
	th.AssertEquals(t, extNetworkID, policy["object_id"])
	th.AssertEquals(t, "99999999-9999-9999-9999-999999999999", policy["target_tenant"])
}

func TestRunSegmentSet_OnlySendsChangedFields(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var body map[string]any
	fakeServer.Mux.HandleFunc("/segments/"+extSegmentID, func(w http.ResponseWriter, r *http.Request) {
		th.AssertEquals(t, http.MethodPut, r.Method)
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"segment": {"id": "` + extSegmentID + `", "name": "renamed",
		  "network_id": "` + extNetworkID + `", "network_type": "vlan", "segmentation_id": 101}}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	err := runSegmentSet(context.Background(), networkClient(fakeServer), o, extSegmentID,
		"renamed", "", 0, true, false, false, &out)
	if err != nil {
		t.Fatalf("runSegmentSet returned error: %v", err)
	}
	// UpdateOpts fields are pointers, so an unchanged flag must not appear at
	// all rather than blanking the stored value.
	seg := body["segment"].(map[string]any)
	th.AssertEquals(t, "renamed", seg["name"])
	if _, present := seg["description"]; present {
		t.Errorf("description sent although --description was not given: %#v", seg)
	}
	if _, present := seg["segmentation_id"]; present {
		t.Errorf("segmentation_id sent although --segment was not given: %#v", seg)
	}
}

func TestRunPortForwardingCreate_ResolvesPortAndOmitsRanges(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/ports", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ports": [{"id": "` + extPortID + `", "name": "vm-port"}]}`))
	})
	var body map[string]any
	fakeServer.Mux.HandleFunc("/floatingips/"+extFIPID+"/port_forwardings", func(w http.ResponseWriter, r *http.Request) {
		th.AssertEquals(t, http.MethodPost, r.Method)
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"port_forwarding": {"id": "` + extPFID + `", "protocol": "tcp",
		  "internal_port_id": "` + extPortID + `", "internal_ip_address": "192.0.2.10",
		  "internal_port": 22, "external_port": 2222}}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	f := &portForwardingFlags{
		port: "vm-port", internalIP: "192.0.2.10",
		internalPort: 22, externalPort: 2222, protocol: "tcp",
	}
	if err := runPortForwardingCreate(context.Background(), networkClient(fakeServer), o, extFIPID, f, &out); err != nil {
		t.Fatalf("runPortForwardingCreate returned error: %v", err)
	}
	pf := body["port_forwarding"].(map[string]any)
	th.AssertEquals(t, extPortID, pf["internal_port_id"])
	th.AssertEquals(t, float64(2222), pf["external_port"])
	// Neutron rejects a single port and a port range together, so the unused
	// range keys must be absent, not empty strings.
	for _, k := range []string{"internal_port_range", "external_port_range"} {
		if _, present := pf[k]; present {
			t.Errorf("%s sent although no range flag was given: %#v", k, pf)
		}
	}
}

func TestRunRouterRoute_UsesTheAtomicExtraRouteAction(t *testing.T) {
	for _, tc := range []struct {
		add  bool
		want string
	}{
		{true, "/routers/" + extRouterID + "/add_extraroutes"},
		{false, "/routers/" + extRouterID + "/remove_extraroutes"},
	} {
		fakeServer := th.SetupHTTP()

		fakeServer.Mux.HandleFunc("/routers", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"routers": [{"id": "` + extRouterID + `", "name": "r1"}]}`))
		})
		var gotPath string
		var body map[string]any
		handler := func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decoding request body: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"router": {"id": "` + extRouterID + `", "name": "r1",
			  "routes": [{"destination": "198.51.100.0/24", "nexthop": "192.0.2.1"}]}}`))
		}
		fakeServer.Mux.HandleFunc("/routers/"+extRouterID+"/add_extraroutes", handler)
		fakeServer.Mux.HandleFunc("/routers/"+extRouterID+"/remove_extraroutes", handler)

		var out bytes.Buffer
		o := &output.Options{Format: "value"}
		err := runRouterRoute(context.Background(), networkClient(fakeServer), o, "r1",
			[]string{"destination=198.51.100.0/24,gateway=192.0.2.1"}, tc.add, &out)
		if err != nil {
			t.Fatalf("runRouterRoute returned error: %v", err)
		}
		// The plain router update replaces the whole routes list; the atomic
		// action is what makes concurrent callers safe.
		th.AssertEquals(t, tc.want, gotPath)
		routes := body["router"].(map[string]any)["routes"].([]any)
		th.AssertEquals(t, "198.51.100.0/24", routes[0].(map[string]any)["destination"])
		th.AssertEquals(t, "192.0.2.1", routes[0].(map[string]any)["nexthop"])
		fakeServer.Teardown()
	}
}

func TestParseRouterRoutes_RejectsHalfSpecifiedRoutes(t *testing.T) {
	if _, err := parseRouterRoutes([]string{"destination=198.51.100.0/24"}); err == nil {
		t.Errorf("a route without a gateway was accepted")
	}
	if _, err := parseRouterRoutes([]string{"destination=198.51.100.0/24,via=192.0.2.1"}); err == nil {
		t.Errorf("an unknown route key was accepted")
	}
	// "nexthop" is neutron's own spelling and is accepted alongside OSC's
	// "gateway".
	routes, err := parseRouterRoutes([]string{"destination=198.51.100.0/24,nexthop=192.0.2.1"})
	if err != nil {
		t.Fatalf("parseRouterRoutes returned error: %v", err)
	}
	th.AssertEquals(t, "192.0.2.1", routes[0].NextHop)
}

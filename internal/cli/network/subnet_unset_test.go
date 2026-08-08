package network

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

const unsetSubnetID = "11111111-1111-1111-1111-111111111111"

// serveSubnetForUnset returns a subnet with two of everything, and captures the
// PUT body plus the If-Match header the update carries.
func serveSubnetForUnset(t *testing.T, fakeServer th.FakeServer) (*map[string]any, *string) {
	t.Helper()
	var body map[string]any
	var ifMatch string
	fakeServer.Mux.HandleFunc("/subnets", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"subnets": [{"id": "` + unsetSubnetID + `", "name": "private"}]}`))
	})
	fakeServer.Mux.HandleFunc("/subnets/"+unsetSubnetID, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			ifMatch = r.Header.Get("If-Match")
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decoding request body: %v", err)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"subnet": {
		  "id": "` + unsetSubnetID + `",
		  "name": "private",
		  "network_id": "22222222-2222-2222-2222-222222222222",
		  "cidr": "192.0.2.0/24",
		  "gateway_ip": "192.0.2.1",
		  "revision_number": 7,
		  "dns_nameservers": ["192.0.2.53", "198.51.100.53"],
		  "service_types": ["compute:nova", "network:router_gateway"],
		  "allocation_pools": [{"start": "192.0.2.10", "end": "192.0.2.20"},
		                       {"start": "192.0.2.30", "end": "192.0.2.40"}],
		  "host_routes": [{"destination": "10.0.0.0/8", "nexthop": "192.0.2.254"},
		                  {"destination": "172.16.0.0/12", "nexthop": "192.0.2.253"}]
		}}`))
	})
	return &body, &ifMatch
}

func TestRunSubnetUnset_RemovesOnlyTheNamedEntries(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	body, ifMatch := serveSubnetForUnset(t, fakeServer)

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	f := &subnetUnsetFlags{
		dnsNameserver:  []string{"192.0.2.53"},
		serviceType:    []string{"compute:nova"},
		allocationPool: []string{"start=192.0.2.10,end=192.0.2.20"},
		hostRoute:      []string{"destination=10.0.0.0/8,gateway=192.0.2.254"},
	}
	if err := runSubnetUnset(context.Background(), networkClient(fakeServer), o, unsetSubnetID, f, &out); err != nil {
		t.Fatalf("runSubnetUnset returned error: %v", err)
	}

	subnet := (*body)["subnet"].(map[string]any)

	// Each list is written back whole, carrying only the survivors — neutron
	// cannot remove a single entry.
	th.AssertDeepEquals(t, []any{"198.51.100.53"}, subnet["dns_nameservers"])
	th.AssertDeepEquals(t, []any{"network:router_gateway"}, subnet["service_types"])

	pools := subnet["allocation_pools"].([]any)
	th.AssertEquals(t, 1, len(pools))
	th.AssertEquals(t, "192.0.2.30", pools[0].(map[string]any)["start"])

	routes := subnet["host_routes"].([]any)
	th.AssertEquals(t, 1, len(routes))
	th.AssertEquals(t, "172.16.0.0/12", routes[0].(map[string]any)["destination"])

	// The update is pinned to the revision that was read, so a concurrent change
	// is rejected instead of being clobbered by a stale list.
	th.AssertEquals(t, "revision_number=7", *ifMatch)
}

func TestRunSubnetUnset_GatewayClearsWithEmptyString(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	body, _ := serveSubnetForUnset(t, fakeServer)

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	f := &subnetUnsetFlags{gateway: true}
	if err := runSubnetUnset(context.Background(), networkClient(fakeServer), o, unsetSubnetID, f, &out); err != nil {
		t.Fatalf("runSubnetUnset returned error: %v", err)
	}

	subnet := (*body)["subnet"].(map[string]any)
	// Neutron drops the gateway on an explicit null. koc sets the field to "",
	// which gophercloud's ToSubnetUpdateMap rewrites to null for exactly this
	// case; omitting the key would mean "leave it alone".
	got, present := subnet["gateway_ip"]
	if !present || got != nil {
		t.Errorf("gateway_ip must be sent as null, got %#v", subnet)
	}
	// Untouched lists must not appear at all.
	if _, present := subnet["dns_nameservers"]; present {
		t.Errorf("dns_nameservers sent although no --dns-nameserver was given: %#v", subnet)
	}
}

func TestParseHostRoutes_RejectsIncompleteSpecs(t *testing.T) {
	if _, err := parseHostRoutes([]string{"destination=10.0.0.0/8"}); err == nil {
		t.Error("expected an error when gateway= is missing")
	}
	if _, err := parseHostRoutes([]string{"destination=10.0.0.0/8,nexthop=192.0.2.1"}); err != nil {
		t.Errorf("nexthop= should be accepted as an alias of gateway=: %v", err)
	}
	if _, err := parseHostRoutes([]string{"dest=10.0.0.0/8,gateway=192.0.2.1"}); err == nil {
		t.Error("expected an error for an unknown key")
	}
}

func TestRunRouterAddGateway_SetsNetworkAndFixedIP(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const routerID = "33333333-3333-3333-3333-333333333333"
	const networkID = "44444444-4444-4444-4444-444444444444"
	const subnetID = "55555555-5555-5555-5555-555555555555"

	var body map[string]any
	serveRouterAndNetworkLookups(fakeServer, routerID, networkID, subnetID)
	fakeServer.Mux.HandleFunc("/routers/"+routerID, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decoding request body: %v", err)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"router": {"id": "` + routerID + `", "name": "edge"}}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	err := runRouterAddGateway(context.Background(), networkClient(fakeServer), o, routerID, networkID,
		[]string{"subnet=" + subnetID + ",ip-address=203.0.113.7"}, &out)
	if err != nil {
		t.Fatalf("runRouterAddGateway returned error: %v", err)
	}

	gateway := body["router"].(map[string]any)["external_gateway_info"].(map[string]any)
	th.AssertEquals(t, networkID, gateway["network_id"])
	fixed := gateway["external_fixed_ips"].([]any)[0].(map[string]any)
	th.AssertEquals(t, subnetID, fixed["subnet_id"])
	th.AssertEquals(t, "203.0.113.7", fixed["ip_address"])
}

func TestRunRouterRemoveGateway_SendsExplicitEmptyObject(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const routerID = "33333333-3333-3333-3333-333333333333"
	var body map[string]any
	serveRouterAndNetworkLookups(fakeServer, routerID, "", "")
	fakeServer.Mux.HandleFunc("/routers/"+routerID, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decoding request body: %v", err)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"router": {"id": "` + routerID + `", "name": "edge"}}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	if err := runRouterRemoveGateway(context.Background(), networkClient(fakeServer), o, routerID, &out); err != nil {
		t.Fatalf("runRouterRemoveGateway returned error: %v", err)
	}

	router := body["router"].(map[string]any)
	// gophercloud tags GatewayInfo omitempty, so a zero-valued struct would be
	// dropped and the request would become a no-op. The key has to be present.
	gateway, present := router["external_gateway_info"]
	if !present {
		t.Fatalf("external_gateway_info missing; the removal would be a no-op: %#v", router)
	}
	if m, ok := gateway.(map[string]any); !ok || len(m) != 0 {
		t.Errorf("external_gateway_info must be an empty object, got %#v", gateway)
	}
}

// serveRouterAndNetworkLookups answers the name lookups the network resolvers
// perform before any write; they list by name rather than passing a UUID
// straight through.
func serveRouterAndNetworkLookups(fakeServer th.FakeServer, routerID, networkID, subnetID string) {
	fakeServer.Mux.HandleFunc("/routers", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"routers": [{"id": "` + routerID + `", "name": "edge"}]}`))
	})
	if networkID != "" {
		fakeServer.Mux.HandleFunc("/networks", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"networks": [{"id": "` + networkID + `", "name": "public"}]}`))
		})
	}
	if subnetID != "" {
		fakeServer.Mux.HandleFunc("/subnets", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"subnets": [{"id": "` + subnetID + `", "name": "public-sub"}]}`))
		})
	}
}

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

const (
	paritySubnetID        = "11111111-1111-1111-1111-111111111111"
	paritySegmentID       = "33333333-3333-3333-3333-333333333333"
	subnetParityProjectID = "44444444-4444-4444-4444-444444444444"
)

// paritySubnetBody is a subnet carrying one of every list attribute, so a set
// that appends and an unset that removes both have something to work against.
const paritySubnetBody = `{"subnet":{
  "id":"` + paritySubnetID + `","name":"sn","network_id":"net-1","cidr":"192.0.2.0/24",
  "ip_version":4,"revision_number":3,"tags":["old"],
  "dns_nameservers":["192.0.2.53"],
  "allocation_pools":[{"start":"192.0.2.10","end":"192.0.2.20"}],
  "host_routes":[{"destination":"10.0.0.0/8","nexthop":"192.0.2.254"}],
  "service_types":["compute:nova"]}}`

func TestRunSubnetList_ParityFiltersAndLongColumns(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/subnets", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		q := r.URL.Query()
		want := map[string][]string{
			"cidr":          {"10.10.0.0/16"},
			"service_types": {"network:floatingip_agent_gateway", "compute:nova"},
			"tags":          {"a,b"},
			"tags-any":      {"c"},
			"not-tags":      {"d"},
			"not-tags-any":  {"e"},
		}
		if !reflect.DeepEqual(map[string][]string(q), want) {
			t.Errorf("query = %v\nwant    %v", q, want)
		}
		writeJSON(t, w, http.StatusOK, `{"subnets":[{"id":"sub-1","name":"sn","network_id":"net-1",
		  "cidr":"10.10.0.0/24","ip_version":4,"enable_dhcp":true,"gateway_ip":"10.10.0.1",
		  "dns_nameservers":["192.0.2.53"],"tags":["a","b"],"service_types":["compute:nova"],
		  "host_routes":[{"destination":"10.0.0.0/8","nexthop":"10.10.0.254"}]}]}`)
	})
	f := &subnetListFlags{
		subnetRange:  "10.10.0.0/16",
		serviceTypes: []string{"network:floatingip_agent_gateway", "compute:nova"},
		long:         true,
		tagFilterFlags: tagFilterFlags{
			tags: []string{"a", "b"}, anyTags: []string{"c"}, notTags: []string{"d"}, notAnyTags: []string{"e"},
		},
	}
	o := &output.Options{Format: output.FormatCSV}
	var buf bytes.Buffer
	if err := runSubnetList(context.Background(), networkClient(fakeServer), o, f, "", &buf); err != nil {
		t.Fatalf("runSubnetList: %v", err)
	}
	header := strings.SplitN(buf.String(), "\n", 2)[0]
	wantHeader := "ID,Name,Network,Subnet,Project,DHCP,Name Servers,Allocation Pools,Host Routes," +
		"IP Version,Gateway,Service Types,Tags,Subnet Pool"
	if header != wantHeader {
		t.Errorf("header = %s\nwant     %s", header, wantHeader)
	}
	if !strings.Contains(buf.String(), "destination=10.0.0.0/8,gateway=10.10.0.254") {
		t.Errorf("host routes not rendered in --host-route spelling:\n%s", buf.String())
	}
}

func TestRunSubnetCreate_SendsEveryParityAttributeThenTags(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	echoLookup(t, fakeServer, "/networks", "networks")
	echoLookup(t, fakeServer, "/subnetpools", "subnetpools")
	fakeServer.Mux.HandleFunc("/subnets", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		th.TestJSONRequest(t, r, `{"subnet":{
		  "name":"sn","network_id":"net-1","ip_version":6,"gateway_ip":null,
		  "subnetpool_id":"pool-1","prefixlen":64,"segment_id":"`+paritySegmentID+`",
		  "description":"d","project_id":"`+subnetParityProjectID+`",
		  "ipv6_ra_mode":"slaac","ipv6_address_mode":"dhcpv6-stateless",
		  "dns_publish_fixed_ip":true,"enable_dhcp":false,
		  "service_types":["compute:nova"],
		  "host_routes":[{"destination":"2001:db8:1::/48","nexthop":"2001:db8::1"}],
		  "custom":"x"}}`)
		writeJSON(t, w, http.StatusCreated, `{"subnet":{"id":"sub-1","name":"sn","tags":[]}}`)
	})
	fakeServer.Mux.HandleFunc("/subnets/sub-1/tags", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"tags":["blue","red"]}`)
		writeJSON(t, w, http.StatusOK, `{"tags":["blue","red"]}`)
	})
	f := &subnetCreateFlags{
		network: "net-1", ipVersion: 6, gateway: "None", subnetPool: "pool-1", prefixLength: 64,
		networkSegment: paritySegmentID, description: "d", projectID: subnetParityProjectID,
		ipv6RAMode: "slaac", ipv6AddressMode: "dhcpv6-stateless", dnsPublishFixedIP: true, noDHCP: true,
		serviceType:   []string{"compute:nova"},
		hostRoute:     []string{"destination=2001:db8:1::/48,gateway=2001:db8::1"},
		extraProperty: []string{"name=custom,value=x"},
		tagWriteFlags: tagWriteFlags{tags: []string{"red", "blue"}},
	}
	o := &output.Options{Format: output.FormatJSON}
	var buf bytes.Buffer
	if err := runSubnetCreate(context.Background(), networkClient(fakeServer), o, "sn", f, &buf); err != nil {
		t.Fatalf("runSubnetCreate: %v", err)
	}
	if !strings.Contains(buf.String(), `"blue"`) {
		t.Errorf("output does not show the tags just set:\n%s", buf.String())
	}
}

// The two pool-less allocation modes: use_default_subnetpool is not modelled
// by CreateOpts and rides the body adapter; prefix delegation is neutron's
// reserved subnetpool_id.
func TestRunSubnetCreate_DefaultPoolAndPrefixDelegation(t *testing.T) {
	for name, tc := range map[string]struct {
		flags subnetCreateFlags
		body  string
	}{
		"use-default-subnet-pool": {
			subnetCreateFlags{network: "net-1", ipVersion: 4, useDefaultSubnetPool: true, prefixLength: 26},
			`{"subnet":{"name":"sn","network_id":"net-1","ip_version":4,"prefixlen":26,"use_default_subnetpool":true}}`,
		},
		"use-prefix-delegation": {
			subnetCreateFlags{network: "net-1", ipVersion: 6, usePrefixDelegation: true, gateway: "auto"},
			`{"subnet":{"name":"sn","network_id":"net-1","ip_version":6,"subnetpool_id":"prefix_delegation"}}`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()
			echoLookup(t, fakeServer, "/networks", "networks")
			fakeServer.Mux.HandleFunc("/subnets", func(w http.ResponseWriter, r *http.Request) {
				th.TestJSONRequest(t, r, tc.body)
				writeJSON(t, w, http.StatusCreated, `{"subnet":{"id":"sub-1"}}`)
			})
			o := &output.Options{Format: output.FormatValue}
			var buf bytes.Buffer
			if err := runSubnetCreate(context.Background(), networkClient(fakeServer), o, "sn", &tc.flags, &buf); err != nil {
				t.Fatalf("runSubnetCreate: %v", err)
			}
		})
	}
}

func TestRunSubnetCreate_RejectsUnknownIPv6Mode(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	f := &subnetCreateFlags{network: "net-1", ipVersion: 6, ipv6RAMode: "stateful"}
	err := runSubnetCreate(context.Background(), networkClient(fakeServer), &output.Options{}, "sn", f, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "--ipv6-ra-mode") {
		t.Fatalf("want an --ipv6-ra-mode error, got %v", err)
	}
}

// Upstream appends --dns-nameserver/--allocation-pool/--host-route/
// --service-type to what the subnet already has, new entries first. koc used
// to replace the nameservers; the update is now pinned to the revision read.
func TestRunSubnetSet_AppendsListsToTheCurrentValues(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	var puts int
	fakeServer.Mux.HandleFunc("/subnets/"+paritySubnetID, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeJSON(t, w, http.StatusOK, paritySubnetBody)
			return
		}
		puts++
		th.TestMethod(t, r, http.MethodPut)
		th.TestHeader(t, r, "If-Match", "revision_number=3")
		th.TestJSONRequest(t, r, `{"subnet":{
		  "dns_nameservers":["198.51.100.53","192.0.2.53"],
		  "allocation_pools":[{"start":"192.0.2.30","end":"192.0.2.40"},{"start":"192.0.2.10","end":"192.0.2.20"}],
		  "host_routes":[{"destination":"172.16.0.0/12","nexthop":"192.0.2.253"},{"destination":"10.0.0.0/8","nexthop":"192.0.2.254"}],
		  "service_types":["network:router_gateway","compute:nova"]}}`)
		writeJSON(t, w, http.StatusOK, paritySubnetBody)
	})
	echoLookup(t, fakeServer, "/subnets", "subnets")
	f := &subnetSetFlags{
		dnsNameservers: []string{"198.51.100.53"},
		allocationPool: []string{"start=192.0.2.30,end=192.0.2.40"},
		hostRoute:      []string{"destination=172.16.0.0/12,gateway=192.0.2.253"},
		serviceType:    []string{"network:router_gateway"},
	}
	var buf bytes.Buffer
	if err := runSubnetSet(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatValue},
		paritySubnetID, f, fakeFlags{}, &buf); err != nil {
		t.Fatalf("runSubnetSet: %v", err)
	}
	if puts != 1 {
		t.Errorf("PUTs = %d, want 1", puts)
	}
}

// --no-* alone clears the list (no read needed); with the matching list flag it
// overwrites. The emptied allocation pools must still be sent, although
// UpdateOpts drops an empty slice. The scalars ride the same PUT.
func TestRunSubnetSet_ClearsAndOverwritesListsAndSetsScalars(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/subnets/"+paritySubnetID, func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		if got := r.Header.Get("If-Match"); got != "" {
			t.Errorf("If-Match = %q, want none: nothing was read", got)
		}
		th.TestJSONRequest(t, r, `{"subnet":{
		  "dns_nameservers":["198.51.100.53"],"allocation_pools":[],"host_routes":[],
		  "gateway_ip":null,"description":"d","dns_publish_fixed_ip":false,
		  "segment_id":"`+paritySegmentID+`","custom":7}}`)
		writeJSON(t, w, http.StatusOK, paritySubnetBody)
	})
	echoLookup(t, fakeServer, "/subnets", "subnets")
	f := &subnetSetFlags{
		dnsNameservers: []string{"198.51.100.53"}, noDNSNameservers: true,
		noAllocationPool: true, noHostRoute: true,
		gateway: "NONE", description: "d", noDNSPublishFixedIP: true, networkSegment: paritySegmentID,
		extraProperty: []string{"type=int,name=custom,value=7"},
	}
	var buf bytes.Buffer
	if err := runSubnetSet(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatValue},
		paritySubnetID, f, fakeFlags{flagDescription: true}, &buf); err != nil {
		t.Fatalf("runSubnetSet: %v", err)
	}
}

func TestRunSubnetSet_GatewayAutoIsRejected(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	echoLookup(t, fakeServer, "/subnets", "subnets")
	err := runSubnetSet(context.Background(), networkClient(fakeServer), &output.Options{},
		paritySubnetID, &subnetSetFlags{gateway: "auto"}, fakeFlags{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "auto") {
		t.Fatalf("want a --gateway auto error, got %v", err)
	}
}

// Upstream skips the subnet PUT when tags are the only change.
func TestRunSubnetSet_TagsOnlySkipsTheUpdate(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	echoLookup(t, fakeServer, "/subnets", "subnets")
	fakeServer.Mux.HandleFunc("/subnets/"+paritySubnetID, func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		writeJSON(t, w, http.StatusOK, paritySubnetBody)
	})
	fakeServer.Mux.HandleFunc("/subnets/"+paritySubnetID+"/tags", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"tags":["new"]}`)
		writeJSON(t, w, http.StatusOK, `{"tags":["new"]}`)
	})
	f := &subnetSetFlags{tagWriteFlags: tagWriteFlags{tags: []string{"new"}, noTag: true}}
	var buf bytes.Buffer
	if err := runSubnetSet(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatValue},
		paritySubnetID, f, fakeFlags{}, &buf); err != nil {
		t.Fatalf("runSubnetSet: %v", err)
	}
	if !strings.Contains(buf.String(), "new") {
		t.Errorf("output does not show the new tag set:\n%s", buf.String())
	}
}

// --extra-property on unset clears the named attribute (null); removing the
// last allocation pool sends [] rather than omitting the key; --all-tag clears
// the tags after the PUT.
func TestRunSubnetUnset_ExtraPropertyLastPoolAndTags(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	echoLookup(t, fakeServer, "/subnets", "subnets")
	fakeServer.Mux.HandleFunc("/subnets/"+paritySubnetID, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeJSON(t, w, http.StatusOK, paritySubnetBody)
			return
		}
		th.TestMethod(t, r, http.MethodPut)
		th.TestHeader(t, r, "If-Match", "revision_number=3")
		th.TestJSONRequest(t, r, `{"subnet":{"allocation_pools":[],"custom":null}}`)
		writeJSON(t, w, http.StatusOK, paritySubnetBody)
	})
	fakeServer.Mux.HandleFunc("/subnets/"+paritySubnetID+"/tags", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"tags":[]}`)
		writeJSON(t, w, http.StatusOK, `{"tags":[]}`)
	})
	f := &subnetUnsetFlags{
		allocationPool: []string{"start=192.0.2.10,end=192.0.2.20"},
		extraProperty:  []string{"name=custom"},
		tagWriteFlags:  tagWriteFlags{allTag: true},
	}
	if err := runSubnetUnset(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatValue},
		paritySubnetID, f, &bytes.Buffer{}); err != nil {
		t.Fatalf("runSubnetUnset: %v", err)
	}
}

func TestRunSubnetUnset_TagsOnlySendsNoSubnetPut(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	echoLookup(t, fakeServer, "/subnets", "subnets")
	fakeServer.Mux.HandleFunc("/subnets/"+paritySubnetID, func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		writeJSON(t, w, http.StatusOK, paritySubnetBody)
	})
	fakeServer.Mux.HandleFunc("/subnets/"+paritySubnetID+"/tags", func(w http.ResponseWriter, r *http.Request) {
		th.TestJSONRequest(t, r, `{"tags":[]}`)
		writeJSON(t, w, http.StatusOK, `{"tags":[]}`)
	})
	f := &subnetUnsetFlags{tagWriteFlags: tagWriteFlags{tags: []string{"old"}}}
	if err := runSubnetUnset(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatValue},
		paritySubnetID, f, &bytes.Buffer{}); err != nil {
		t.Fatalf("runSubnetUnset: %v", err)
	}
}

// As upstream, removing an entry the subnet does not carry is an error.
func TestRunSubnetUnset_MissingEntryIsAnError(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	echoLookup(t, fakeServer, "/subnets", "subnets")
	fakeServer.Mux.HandleFunc("/subnets/"+paritySubnetID, func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		writeJSON(t, w, http.StatusOK, paritySubnetBody)
	})
	f := &subnetUnsetFlags{dnsNameserver: []string{"203.0.113.53"}}
	err := runSubnetUnset(context.Background(), networkClient(fakeServer), &output.Options{}, paritySubnetID, f, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "does not contain dns-nameserver 203.0.113.53") {
		t.Fatalf("want a missing-entry error, got %v", err)
	}
}

// --- subnet pool ------------------------------------------------------------

// poolLens are the prefix-length fields neutron always returns and
// gophercloud's SubnetPool decoder insists on.
const poolLens = `"default_prefixlen":"24","min_prefixlen":"8","max_prefixlen":"32",`

func TestRunSubnetPoolList_TagFiltersAddressScopeAndLongColumns(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	echoLookup(t, fakeServer, "/address-scopes", "address_scopes")
	fakeServer.Mux.HandleFunc("/subnetpools", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		want := map[string][]string{
			"address_scope_id": {"scope-a"}, "project_id": {"p1"},
			"tags": {"a"}, "not-tags-any": {"b,c"},
		}
		if !reflect.DeepEqual(map[string][]string(q), want) {
			t.Errorf("query = %v\nwant    %v", q, want)
		}
		writeJSON(t, w, http.StatusOK, subnetPoolListBody)
	})
	f := &subnetPoolListFlags{
		addressScope: "scope-a", long: true,
		tagFilterFlags: tagFilterFlags{tags: []string{"a"}, notAnyTags: []string{"b", "c"}},
	}
	var buf bytes.Buffer
	if err := runSubnetPoolList(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatCSV}, f, "p1", &buf); err != nil {
		t.Fatalf("runSubnetPoolList: %v", err)
	}
	header := strings.SplitN(buf.String(), "\n", 2)[0]
	if want := "ID,Name,Prefixes,Default Prefix Length,Address Scope,Default Subnet Pool,Shared,Tags,Project"; header != want {
		t.Errorf("header = %s\nwant     %s", header, want)
	}
}

func TestRunSubnetPoolCreate_ExtraPropertyAddressScopeAndTags(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	echoLookup(t, fakeServer, "/address-scopes", "address_scopes")
	fakeServer.Mux.HandleFunc("/subnetpools", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		th.TestJSONRequest(t, r, `{"subnetpool":{"name":"pool","prefixes":["198.51.100.0/24"],
		  "address_scope_id":"scope-a","project_id":"p1","custom":true}}`)
		writeJSON(t, w, http.StatusCreated, `{"subnetpool":{`+poolLens+`"id":"sp1","name":"pool","tags":[]}}`)
	})
	fakeServer.Mux.HandleFunc("/subnetpools/sp1/tags", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"tags":["x"]}`)
		writeJSON(t, w, http.StatusOK, `{"tags":["x"]}`)
	})
	f := &subnetPoolWriteFlags{
		prefixes: []string{"198.51.100.0/24"}, addressScope: "scope-a",
		extraProperty: []string{"type=bool,name=custom,value=True"},
		tagWriteFlags: tagWriteFlags{tags: []string{"x"}},
	}
	if err := runSubnetPoolCreate(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatValue},
		"pool", f, "p1", &bytes.Buffer{}); err != nil {
		t.Fatalf("runSubnetPoolCreate: %v", err)
	}
}

// --pool-prefix appends to the pool's prefixes (upstream extends them);
// --no-address-scope sends an explicit null.
func TestRunSubnetPoolSet_AppendsPrefixesAndClearsAddressScope(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	echoLookup(t, fakeServer, "/subnetpools", "subnetpools")
	fakeServer.Mux.HandleFunc("/subnetpools/sp1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeJSON(t, w, http.StatusOK, `{"subnetpool":{`+poolLens+`"id":"sp1","prefixes":["10.0.0.0/8"]}}`)
			return
		}
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"subnetpool":{"prefixes":["172.16.0.0/12","10.0.0.0/8"],"address_scope_id":null}}`)
		writeJSON(t, w, http.StatusOK, `{"subnetpool":{`+poolLens+`"id":"sp1"}}`)
	})
	f := &subnetPoolWriteFlags{prefixes: []string{"172.16.0.0/12"}, noAddressScope: true}
	if err := runSubnetPoolSet(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatValue},
		"sp1", f, changedSet{"pool-prefix": true}, &bytes.Buffer{}); err != nil {
		t.Fatalf("runSubnetPoolSet: %v", err)
	}
}

func TestRunSubnetPoolSet_TagsOnlySkipsTheUpdate(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	echoLookup(t, fakeServer, "/subnetpools", "subnetpools")
	fakeServer.Mux.HandleFunc("/subnetpools/sp1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		writeJSON(t, w, http.StatusOK, `{"subnetpool":{`+poolLens+`"id":"sp1","tags":["a"]}}`)
	})
	fakeServer.Mux.HandleFunc("/subnetpools/sp1/tags", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"tags":["a","b"]}`)
		writeJSON(t, w, http.StatusOK, `{"tags":["a","b"]}`)
	})
	f := &subnetPoolWriteFlags{tagWriteFlags: tagWriteFlags{tags: []string{"b"}}}
	if err := runSubnetPoolSet(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatValue},
		"sp1", f, changedSet{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("runSubnetPoolSet: %v", err)
	}
}

func TestRunSubnetPoolUnset_RemovesTags(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	echoLookup(t, fakeServer, "/subnetpools", "subnetpools")
	fakeServer.Mux.HandleFunc("/subnetpools/sp1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		writeJSON(t, w, http.StatusOK, `{"subnetpool":{`+poolLens+`"id":"sp1","tags":["a","b","c"]}}`)
	})
	fakeServer.Mux.HandleFunc("/subnetpools/sp1/tags", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"tags":["b"]}`)
		writeJSON(t, w, http.StatusOK, `{"tags":["b"]}`)
	})
	var buf bytes.Buffer
	f := &tagWriteFlags{tags: []string{"a", "c"}}
	if err := runSubnetPoolUnset(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatJSON},
		"sp1", f, &buf); err != nil {
		t.Fatalf("runSubnetPoolUnset: %v", err)
	}
	var shown map[string]any
	if err := json.Unmarshal(buf.Bytes(), &shown); err != nil {
		t.Fatalf("decoding output: %v\n%s", err, buf.String())
	}
	if !reflect.DeepEqual(shown["tags"], []any{"b"}) {
		t.Errorf("tags shown = %v, want [b]", shown["tags"])
	}
	if err := runSubnetPoolUnset(context.Background(), networkClient(fakeServer), &output.Options{}, "sp1", &tagWriteFlags{}, &buf); err == nil {
		t.Error("unset with no flag succeeded; want an error")
	}
}

// --- through cobra: the flags wired in RunE ----------------------------------

// --project is resolved in RunE (a UUID passes through without keystone) and
// --use-default-subnet-pool / --tag must reach the seam.
func TestExec_SubnetCreate_ProjectDefaultPoolAndTag(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	echoLookup(t, fakeServer, "/v2.0/networks", "networks")
	fakeServer.Mux.HandleFunc("/v2.0/subnets", func(w http.ResponseWriter, r *http.Request) {
		th.TestJSONRequest(t, r, `{"subnet":{"name":"sn","network_id":"net-1","ip_version":4,
		  "project_id":"`+subnetParityProjectID+`","use_default_subnetpool":true}}`)
		writeJSON(t, w, http.StatusCreated, `{"subnet":{"id":"sub-1","tags":[]}}`)
	})
	var tagged bool
	fakeServer.Mux.HandleFunc("/v2.0/subnets/sub-1/tags", func(w http.ResponseWriter, _ *http.Request) {
		tagged = true
		writeJSON(t, w, http.StatusOK, `{"tags":["a"]}`)
	})
	out, err := execNetwork(t, fakeServer, "subnet", "create", "sn", "--network", "net-1",
		"--use-default-subnet-pool", "--project", subnetParityProjectID, "--tag", "a")
	if err != nil {
		t.Fatalf("subnet create: %v\n%s", err, out)
	}
	if !tagged {
		t.Error("--tag did not reach the tags sub-resource")
	}
}

func TestExec_SubnetCreate_PoolFlagsAreMutuallyExclusive(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	_, err := execNetwork(t, fakeServer, "subnet", "create", "sn", "--network", "net-1",
		"--subnet-pool", "p", "--use-prefix-delegation")
	if err == nil {
		t.Fatal("--subnet-pool with --use-prefix-delegation was accepted")
	}
}

func TestExec_SubnetPoolUnset_IsWired(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	echoLookup(t, fakeServer, "/v2.0/subnetpools", "subnetpools")
	fakeServer.Mux.HandleFunc("/v2.0/subnetpools/sp1", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, `{"subnetpool":{`+poolLens+`"id":"sp1","tags":["a"]}}`)
	})
	var body string
	fakeServer.Mux.HandleFunc("/v2.0/subnetpools/sp1/tags", func(w http.ResponseWriter, r *http.Request) {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r.Body)
		body = buf.String()
		writeJSON(t, w, http.StatusOK, `{"tags":[]}`)
	})
	if out, err := execNetwork(t, fakeServer, "subnet", "pool", "unset", "sp1", "--all-tag"); err != nil {
		t.Fatalf("subnet pool unset: %v\n%s", err, out)
	}
	if !strings.Contains(body, `"tags":[]`) {
		t.Errorf("tags body = %q, want an empty tag set", body)
	}
	if _, err := execNetwork(t, fakeServer, "subnet", "pool", "unset", "sp1", "--all-tag", "--tag", "a"); err == nil {
		t.Error("--all-tag with --tag was accepted")
	}
}

package network_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/layer3/addressscopes"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/layer3/portforwarding"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/layer3/routers"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/networkipavailabilities"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/qos/policies"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/rbacpolicies"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/security/addressgroups"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/segments"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/subnetpools"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/trunks"

	"github.com/ftarasenko/koc/pkg/openstack"
	"github.com/ftarasenko/koc/pkg/openstack/network"
)

const (
	qosID   = "5f6a7b8c-9d0e-4f1a-8b2c-4d5e6f7a8b9c"
	ruleID  = "6a7b8c9d-0e1f-4a2b-9c3d-5e6f7a8b9c0d"
	trunkID = "7b8c9d0e-1f2a-4b3c-8d4e-6f7a8b9c0d1e"
)

func TestQoS(t *testing.T) {
	p := `{"id":"` + qosID + `","name":"gold","shared":true,"is_default":false,` +
		`"rules":[{"id":"` + ruleID + `","type":"minimum_packet_rate","min_kpps":1000,"direction":"any"}]}`
	rule := `{"id":"` + ruleID + `","min_kpps":1000,"direction":"any"}`
	rulePath := "/qos/policies/" + qosID + "/minimum_packet_rate_rules"
	seen, sc := fakeNeutron(t, map[string]reply{
		"GET /qos/policies":                      {Body: `{"policies":[` + p + `]}`},
		"GET /qos/policies/" + qosID:             {Body: `{"policy":` + p + `}`},
		"POST /qos/policies":                     {Status: http.StatusCreated, Body: `{"policy":` + p + `}`},
		"PUT /qos/policies/" + qosID:             {Body: `{"policy":` + p + `}`},
		"DELETE /qos/policies/" + qosID:          {Status: http.StatusNoContent},
		"GET " + rulePath + "/" + ruleID:         {Body: `{"minimum_packet_rate_rule":` + rule + `}`},
		"POST " + rulePath:                       {Status: http.StatusCreated, Body: `{"minimum_packet_rate_rule":` + rule + `}`},
		"PUT " + rulePath + "/" + ruleID:         {Body: `{"minimum_packet_rate_rule":` + rule + `}`},
		"DELETE " + rulePath + "/" + ruleID:      {Status: http.StatusNoContent},
		"GET /qos/rule-types?all_supported=true": {Body: `{"rule_types":[{"type":"bandwidth_limit"},{"type":"dscp_marking"}]}`},
		"GET /qos/rule-types?all_rules=true":     {Body: `{"rule_types":[{"type":"bandwidth_limit"}]}`},
		"GET /qos/rule-types":                    {Body: `{"rule_types":[]}`},
		"GET /qos/rule-types/bandwidth_limit":    {Body: `{"rule_type":{"type":"bandwidth_limit","drivers":[{"name":"ovs"}]}}`},
	})
	ctx := context.Background()
	shared := true
	if all, err := network.ListQoSPolicies(ctx, sc, policies.ListOpts{Shared: &shared}); err != nil || len(all) != 1 || all[0].Name != "gold" {
		t.Fatalf("ListQoSPolicies = %+v, %v", all, err)
	}
	if got, err := network.GetQoSPolicy(ctx, sc, qosID); err != nil || len(got.Rules) != 1 {
		t.Fatalf("GetQoSPolicy = %+v, %v", got, err)
	}
	if _, err := network.CreateQoSPolicy(ctx, sc, network.WithQoSPolicyCreateAttrs(policies.CreateOpts{Name: "gold"},
		map[string]any{"shared": false})); err != nil {
		t.Fatalf("CreateQoSPolicy: %v", err)
	}
	if _, err := network.UpdateQoSPolicy(ctx, sc, qosID, policies.UpdateOpts{IsDefault: &shared}); err != nil {
		t.Fatalf("UpdateQoSPolicy: %v", err)
	}
	if err := network.DeleteQoSPolicy(ctx, sc, qosID); err != nil {
		t.Fatalf("DeleteQoSPolicy: %v", err)
	}
	typ, err := network.FindQoSRuleType(ctx, sc, qosID, ruleID)
	if err != nil || typ != "minimum_packet_rate" {
		t.Fatalf("FindQoSRuleType = %q, %v", typ, err)
	}
	if r, err := network.GetQoSRule(ctx, sc, qosID, typ, ruleID); err != nil || r["direction"] != "any" {
		t.Fatalf("GetQoSRule = %v, %v", r, err)
	}
	if r, err := network.CreateQoSRule(ctx, sc, qosID, typ, map[string]any{"min_kpps": 1000}); err != nil || r["id"] != ruleID {
		t.Fatalf("CreateQoSRule = %v, %v", r, err)
	}
	if _, err := network.UpdateQoSRule(ctx, sc, qosID, typ, ruleID, map[string]any{"direction": "any"}); err != nil {
		t.Fatalf("UpdateQoSRule: %v", err)
	}
	if err := network.DeleteQoSRule(ctx, sc, qosID, typ, ruleID); err != nil {
		t.Fatalf("DeleteQoSRule: %v", err)
	}
	if all, err := network.ListQoSRuleTypes(ctx, sc, network.QoSRuleTypeListOpts{AllSupported: true, AllRules: true}); err != nil || len(all) != 2 {
		t.Fatalf("ListQoSRuleTypes(all supported) = %+v, %v", all, err)
	}
	if all, err := network.ListQoSRuleTypes(ctx, sc, network.QoSRuleTypeListOpts{AllRules: true}); err != nil || len(all) != 1 {
		t.Fatalf("ListQoSRuleTypes(all rules) = %+v, %v", all, err)
	}
	if all, err := network.ListQoSRuleTypes(ctx, sc, network.QoSRuleTypeListOpts{}); err != nil || len(all) != 0 {
		t.Fatalf("ListQoSRuleTypes = %+v, %v", all, err)
	}
	if rt, err := network.GetQoSRuleType(ctx, sc, "bandwidth_limit"); err != nil || len(rt.Drivers) != 1 {
		t.Fatalf("GetQoSRuleType = %+v, %v", rt, err)
	}
	assertRequests(t, *seen, []req{
		{"GET", "/qos/policies", "shared=true", "", ""},
		{"GET", "/qos/policies/" + qosID, "", "", ""},
		{"POST", "/qos/policies", "", "", `{"policy":{"name":"gold","shared":false}}`},
		{"PUT", "/qos/policies/" + qosID, "", "", `{"policy":{"is_default":true}}`},
		{"DELETE", "/qos/policies/" + qosID, "", "", ""},
		{"GET", "/qos/policies/" + qosID, "", "", ""},
		{"GET", rulePath + "/" + ruleID, "", "", ""},
		{"POST", rulePath, "", "", `{"minimum_packet_rate_rule":{"min_kpps":1000}}`},
		{"PUT", rulePath + "/" + ruleID, "", "", `{"minimum_packet_rate_rule":{"direction":"any"}}`},
		{"DELETE", rulePath + "/" + ruleID, "", "", ""},
		{"GET", "/qos/rule-types", "all_supported=true", "", ""},
		{"GET", "/qos/rule-types", "all_rules=true", "", ""},
		{"GET", "/qos/rule-types", "", "", ""},
		{"GET", "/qos/rule-types/bandwidth_limit", "", "", ""},
	})

	_, err = network.FindQoSRuleType(ctx, sc, qosID, "other")
	var nf *network.QoSRuleNotFoundError
	if !errors.As(err, &nf) || !errors.Is(err, openstack.ErrNotFound) || err.Error() != "QoS policy "+qosID+" has no rule other" {
		t.Errorf("missing rule = %v", err)
	}
	_, gone := fakeNeutron(t, nil)
	is404(t, second(network.ListQoSPolicies(ctx, gone, policies.ListOpts{})), "listing network QoS policies: ")
	is404(t, second(network.GetQoSPolicy(ctx, gone, "q")), "getting network QoS policy q: ")
	is404(t, second(network.CreateQoSPolicy(ctx, gone, policies.CreateOpts{})), "creating network QoS policy: ")
	is404(t, second(network.UpdateQoSPolicy(ctx, gone, "q", policies.UpdateOpts{})), "updating network QoS policy q: ")
	is404(t, network.DeleteQoSPolicy(ctx, gone, "q"), "deleting network QoS policy q: ")
	is404(t, second(network.FindQoSRuleType(ctx, gone, "q", "r")), "looking up QoS rule r: ")
	is404(t, second(network.GetQoSRule(ctx, gone, "q", "dscp_marking", "r")), "getting QoS rule r: ")
	is404(t, second(network.CreateQoSRule(ctx, gone, "q", "dscp_marking", nil)), "creating a dscp_marking rule on QoS policy q: ")
	is404(t, second(network.UpdateQoSRule(ctx, gone, "q", "dscp_marking", "r", nil)), "updating QoS rule r: ")
	is404(t, network.DeleteQoSRule(ctx, gone, "q", "dscp_marking", "r"), "deleting QoS rule r: ")
	is404(t, second(network.ListQoSRuleTypes(ctx, gone, network.QoSRuleTypeListOpts{})), "listing QoS rule types: ")
	is404(t, second(network.GetQoSRuleType(ctx, gone, "x")), "showing QoS rule type x: ")
}

func TestAddressScopesAndGroups(t *testing.T) {
	s := `{"id":"as-1","name":"as1","ip_version":4,"shared":false}`
	g := `{"id":"ag-1","name":"ag1","addresses":["192.0.2.0/28"]}`
	seen, sc := fakeNeutron(t, map[string]reply{
		"GET /address-scopes":                       {Body: `{"address_scopes":[` + s + `]}`},
		"GET /address-scopes/as-1":                  {Body: `{"address_scope":` + s + `}`},
		"POST /address-scopes":                      {Status: http.StatusCreated, Body: `{"address_scope":` + s + `}`},
		"PUT /address-scopes/as-1":                  {Body: `{"address_scope":` + s + `}`},
		"DELETE /address-scopes/as-1":               {Status: http.StatusNoContent},
		"GET /address-groups":                       {Body: `{"address_groups":[` + g + `]}`},
		"GET /address-groups/ag-1":                  {Body: `{"address_group":` + g + `}`},
		"POST /address-groups":                      {Status: http.StatusCreated, Body: `{"address_group":` + g + `}`},
		"PUT /address-groups/ag-1":                  {Body: `{"address_group":` + g + `}`},
		"DELETE /address-groups/ag-1":               {Status: http.StatusNoContent},
		"PUT /address-groups/ag-1/add_addresses":    {Body: `{"address_group":` + g + `}`},
		"PUT /address-groups/ag-1/remove_addresses": {Body: `{"address_group":` + g + `}`},
	})
	ctx := context.Background()
	if all, err := network.ListAddressScopes(ctx, sc, addressscopes.ListOpts{IPVersion: 4}); err != nil || len(all) != 1 {
		t.Fatalf("ListAddressScopes = %+v, %v", all, err)
	}
	if got, err := network.GetAddressScope(ctx, sc, "as-1"); err != nil || got.Name != "as1" {
		t.Fatalf("GetAddressScope = %+v, %v", got, err)
	}
	if _, err := network.CreateAddressScope(ctx, sc, network.WithAddressScopeCreateAttrs(
		addressscopes.CreateOpts{Name: "as1", IPVersion: 4}, map[string]any{"shared": false})); err != nil {
		t.Fatalf("CreateAddressScope: %v", err)
	}
	name := "as2"
	if _, err := network.UpdateAddressScope(ctx, sc, "as-1", addressscopes.UpdateOpts{Name: &name}); err != nil {
		t.Fatalf("UpdateAddressScope: %v", err)
	}
	if err := network.DeleteAddressScope(ctx, sc, "as-1"); err != nil {
		t.Fatalf("DeleteAddressScope: %v", err)
	}
	if all, err := network.ListAddressGroups(ctx, sc, addressgroups.ListOpts{Name: "ag1"}); err != nil || len(all) != 1 {
		t.Fatalf("ListAddressGroups = %+v, %v", all, err)
	}
	if got, err := network.GetAddressGroup(ctx, sc, "ag-1"); err != nil || got.Addresses[0] != "192.0.2.0/28" {
		t.Fatalf("GetAddressGroup = %+v, %v", got, err)
	}
	if _, err := network.CreateAddressGroup(ctx, sc, addressgroups.CreateOpts{Name: "ag1", Addresses: []string{}}); err != nil {
		t.Fatalf("CreateAddressGroup: %v", err)
	}
	if _, err := network.UpdateAddressGroup(ctx, sc, "ag-1", addressgroups.UpdateOpts{Name: &name}); err != nil {
		t.Fatalf("UpdateAddressGroup: %v", err)
	}
	if _, err := network.AddAddressGroupAddresses(ctx, sc, "ag-1", []string{"198.51.100.0/24"}); err != nil {
		t.Fatalf("AddAddressGroupAddresses: %v", err)
	}
	if _, err := network.RemoveAddressGroupAddresses(ctx, sc, "ag-1", []string{"198.51.100.0/24"}); err != nil {
		t.Fatalf("RemoveAddressGroupAddresses: %v", err)
	}
	if err := network.DeleteAddressGroup(ctx, sc, "ag-1"); err != nil {
		t.Fatalf("DeleteAddressGroup: %v", err)
	}
	assertRequests(t, *seen, []req{
		{"GET", "/address-scopes", "ip_version=4", "", ""},
		{"GET", "/address-scopes/as-1", "", "", ""},
		{"POST", "/address-scopes", "", "", `{"address_scope":{"ip_version":4,"name":"as1","shared":false}}`},
		{"PUT", "/address-scopes/as-1", "", "", `{"address_scope":{"name":"as2"}}`},
		{"DELETE", "/address-scopes/as-1", "", "", ""},
		{"GET", "/address-groups", "name=ag1", "", ""},
		{"GET", "/address-groups/ag-1", "", "", ""},
		{"POST", "/address-groups", "", "", `{"address_group":{"addresses":[],"name":"ag1"}}`},
		{"PUT", "/address-groups/ag-1", "", "", `{"address_group":{"name":"as2"}}`},
		{"PUT", "/address-groups/ag-1/add_addresses", "", "", `{"addresses":["198.51.100.0/24"]}`},
		{"PUT", "/address-groups/ag-1/remove_addresses", "", "", `{"addresses":["198.51.100.0/24"]}`},
		{"DELETE", "/address-groups/ag-1", "", "", ""},
	})
	_, gone := fakeNeutron(t, nil)
	is404(t, second(network.ListAddressScopes(ctx, gone, addressscopes.ListOpts{})), "listing address scopes: ")
	is404(t, second(network.GetAddressScope(ctx, gone, "s")), "showing address scope s: ")
	is404(t, second(network.CreateAddressScope(ctx, gone, addressscopes.CreateOpts{})), "creating address scope: ")
	is404(t, second(network.UpdateAddressScope(ctx, gone, "s", addressscopes.UpdateOpts{})), "updating address scope s: ")
	is404(t, network.DeleteAddressScope(ctx, gone, "s"), "deleting address scope s: ")
	is404(t, second(network.ListAddressGroups(ctx, gone, addressgroups.ListOpts{})), "listing address groups: ")
	is404(t, second(network.GetAddressGroup(ctx, gone, "g")), "showing address group g: ")
	is404(t, second(network.CreateAddressGroup(ctx, gone, addressgroups.CreateOpts{Addresses: []string{}})), "creating address group: ")
	is404(t, second(network.UpdateAddressGroup(ctx, gone, "g", addressgroups.UpdateOpts{})), "updating address group g: ")
	is404(t, network.DeleteAddressGroup(ctx, gone, "g"), "deleting address group g: ")
	is404(t, second(network.AddAddressGroupAddresses(ctx, gone, "g", []string{})), "adding addresses to address group g: ")
	is404(t, second(network.RemoveAddressGroupAddresses(ctx, gone, "g", []string{})), "removing addresses from address group g: ")
}

func TestSubnetPools(t *testing.T) {
	unset := `{"id":"sp-1","name":"pool","prefixes":["192.0.2.0/24"],"default_quota":null,"default_prefixlen":26,"min_prefixlen":"8","max_prefixlen":32}`
	set := `{"id":"sp-1","name":"pool","prefixes":["192.0.2.0/24"],"default_quota":0,"default_prefixlen":26,"min_prefixlen":8,"max_prefixlen":32}`
	seen, sc := fakeNeutron(t, map[string]reply{
		"GET /subnetpools":         {Body: `{"subnetpools":[` + unset + `]}`},
		"GET /subnetpools/sp-1":    {Body: `{"subnetpool":` + unset + `}`},
		"POST /subnetpools":        {Status: http.StatusCreated, Body: `{"subnetpool":` + set + `}`},
		"PUT /subnetpools/sp-1":    {Body: `{"subnetpool":` + set + `}`},
		"DELETE /subnetpools/sp-1": {Status: http.StatusNoContent},
	})
	ctx := context.Background()
	if all, err := network.ListSubnetPools(ctx, sc, subnetpools.ListOpts{Name: "pool"}); err != nil || len(all) != 1 {
		t.Fatalf("ListSubnetPools = %+v, %v", all, err)
	}
	if got, err := network.GetSubnetPool(ctx, sc, "sp-1"); err != nil || got.Name != "pool" || got.Attrs.DefaultQuota != nil {
		t.Fatalf("GetSubnetPool = %+v, %v", got, err)
	}
	got, err := network.CreateSubnetPool(ctx, sc, subnetpools.CreateOpts{Name: "pool", Prefixes: []string{"192.0.2.0/24"}})
	if err != nil || got.Attrs.DefaultQuota == nil || *got.Attrs.DefaultQuota != 0 {
		t.Fatalf("CreateSubnetPool = %+v, %v", got, err)
	}
	if _, err := network.UpdateSubnetPool(ctx, sc, "sp-1", subnetpools.UpdateOpts{Prefixes: []string{"192.0.2.0/24"}}); err != nil {
		t.Fatalf("UpdateSubnetPool: %v", err)
	}
	if err := network.DeleteSubnetPool(ctx, sc, "sp-1"); err != nil {
		t.Fatalf("DeleteSubnetPool: %v", err)
	}
	assertRequests(t, *seen, []req{
		{"GET", "/subnetpools", "name=pool", "", ""},
		{"GET", "/subnetpools/sp-1", "", "", ""},
		{"POST", "/subnetpools", "", "", `{"subnetpool":{"name":"pool","prefixes":["192.0.2.0/24"]}}`},
		{"PUT", "/subnetpools/sp-1", "", "", `{"subnetpool":{"prefixes":["192.0.2.0/24"]}}`},
		{"DELETE", "/subnetpools/sp-1", "", "", ""},
	})
	_, bad := fakeNeutron(t, map[string]reply{"GET /subnetpools/sp-1": {Body: `{"subnetpool":{"id":"sp-1","default_prefixlen":26,"min_prefixlen":8,"max_prefixlen":32,"default_quota":"lots"}}`}})
	if _, err := network.GetSubnetPool(ctx, bad, "sp-1"); err == nil || !strings.HasPrefix(err.Error(), "getting subnet pool sp-1: ") {
		t.Errorf("malformed pool: %v", err)
	}
	_, gone := fakeNeutron(t, nil)
	is404(t, second(network.ListSubnetPools(ctx, gone, subnetpools.ListOpts{})), "listing subnet pools: ")
	is404(t, second(network.GetSubnetPool(ctx, gone, "p")), "getting subnet pool p: ")
	is404(t, second(network.CreateSubnetPool(ctx, gone, subnetpools.CreateOpts{})), "creating subnet pool: ")
	is404(t, second(network.UpdateSubnetPool(ctx, gone, "p", subnetpools.UpdateOpts{})), "updating subnet pool p: ")
	is404(t, network.DeleteSubnetPool(ctx, gone, "p"), "deleting subnet pool p: ")
}

func TestTrunks(t *testing.T) {
	tr := `{"id":"` + trunkID + `","name":"t1","port_id":"` + portID + `","sub_ports":[]}`
	seen, sc := fakeNeutron(t, map[string]reply{
		"GET /trunks":                                 {Body: `{"trunks":[` + tr + `]}`},
		"GET /trunks/" + trunkID:                      {Body: `{"trunk":` + tr + `}`},
		"POST /trunks":                                {Status: http.StatusCreated, Body: `{"trunk":` + tr + `}`},
		"PUT /trunks/" + trunkID:                      {Body: `{"trunk":` + tr + `}`},
		"DELETE /trunks/" + trunkID:                   {Status: http.StatusNoContent},
		"GET /trunks/" + trunkID + "/get_subports":    {Body: `{"sub_ports":[{"port_id":"sp","segmentation_type":"vlan","segmentation_id":10}]}`},
		"PUT /trunks/" + trunkID + "/add_subports":    {Body: tr},
		"PUT /trunks/" + trunkID + "/remove_subports": {Body: tr},
	})
	ctx := context.Background()
	if all, err := network.ListTrunks(ctx, sc, trunks.ListOpts{PortID: portID}); err != nil || len(all) != 1 {
		t.Fatalf("ListTrunks = %+v, %v", all, err)
	}
	if got, err := network.GetTrunk(ctx, sc, trunkID); err != nil || got.Name != "t1" {
		t.Fatalf("GetTrunk = %+v, %v", got, err)
	}
	if _, err := network.CreateTrunk(ctx, sc, network.WithTrunkCreateAttrs(trunks.CreateOpts{PortID: portID},
		map[string]any{"sub_ports": network.AddTrunkSubportsOpts{{PortID: "sp", SegmentationType: "inherit"}}})); err != nil {
		t.Fatalf("CreateTrunk: %v", err)
	}
	name := "t2"
	if _, err := network.UpdateTrunk(ctx, sc, trunkID, trunks.UpdateOpts{Name: &name}); err != nil {
		t.Fatalf("UpdateTrunk: %v", err)
	}
	if subs, err := network.ListTrunkSubports(ctx, sc, trunkID); err != nil || len(subs) != 1 || subs[0].SegmentationID != 10 {
		t.Fatalf("ListTrunkSubports = %+v, %v", subs, err)
	}
	if _, err := network.AddTrunkSubports(ctx, sc, trunkID, network.AddTrunkSubportsOpts{{PortID: "sp"}}); err != nil {
		t.Fatalf("AddTrunkSubports: %v", err)
	}
	if _, err := network.RemoveTrunkSubports(ctx, sc, trunkID, trunks.RemoveSubportsOpts{Subports: []trunks.RemoveSubport{{PortID: "sp"}}}); err != nil {
		t.Fatalf("RemoveTrunkSubports: %v", err)
	}
	if err := network.DeleteTrunk(ctx, sc, trunkID); err != nil {
		t.Fatalf("DeleteTrunk: %v", err)
	}
	assertRequests(t, *seen, []req{
		{"GET", "/trunks", "port_id=" + portID, "", ""},
		{"GET", "/trunks/" + trunkID, "", "", ""},
		{"POST", "/trunks", "", "", `{"trunk":{"port_id":"` + portID + `","sub_ports":[{"port_id":"sp","segmentation_type":"inherit"}]}}`},
		{"PUT", "/trunks/" + trunkID, "", "", `{"trunk":{"name":"t2"}}`},
		{"GET", "/trunks/" + trunkID + "/get_subports", "", "", ""},
		{"PUT", "/trunks/" + trunkID + "/add_subports", "", "", `{"sub_ports":[{"port_id":"sp"}]}`},
		{"PUT", "/trunks/" + trunkID + "/remove_subports", "", "", `{"sub_ports":[{"port_id":"sp"}]}`},
		{"DELETE", "/trunks/" + trunkID, "", "", ""},
	})
	_, gone := fakeNeutron(t, nil)
	is404(t, second(network.ListTrunks(ctx, gone, trunks.ListOpts{})), "listing network trunks: ")
	is404(t, second(network.GetTrunk(ctx, gone, "t")), "getting network trunk t: ")
	is404(t, second(network.CreateTrunk(ctx, gone, trunks.CreateOpts{PortID: "p"})), "creating network trunk: ")
	is404(t, second(network.UpdateTrunk(ctx, gone, "t", trunks.UpdateOpts{})), "updating network trunk t: ")
	is404(t, network.DeleteTrunk(ctx, gone, "t"), "deleting network trunk t: ")
	is404(t, second(network.ListTrunkSubports(ctx, gone, "t")), "listing sub-ports of trunk t: ")
	is404(t, second(network.AddTrunkSubports(ctx, gone, "t", network.AddTrunkSubportsOpts{})), "adding sub-ports to trunk t: ")
	is404(t, second(network.RemoveTrunkSubports(ctx, gone, "t", trunks.RemoveSubportsOpts{})), "removing sub-ports from trunk t: ")
}

func TestIPAvailabilityRBACSegments(t *testing.T) {
	av := `{"network_id":"` + netID + `","network_name":"private","total_ips":253,"used_ips":3,` +
		`"subnet_ip_availability":[{"subnet_id":"` + subnetID + `","cidr":"192.0.2.0/24","ip_version":4,"total_ips":253,"used_ips":3}]`
	rb := `{"id":"rb-1","object_type":"network","object_id":"` + netID + `","action":"access_as_shared","target_tenant":"*"}`
	seg := `{"id":"seg-1","name":"s1","network_id":"` + netID + `","network_type":"vlan","segmentation_id":100}`
	seen, sc := fakeNeutron(t, map[string]reply{
		"GET /network-ip-availabilities":          {Body: `{"network_ip_availabilities":[` + av + `,"ip_availability_details":{"used_ips_in_subnet":3}},` + av + `}]}`},
		"GET /network-ip-availabilities/" + netID: {Body: `{"network_ip_availability":` + av + `}}`},
		"GET /rbac-policies":                      {Body: `{"rbac_policies":[` + rb + `]}`},
		"GET /rbac-policies/rb-1":                 {Body: `{"rbac_policy":` + rb + `}`},
		"POST /rbac-policies":                     {Status: http.StatusCreated, Body: `{"rbac_policy":` + rb + `}`},
		"PUT /rbac-policies/rb-1":                 {Body: `{"rbac_policy":` + rb + `}`},
		"DELETE /rbac-policies/rb-1":              {Status: http.StatusNoContent},
		"GET /segments":                           {Body: `{"segments":[` + seg + `]}`},
		"GET /segments/seg-1":                     {Body: `{"segment":` + seg + `}`},
		"POST /segments":                          {Status: http.StatusCreated, Body: `{"segment":` + seg + `}`},
		"PUT /segments/seg-1":                     {Body: `{"segment":` + seg + `}`},
		"DELETE /segments/seg-1":                  {Status: http.StatusNoContent},
	})
	ctx := context.Background()
	all, err := network.ListIPAvailabilities(ctx, sc, networkipavailabilities.ListOpts{IPVersion: "4"})
	if err != nil || len(all) != 2 || all[0].TotalIPs != "253" || all[0].Details["used_ips_in_subnet"] != float64(3) || all[1].Details != nil {
		t.Fatalf("ListIPAvailabilities = %+v, %v", all, err)
	}
	if got, err := network.GetIPAvailability(ctx, sc, netID); err != nil || len(got.SubnetIPAvailabilities) != 1 {
		t.Fatalf("GetIPAvailability = %+v, %v", got, err)
	}
	if ps, err := network.ListRBACPolicies(ctx, sc, rbacpolicies.ListOpts{ObjectType: "network"}); err != nil || len(ps) != 1 {
		t.Fatalf("ListRBACPolicies = %+v, %v", ps, err)
	}
	if p, err := network.GetRBACPolicy(ctx, sc, "rb-1"); err != nil || p.TargetTenant != "*" {
		t.Fatalf("GetRBACPolicy = %+v, %v", p, err)
	}
	if _, err := network.CreateRBACPolicy(ctx, sc, network.WithRBACPolicyCreateAttrs(rbacpolicies.CreateOpts{
		Action: rbacpolicies.ActionAccessShared, ObjectType: "network", ObjectID: netID, TargetTenant: "*",
	}, map[string]any{"project_id": "p1"})); err != nil {
		t.Fatalf("CreateRBACPolicy: %v", err)
	}
	if _, err := network.UpdateRBACPolicy(ctx, sc, "rb-1", network.WithRBACPolicyUpdateAttrs(network.RBACPolicyEmptyUpdate{},
		map[string]any{"description": "d"})); err != nil {
		t.Fatalf("UpdateRBACPolicy: %v", err)
	}
	if err := network.DeleteRBACPolicy(ctx, sc, "rb-1"); err != nil {
		t.Fatalf("DeleteRBACPolicy: %v", err)
	}
	if ss, err := network.ListSegments(ctx, sc, segments.ListOpts{NetworkID: netID}); err != nil || len(ss) != 1 {
		t.Fatalf("ListSegments = %+v, %v", ss, err)
	}
	if s, err := network.GetSegment(ctx, sc, "seg-1"); err != nil || s.SegmentationID != 100 {
		t.Fatalf("GetSegment = %+v, %v", s, err)
	}
	if _, err := network.CreateSegment(ctx, sc, segments.CreateOpts{NetworkID: netID, NetworkType: "vlan"}); err != nil {
		t.Fatalf("CreateSegment: %v", err)
	}
	name := "s2"
	if _, err := network.UpdateSegment(ctx, sc, "seg-1", segments.UpdateOpts{Name: &name}); err != nil {
		t.Fatalf("UpdateSegment: %v", err)
	}
	if err := network.DeleteSegment(ctx, sc, "seg-1"); err != nil {
		t.Fatalf("DeleteSegment: %v", err)
	}
	assertRequests(t, *seen, []req{
		{"GET", "/network-ip-availabilities", "ip_version=4", "", ""},
		{"GET", "/network-ip-availabilities/" + netID, "", "", ""},
		{"GET", "/rbac-policies", "object_type=network", "", ""},
		{"GET", "/rbac-policies/rb-1", "", "", ""},
		{"POST", "/rbac-policies", "", "", `{"rbac_policy":{"action":"access_as_shared","object_id":"` + netID +
			`","object_type":"network","project_id":"p1","target_tenant":"*"}}`},
		{"PUT", "/rbac-policies/rb-1", "", "", `{"rbac_policy":{"description":"d"}}`},
		{"DELETE", "/rbac-policies/rb-1", "", "", ""},
		{"GET", "/segments", "network_id=" + netID, "", ""},
		{"GET", "/segments/seg-1", "", "", ""},
		{"POST", "/segments", "", "", `{"segment":{"network_id":"` + netID + `","network_type":"vlan"}}`},
		{"PUT", "/segments/seg-1", "", "", `{"segment":{"name":"s2"}}`},
		{"DELETE", "/segments/seg-1", "", "", ""},
	})
	_, bad := fakeNeutron(t, map[string]reply{"GET /network-ip-availabilities": {
		Body: `{"network_ip_availabilities":[{"network_id":"n","total_ips":1,"ip_availability_details":"none"}]}`}})
	if _, err := network.ListIPAvailabilities(ctx, bad, networkipavailabilities.ListOpts{}); err == nil ||
		!strings.HasPrefix(err.Error(), "parsing the IP availability list: ") {
		t.Errorf("malformed details: %v", err)
	}
	_, gone := fakeNeutron(t, nil)
	is404(t, second(network.ListIPAvailabilities(ctx, gone, networkipavailabilities.ListOpts{})), "listing IP availability: ")
	is404(t, second(network.GetIPAvailability(ctx, gone, "n")), "showing IP availability for network n: ")
	is404(t, second(network.ListRBACPolicies(ctx, gone, rbacpolicies.ListOpts{})), "listing network RBAC policies: ")
	is404(t, second(network.GetRBACPolicy(ctx, gone, "r")), "showing network RBAC policy r: ")
	is404(t, second(network.CreateRBACPolicy(ctx, gone, rbacpolicies.CreateOpts{Action: rbacpolicies.ActionAccessShared, ObjectType: "network", ObjectID: "n", TargetTenant: "*"})), "creating network RBAC policy: ")
	is404(t, second(network.UpdateRBACPolicy(ctx, gone, "r", network.RBACPolicyEmptyUpdate{})), "updating network RBAC policy r: ")
	is404(t, network.DeleteRBACPolicy(ctx, gone, "r"), "deleting network RBAC policy r: ")
	is404(t, second(network.ListSegments(ctx, gone, segments.ListOpts{})), "listing network segments: ")
	is404(t, second(network.GetSegment(ctx, gone, "s")), "showing network segment s: ")
	is404(t, second(network.CreateSegment(ctx, gone, segments.CreateOpts{NetworkID: "n", NetworkType: "vlan"})), "creating network segment: ")
	is404(t, second(network.UpdateSegment(ctx, gone, "s", segments.UpdateOpts{})), "updating network segment s: ")
	is404(t, network.DeleteSegment(ctx, gone, "s"), "deleting network segment s: ")
}

func TestPortForwardingsAndRoutes(t *testing.T) {
	pf := `{"id":"pf-1","protocol":"tcp","internal_port_id":"` + portID + `","internal_ip_address":"192.0.2.10",` +
		`"internal_port":22,"external_port":2222}`
	base := "/floatingips/" + fipID + "/port_forwardings"
	r := `{"router":{"id":"` + routerID + `","name":"r1","routes":[{"destination":"203.0.113.0/24","nexthop":"192.0.2.254"}]}}`
	seen, sc := fakeNeutron(t, map[string]reply{
		"GET " + base:              {Body: `{"port_forwardings":[` + pf + `]}`},
		"GET " + base + "/pf-1":    {Body: `{"port_forwarding":` + pf + `}`},
		"POST " + base:             {Status: http.StatusCreated, Body: `{"port_forwarding":` + pf + `}`},
		"PUT " + base + "/pf-1":    {Body: `{"port_forwarding":` + pf + `}`},
		"DELETE " + base + "/pf-1": {Status: http.StatusNoContent},
		"PUT /routers/" + routerID + "/add_extraroutes":    {Body: r},
		"PUT /routers/" + routerID + "/remove_extraroutes": {Body: r},
	})
	ctx := context.Background()
	if all, err := network.ListPortForwardings(ctx, sc, fipID, portforwarding.ListOpts{Protocol: "tcp"}); err != nil || len(all) != 1 {
		t.Fatalf("ListPortForwardings = %+v, %v", all, err)
	}
	if got, err := network.GetPortForwarding(ctx, sc, fipID, "pf-1"); err != nil || got.ExternalPort != 2222 {
		t.Fatalf("GetPortForwarding = %+v, %v", got, err)
	}
	if _, err := network.CreatePortForwarding(ctx, sc, fipID, portforwarding.CreateOpts{
		Protocol: "tcp", InternalPortID: portID, InternalIPAddress: "192.0.2.10", InternalPort: 22, ExternalPort: 2222,
	}); err != nil {
		t.Fatalf("CreatePortForwarding: %v", err)
	}
	if _, err := network.UpdatePortForwarding(ctx, sc, fipID, "pf-1", portforwarding.UpdateOpts{ExternalPort: 2200}); err != nil {
		t.Fatalf("UpdatePortForwarding: %v", err)
	}
	if err := network.DeletePortForwarding(ctx, sc, fipID, "pf-1"); err != nil {
		t.Fatalf("DeletePortForwarding: %v", err)
	}
	route := []routers.Route{{DestinationCIDR: "203.0.113.0/24", NextHop: "192.0.2.254"}}
	if got, err := network.AddRouterRoutes(ctx, sc, routerID, route); err != nil || len(got.Routes) != 1 {
		t.Fatalf("AddRouterRoutes = %+v, %v", got, err)
	}
	if _, err := network.RemoveRouterRoutes(ctx, sc, routerID, route); err != nil {
		t.Fatalf("RemoveRouterRoutes: %v", err)
	}
	routeBody := `{"router":{"routes":[{"destination":"203.0.113.0/24","nexthop":"192.0.2.254"}]}}`
	assertRequests(t, *seen, []req{
		{"GET", base, "protocol=tcp", "", ""},
		{"GET", base + "/pf-1", "", "", ""},
		{"POST", base, "", "", `{"port_forwarding":{"external_port":2222,"internal_ip_address":"192.0.2.10","internal_port":22,` +
			`"internal_port_id":"` + portID + `","protocol":"tcp"}}`},
		{"PUT", base + "/pf-1", "", "", `{"port_forwarding":{"external_port":2200}}`},
		{"DELETE", base + "/pf-1", "", "", ""},
		{"PUT", "/routers/" + routerID + "/add_extraroutes", "", "", routeBody},
		{"PUT", "/routers/" + routerID + "/remove_extraroutes", "", "", routeBody},
	})
	_, gone := fakeNeutron(t, nil)
	is404(t, second(network.ListPortForwardings(ctx, gone, "f", portforwarding.ListOpts{})), "listing port forwardings of floating IP f: ")
	is404(t, second(network.GetPortForwarding(ctx, gone, "f", "p")), "showing port forwarding p: ")
	is404(t, second(network.CreatePortForwarding(ctx, gone, "f", portforwarding.CreateOpts{})), "creating a port forwarding on floating IP f: ")
	is404(t, second(network.UpdatePortForwarding(ctx, gone, "f", "p", portforwarding.UpdateOpts{})), "updating port forwarding p: ")
	is404(t, network.DeletePortForwarding(ctx, gone, "f", "p"), "deleting port forwarding p: ")
	is404(t, second(network.AddRouterRoutes(ctx, gone, "r", route)), "updating the routes of router r: ")
	is404(t, second(network.RemoveRouterRoutes(ctx, gone, "r", route)), "updating the routes of router r: ")
}

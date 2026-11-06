package network_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/bgpvpns"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/fwaas_v2/groups"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/fwaas_v2/policies"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/fwaas_v2/rules"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/vpnaas/endpointgroups"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/vpnaas/ikepolicies"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/vpnaas/ipsecpolicies"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/vpnaas/services"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/vpnaas/siteconnections"

	"github.com/gophercloud/gophercloud/v2"

	"github.com/ftarasenko/koc/pkg/openstack/network"
)

const (
	vpnID    = "2a3b4c5d-6e7f-4a8b-9c9d-1e2f3a4b5c6d"
	assocID  = "3b4c5d6e-7f8a-4b9c-8d0e-2f3a4b5c6d7e"
	fwID     = "4c5d6e7f-8a9b-4c0d-9e1f-3a4b5c6d7e8f"
	vpnObjID = "5d6e7f8a-9b0c-4d1e-8f2a-4b5c6d7e8f9a"
)

func TestBGPVPNs(t *testing.T) {
	vpn := `{"id":"` + vpnID + `","name":"v1","type":"l3","route_targets":["64512:1"]}`
	base := "/bgpvpn/bgpvpns"
	vpnPath := base + "/" + vpnID
	routes := map[string]reply{
		"GET " + base:          {Body: `{"bgpvpns":[` + vpn + `]}`},
		"GET " + vpnPath:       {Body: `{"bgpvpn":` + vpn + `}`},
		"POST " + base:         {Status: http.StatusCreated, Body: `{"bgpvpn":` + vpn + `}`},
		"PUT " + vpnPath:       {Body: `{"bgpvpn":` + vpn + `}`},
		"DELETE " + vpnPath:    {Status: http.StatusNoContent},
		"GET " + base + "/xxx": {Body: `{"bgpvpn":` + vpn + `}`},
	}
	assoc := map[string]string{
		"network": `{"id":"` + assocID + `","network_id":"` + netID + `"}`,
		"router":  `{"id":"` + assocID + `","router_id":"` + routerID + `","advertise_extra_routes":true}`,
		"port":    `{"id":"` + assocID + `","port_id":"` + portID + `","routes":[{"type":"prefix","prefix":"198.51.100.0/24"}]}`,
	}
	for kind, body := range assoc {
		coll := vpnPath + "/" + kind + "_associations"
		routes["GET "+coll] = reply{Body: `{"` + kind + `_associations":[` + body + `]}`}
		routes["POST "+coll] = reply{Status: http.StatusCreated, Body: `{"` + kind + `_association":` + body + `}`}
		routes["GET "+coll+"/"+assocID] = reply{Body: `{"` + kind + `_association":` + body + `}`}
		routes["PUT "+coll+"/"+assocID] = reply{Body: `{"` + kind + `_association":` + body + `}`}
		routes["DELETE "+coll+"/"+assocID] = reply{Status: http.StatusNoContent}
	}
	seen, sc := fakeNeutron(t, routes)
	ctx := context.Background()
	if all, err := network.ListBGPVPNs(ctx, sc, bgpvpns.ListOpts{ProjectID: "p"}); err != nil || len(all) != 1 || all[0].Name != "v1" {
		t.Fatalf("ListBGPVPNs = %+v, %v", all, err)
	}
	if got, err := network.GetBGPVPN(ctx, sc, vpnID); err != nil || got.Type != "l3" {
		t.Fatalf("GetBGPVPN = %+v, %v", got, err)
	}
	if _, err := network.CreateBGPVPN(ctx, sc, bgpvpns.CreateOpts{Name: "v1", Type: "l3"}); err != nil {
		t.Fatalf("CreateBGPVPN: %v", err)
	}
	name := "v2"
	if _, err := network.UpdateBGPVPN(ctx, sc, vpnID, bgpvpns.UpdateOpts{Name: &name}); err != nil {
		t.Fatalf("UpdateBGPVPN: %v", err)
	}
	if err := network.DeleteBGPVPN(ctx, sc, vpnID); err != nil {
		t.Fatalf("DeleteBGPVPN: %v", err)
	}
	if all, err := network.ListBGPVPNNetworkAssociations(ctx, sc, vpnID, nil); err != nil || len(all) != 1 || all[0].NetworkID != netID {
		t.Fatalf("ListBGPVPNNetworkAssociations = %+v, %v", all, err)
	}
	if _, err := network.CreateBGPVPNNetworkAssociation(ctx, sc, vpnID, bgpvpns.CreateNetworkAssociationOpts{NetworkID: netID}); err != nil {
		t.Fatalf("CreateBGPVPNNetworkAssociation: %v", err)
	}
	if got, err := network.GetBGPVPNNetworkAssociation(ctx, sc, vpnID, assocID); err != nil || got.ID != assocID {
		t.Fatalf("GetBGPVPNNetworkAssociation = %+v, %v", got, err)
	}
	if err := network.DeleteBGPVPNNetworkAssociation(ctx, sc, vpnID, assocID); err != nil {
		t.Fatalf("DeleteBGPVPNNetworkAssociation: %v", err)
	}
	if all, err := network.ListBGPVPNRouterAssociations(ctx, sc, vpnID, nil); err != nil || len(all) != 1 || !all[0].AdvertiseExtraRoutes {
		t.Fatalf("ListBGPVPNRouterAssociations = %+v, %v", all, err)
	}
	if _, err := network.CreateBGPVPNRouterAssociation(ctx, sc, vpnID, bgpvpns.CreateRouterAssociationOpts{RouterID: routerID}); err != nil {
		t.Fatalf("CreateBGPVPNRouterAssociation: %v", err)
	}
	if got, err := network.GetBGPVPNRouterAssociation(ctx, sc, vpnID, assocID); err != nil || got.RouterID != routerID {
		t.Fatalf("GetBGPVPNRouterAssociation = %+v, %v", got, err)
	}
	off := false
	if _, err := network.UpdateBGPVPNRouterAssociation(ctx, sc, vpnID, assocID, bgpvpns.UpdateRouterAssociationOpts{AdvertiseExtraRoutes: &off}); err != nil {
		t.Fatalf("UpdateBGPVPNRouterAssociation: %v", err)
	}
	if err := network.DeleteBGPVPNRouterAssociation(ctx, sc, vpnID, assocID); err != nil {
		t.Fatalf("DeleteBGPVPNRouterAssociation: %v", err)
	}
	if all, err := network.ListBGPVPNPortAssociations(ctx, sc, vpnID, nil); err != nil || len(all) != 1 || len(all[0].Routes) != 1 {
		t.Fatalf("ListBGPVPNPortAssociations = %+v, %v", all, err)
	}
	if _, err := network.CreateBGPVPNPortAssociation(ctx, sc, vpnID, bgpvpns.CreatePortAssociationOpts{PortID: portID}); err != nil {
		t.Fatalf("CreateBGPVPNPortAssociation: %v", err)
	}
	if got, err := network.GetBGPVPNPortAssociation(ctx, sc, vpnID, assocID); err != nil || got.PortID != portID {
		t.Fatalf("GetBGPVPNPortAssociation = %+v, %v", got, err)
	}
	if _, err := network.UpdateBGPVPNPortAssociation(ctx, sc, vpnID, assocID, bgpvpns.UpdatePortAssociationOpts{Routes: &[]bgpvpns.PortRoutes{}}); err != nil {
		t.Fatalf("UpdateBGPVPNPortAssociation: %v", err)
	}
	if err := network.DeleteBGPVPNPortAssociation(ctx, sc, vpnID, assocID); err != nil {
		t.Fatalf("DeleteBGPVPNPortAssociation: %v", err)
	}
	na, ra, pa := vpnPath+"/network_associations", vpnPath+"/router_associations", vpnPath+"/port_associations"
	assertRequests(t, *seen, []req{
		{"GET", base, "project_id=p", "", ""},
		{"GET", vpnPath, "", "", ""},
		{"POST", base, "", "", `{"bgpvpn":{"name":"v1","type":"l3"}}`},
		{"PUT", vpnPath, "", "", `{"bgpvpn":{"name":"v2"}}`},
		{"DELETE", vpnPath, "", "", ""},
		{"GET", na, "", "", ""},
		{"POST", na, "", "", `{"network_association":{"network_id":"` + netID + `"}}`},
		{"GET", na + "/" + assocID, "", "", ""},
		{"DELETE", na + "/" + assocID, "", "", ""},
		{"GET", ra, "", "", ""},
		{"POST", ra, "", "", `{"router_association":{"router_id":"` + routerID + `"}}`},
		{"GET", ra + "/" + assocID, "", "", ""},
		{"PUT", ra + "/" + assocID, "", "", `{"router_association":{"advertise_extra_routes":false}}`},
		{"DELETE", ra + "/" + assocID, "", "", ""},
		{"GET", pa, "", "", ""},
		{"POST", pa, "", "", `{"port_association":{"port_id":"` + portID + `"}}`},
		{"GET", pa + "/" + assocID, "", "", ""},
		{"PUT", pa + "/" + assocID, "", "", `{"port_association":{"routes":[]}}`},
		{"DELETE", pa + "/" + assocID, "", "", ""},
	})
	_, gone := fakeNeutron(t, nil)
	is404(t, second(network.ListBGPVPNs(ctx, gone, nil)), "listing BGP VPNs: ")
	is404(t, second(network.GetBGPVPN(ctx, gone, "v")), "getting BGP VPN v: ")
	is404(t, second(network.CreateBGPVPN(ctx, gone, bgpvpns.CreateOpts{})), "creating BGP VPN: ")
	is404(t, second(network.UpdateBGPVPN(ctx, gone, "v", bgpvpns.UpdateOpts{})), "updating BGP VPN v: ")
	is404(t, network.DeleteBGPVPN(ctx, gone, "v"), "deleting BGP VPN v: ")
	is404(t, second(network.ListBGPVPNNetworkAssociations(ctx, gone, "v", nil)), "listing network associations: ")
	is404(t, second(network.GetBGPVPNNetworkAssociation(ctx, gone, "v", "a")), "getting network association a: ")
	is404(t, second(network.CreateBGPVPNNetworkAssociation(ctx, gone, "v", bgpvpns.CreateNetworkAssociationOpts{NetworkID: "n"})), "creating network association: ")
	is404(t, network.DeleteBGPVPNNetworkAssociation(ctx, gone, "v", "a"), "deleting network association a: ")
	is404(t, second(network.ListBGPVPNRouterAssociations(ctx, gone, "v", nil)), "listing router associations: ")
	is404(t, second(network.GetBGPVPNRouterAssociation(ctx, gone, "v", "a")), "getting router association a: ")
	is404(t, second(network.CreateBGPVPNRouterAssociation(ctx, gone, "v", bgpvpns.CreateRouterAssociationOpts{RouterID: "r"})), "creating router association: ")
	is404(t, second(network.UpdateBGPVPNRouterAssociation(ctx, gone, "v", "a", bgpvpns.UpdateRouterAssociationOpts{})), "updating router association a: ")
	is404(t, network.DeleteBGPVPNRouterAssociation(ctx, gone, "v", "a"), "deleting router association a: ")
	is404(t, second(network.ListBGPVPNPortAssociations(ctx, gone, "v", nil)), "listing port associations: ")
	is404(t, second(network.GetBGPVPNPortAssociation(ctx, gone, "v", "a")), "getting port association a: ")
	is404(t, second(network.CreateBGPVPNPortAssociation(ctx, gone, "v", bgpvpns.CreatePortAssociationOpts{PortID: "p"})), "creating port association: ")
	is404(t, second(network.UpdateBGPVPNPortAssociation(ctx, gone, "v", "a", bgpvpns.UpdatePortAssociationOpts{})), "updating port association a: ")
	is404(t, network.DeleteBGPVPNPortAssociation(ctx, gone, "v", "a"), "deleting port association a: ")
}

func TestFirewalls(t *testing.T) {
	grp := `{"id":"` + fwID + `","name":"g1","ports":["` + portID + `"]}`
	pol := `{"id":"` + fwID + `","name":"p1","firewall_rules":["r1"]}`
	rule := `{"id":"` + fwID + `","name":"r1","action":"allow","source_firewall_group_id":"sg","destination_firewall_group_id":"dg"}`
	g, p, r := "/fwaas/firewall_groups", "/fwaas/firewall_policies", "/fwaas/firewall_rules"
	seen, sc := fakeNeutron(t, map[string]reply{
		"GET " + g:                               {Body: `{"firewall_groups":[` + grp + `]}`},
		"GET " + g + "/" + fwID:                  {Body: `{"firewall_group":` + grp + `}`},
		"POST " + g:                              {Status: http.StatusCreated, Body: `{"firewall_group":` + grp + `}`},
		"PUT " + g + "/" + fwID:                  {Body: `{"firewall_group":` + grp + `}`},
		"DELETE " + g + "/" + fwID:               {Status: http.StatusNoContent},
		"GET " + p:                               {Body: `{"firewall_policies":[` + pol + `]}`},
		"GET " + p + "/" + fwID:                  {Body: `{"firewall_policy":` + pol + `}`},
		"POST " + p:                              {Status: http.StatusCreated, Body: `{"firewall_policy":` + pol + `}`},
		"PUT " + p + "/" + fwID:                  {Body: `{"firewall_policy":` + pol + `}`},
		"DELETE " + p + "/" + fwID:               {Status: http.StatusNoContent},
		"PUT " + p + "/" + fwID + "/insert_rule": {Body: pol},
		"PUT " + p + "/" + fwID + "/remove_rule": {Body: pol},
		"GET " + r:                               {Body: `{"firewall_rules":[` + rule + `]}`},
		"GET " + r + "/" + fwID:                  {Body: `{"firewall_rule":` + rule + `}`},
		"POST " + r:                              {Status: http.StatusCreated, Body: `{"firewall_rule":` + rule + `}`},
		"PUT " + r + "/" + fwID:                  {Body: `{"firewall_rule":` + rule + `}`},
		"DELETE " + r + "/" + fwID:               {Status: http.StatusNoContent},
		"GET " + r + "/bad":                      {Body: `{"firewall_rule":{"id":7}}`},
	})
	ctx := context.Background()
	if all, err := network.ListFirewallGroups(ctx, sc, nil); err != nil || len(all) != 1 || all[0].Ports[0] != portID {
		t.Fatalf("ListFirewallGroups = %+v, %v", all, err)
	}
	if got, err := network.GetFirewallGroup(ctx, sc, fwID); err != nil || got.Name != "g1" {
		t.Fatalf("GetFirewallGroup = %+v, %v", got, err)
	}
	if _, err := network.CreateFirewallGroup(ctx, sc, groups.CreateOpts{Name: "g1"}); err != nil {
		t.Fatalf("CreateFirewallGroup: %v", err)
	}
	gname := "g2"
	if _, err := network.UpdateFirewallGroup(ctx, sc, fwID, groups.UpdateOpts{Name: &gname}); err != nil {
		t.Fatalf("UpdateFirewallGroup: %v", err)
	}
	if err := network.DeleteFirewallGroup(ctx, sc, fwID); err != nil {
		t.Fatalf("DeleteFirewallGroup: %v", err)
	}
	if all, err := network.ListFirewallPolicies(ctx, sc, nil); err != nil || len(all) != 1 || all[0].Rules[0] != "r1" {
		t.Fatalf("ListFirewallPolicies = %+v, %v", all, err)
	}
	if got, err := network.GetFirewallPolicy(ctx, sc, fwID); err != nil || got.Name != "p1" {
		t.Fatalf("GetFirewallPolicy = %+v, %v", got, err)
	}
	if _, err := network.CreateFirewallPolicy(ctx, sc, policies.CreateOpts{Name: "p1"}); err != nil {
		t.Fatalf("CreateFirewallPolicy: %v", err)
	}
	pname := "p2"
	if _, err := network.UpdateFirewallPolicy(ctx, sc, fwID, policies.UpdateOpts{Name: &pname}); err != nil {
		t.Fatalf("UpdateFirewallPolicy: %v", err)
	}
	if err := network.DeleteFirewallPolicy(ctx, sc, fwID); err != nil {
		t.Fatalf("DeleteFirewallPolicy: %v", err)
	}
	if err := network.InsertFirewallPolicyRule(ctx, sc, fwID, policies.InsertRuleOpts{ID: "r2", InsertBefore: "r1"}); err != nil {
		t.Fatalf("InsertFirewallPolicyRule: %v", err)
	}
	if err := network.RemoveFirewallPolicyRule(ctx, sc, fwID, "r2"); err != nil {
		t.Fatalf("RemoveFirewallPolicyRule: %v", err)
	}
	if all, err := network.ListFirewallRules(ctx, sc, nil); err != nil || len(all) != 1 || all[0].SourceFirewallGroupID != "sg" {
		t.Fatalf("ListFirewallRules = %+v, %v", all, err)
	}
	if got, err := network.GetFirewallRule(ctx, sc, fwID); err != nil || got.DestinationFirewallGroupID != "dg" || got.Action != "allow" {
		t.Fatalf("GetFirewallRule = %+v, %v", got, err)
	}
	if _, err := network.CreateFirewallRule(ctx, sc, rules.CreateOpts{Protocol: rules.ProtocolTCP, Action: rules.ActionAllow}); err != nil {
		t.Fatalf("CreateFirewallRule: %v", err)
	}
	rname := "r2"
	if _, err := network.UpdateFirewallRule(ctx, sc, fwID, rules.UpdateOpts{Name: &rname}); err != nil {
		t.Fatalf("UpdateFirewallRule: %v", err)
	}
	if err := network.DeleteFirewallRule(ctx, sc, fwID); err != nil {
		t.Fatalf("DeleteFirewallRule: %v", err)
	}
	if _, err := network.GetFirewallRule(ctx, sc, "bad"); err == nil || !strings.HasPrefix(err.Error(), "parsing firewall rule: ") || errors.Unwrap(err) == nil {
		t.Errorf("GetFirewallRule(bad body) = %v", err)
	}
	assertRequests(t, *seen, []req{
		{"GET", g, "", "", ""},
		{"GET", g + "/" + fwID, "", "", ""},
		{"POST", g, "", "", `{"firewall_group":{"name":"g1"}}`},
		{"PUT", g + "/" + fwID, "", "", `{"firewall_group":{"name":"g2"}}`},
		{"DELETE", g + "/" + fwID, "", "", ""},
		{"GET", p, "", "", ""},
		{"GET", p + "/" + fwID, "", "", ""},
		{"POST", p, "", "", `{"firewall_policy":{"name":"p1"}}`},
		{"PUT", p + "/" + fwID, "", "", `{"firewall_policy":{"name":"p2"}}`},
		{"DELETE", p + "/" + fwID, "", "", ""},
		{"PUT", p + "/" + fwID + "/insert_rule", "", "", `{"firewall_rule_id":"r2","insert_before":"r1"}`},
		{"PUT", p + "/" + fwID + "/remove_rule", "", "", `{"firewall_rule_id":"r2"}`},
		{"GET", r, "", "", ""},
		{"GET", r + "/" + fwID, "", "", ""},
		{"POST", r, "", "", `{"firewall_rule":{"action":"allow","protocol":"tcp"}}`},
		{"PUT", r + "/" + fwID, "", "", `{"firewall_rule":{"name":"r2"}}`},
		{"DELETE", r + "/" + fwID, "", "", ""},
		{"GET", r + "/bad", "", "", ""},
	})
	_, gone := fakeNeutron(t, nil)
	is404(t, second(network.ListFirewallGroups(ctx, gone, nil)), "listing firewall groups: ")
	is404(t, second(network.GetFirewallGroup(ctx, gone, "f")), "getting firewall group f: ")
	is404(t, second(network.CreateFirewallGroup(ctx, gone, groups.CreateOpts{})), "creating firewall group: ")
	is404(t, second(network.UpdateFirewallGroup(ctx, gone, "f", groups.UpdateOpts{})), "updating firewall group f: ")
	is404(t, network.DeleteFirewallGroup(ctx, gone, "f"), "deleting firewall group f: ")
	is404(t, second(network.ListFirewallPolicies(ctx, gone, nil)), "listing firewall policies: ")
	is404(t, second(network.GetFirewallPolicy(ctx, gone, "f")), "getting firewall policy f: ")
	is404(t, second(network.CreateFirewallPolicy(ctx, gone, policies.CreateOpts{})), "creating firewall policy: ")
	is404(t, second(network.UpdateFirewallPolicy(ctx, gone, "f", policies.UpdateOpts{})), "updating firewall policy f: ")
	is404(t, network.DeleteFirewallPolicy(ctx, gone, "f"), "deleting firewall policy f: ")
	is404(t, network.InsertFirewallPolicyRule(ctx, gone, "f", policies.InsertRuleOpts{ID: "r", InsertAfter: "a"}), "inserting firewall rule into firewall policy f: ")
	is404(t, network.RemoveFirewallPolicyRule(ctx, gone, "f", "r"), "removing firewall rule r from firewall policy f: ")
	is404(t, second(network.ListFirewallRules(ctx, gone, nil)), "listing firewall rules: ")
	is404(t, second(network.GetFirewallRule(ctx, gone, "f")), "getting firewall rule f: ")
	is404(t, second(network.CreateFirewallRule(ctx, gone, rules.CreateOpts{Protocol: rules.ProtocolAny, Action: rules.ActionDeny})), "creating firewall rule: ")
	is404(t, second(network.UpdateFirewallRule(ctx, gone, "f", rules.UpdateOpts{})), "updating firewall rule f: ")
	is404(t, network.DeleteFirewallRule(ctx, gone, "f"), "deleting firewall rule f: ")
}

func TestVPNaaS(t *testing.T) {
	ctx := context.Background()
	runVPNNoun(t, "VPN service", "VPN services", "/vpn/vpnservices", "vpnservice", `"name":"s1","subnet_id":"`+subnetID+`"`,
		func(cl *clientRef) error {
			_, err := network.ListVPNServices(ctx, cl.sc, services.ListOpts{Name: "s1"})
			return err
		},
		func(cl *clientRef) error { _, err := network.GetVPNService(ctx, cl.sc, vpnObjID); return err },
		func(cl *clientRef) error {
			_, err := network.CreateVPNService(ctx, cl.sc, services.CreateOpts{Name: "s1", RouterID: routerID})
			return err
		},
		func(cl *clientRef) error {
			n := "s2"
			_, err := network.UpdateVPNService(ctx, cl.sc, vpnObjID, services.UpdateOpts{Name: &n})
			return err
		},
		func(cl *clientRef) error { return network.DeleteVPNService(ctx, cl.sc, vpnObjID) },
		`{"vpnservice":{"admin_state_up":null,"name":"s1","router_id":"`+routerID+`"}}`,
		`{"vpnservice":{"name":"s2"}}`)
	runVPNNoun(t, "IKE policy", "IKE policies", "/vpn/ikepolicies", "ikepolicy", `"name":"i1","ike_version":"v2"`,
		func(cl *clientRef) error {
			_, err := network.ListIKEPolicies(ctx, cl.sc, ikepolicies.ListOpts{Name: "i1"})
			return err
		},
		func(cl *clientRef) error { _, err := network.GetIKEPolicy(ctx, cl.sc, vpnObjID); return err },
		func(cl *clientRef) error {
			_, err := network.CreateIKEPolicy(ctx, cl.sc, ikepolicies.CreateOpts{Name: "i1"})
			return err
		},
		func(cl *clientRef) error {
			n := "i2"
			_, err := network.UpdateIKEPolicy(ctx, cl.sc, vpnObjID, ikepolicies.UpdateOpts{Name: &n})
			return err
		},
		func(cl *clientRef) error { return network.DeleteIKEPolicy(ctx, cl.sc, vpnObjID) },
		`{"ikepolicy":{"name":"i1"}}`,
		`{"ikepolicy":{"name":"i2"}}`)
	runVPNNoun(t, "IPsec policy", "IPsec policies", "/vpn/ipsecpolicies", "ipsecpolicy", `"name":"p1","pfs":"group14"`,
		func(cl *clientRef) error {
			_, err := network.ListIPsecPolicies(ctx, cl.sc, ipsecpolicies.ListOpts{Name: "p1"})
			return err
		},
		func(cl *clientRef) error { _, err := network.GetIPsecPolicy(ctx, cl.sc, vpnObjID); return err },
		func(cl *clientRef) error {
			_, err := network.CreateIPsecPolicy(ctx, cl.sc, ipsecpolicies.CreateOpts{Name: "p1"})
			return err
		},
		func(cl *clientRef) error {
			n := "p2"
			_, err := network.UpdateIPsecPolicy(ctx, cl.sc, vpnObjID, ipsecpolicies.UpdateOpts{Name: &n})
			return err
		},
		func(cl *clientRef) error { return network.DeleteIPsecPolicy(ctx, cl.sc, vpnObjID) },
		`{"ipsecpolicy":{"name":"p1"}}`,
		`{"ipsecpolicy":{"name":"p2"}}`)
	runVPNNoun(t, "IPsec site connection", "IPsec site connections", "/vpn/ipsec-site-connections", "ipsec_site_connection", `"name":"c1","peer_address":"192.0.2.9"`,
		func(cl *clientRef) error {
			_, err := network.ListIPsecSiteConnections(ctx, cl.sc, siteconnections.ListOpts{Name: "c1"})
			return err
		},
		func(cl *clientRef) error { _, err := network.GetIPsecSiteConnection(ctx, cl.sc, vpnObjID); return err },
		func(cl *clientRef) error {
			_, err := network.CreateIPsecSiteConnection(ctx, cl.sc, siteconnections.CreateOpts{Name: "c1", PSK: "x"})
			return err
		},
		func(cl *clientRef) error {
			n := "c2"
			_, err := network.UpdateIPsecSiteConnection(ctx, cl.sc, vpnObjID, siteconnections.UpdateOpts{Name: &n})
			return err
		},
		func(cl *clientRef) error { return network.DeleteIPsecSiteConnection(ctx, cl.sc, vpnObjID) },
		`{"ipsec_site_connection":{"ikepolicy_id":"","ipsecpolicy_id":"","name":"c1","peer_address":"","peer_id":"","psk":"x","vpnservice_id":""}}`,
		`{"ipsec_site_connection":{"name":"c2"}}`)
	runVPNNoun(t, "endpoint group", "endpoint groups", "/vpn/endpoint-groups", "endpoint_group", `"name":"e1","type":"cidr","endpoints":["198.51.100.0/24"]`,
		func(cl *clientRef) error {
			_, err := network.ListVPNEndpointGroups(ctx, cl.sc, endpointgroups.ListOpts{Name: "e1"})
			return err
		},
		func(cl *clientRef) error { _, err := network.GetVPNEndpointGroup(ctx, cl.sc, vpnObjID); return err },
		func(cl *clientRef) error {
			_, err := network.CreateVPNEndpointGroup(ctx, cl.sc, endpointgroups.CreateOpts{Name: "e1", Type: endpointgroups.TypeCIDR, Endpoints: []string{"198.51.100.0/24"}})
			return err
		},
		func(cl *clientRef) error {
			n := "e2"
			_, err := network.UpdateVPNEndpointGroup(ctx, cl.sc, vpnObjID, endpointgroups.UpdateOpts{Name: &n})
			return err
		},
		func(cl *clientRef) error { return network.DeleteVPNEndpointGroup(ctx, cl.sc, vpnObjID) },
		`{"endpoint_group":{"endpoints":["198.51.100.0/24"],"name":"e1","type":"cidr"}}`,
		`{"endpoint_group":{"name":"e2"}}`)
}

// clientRef lets one noun's calls run against the recording, the 404 and the
// empty-body fakes in turn.
type clientRef struct{ sc *gophercloud.ServiceClient }

// runVPNNoun drives one VPNaaS noun through list, get, create, update and
// delete, then the same against a fake that 404s and one whose get answers
// without its resource key.
func runVPNNoun(t *testing.T, kind, plural, path, key, fields string,
	list, get, create, update, del func(*clientRef) error, createBody, updateBody string,
) {
	t.Helper()
	t.Run(kind, func(t *testing.T) {
		obj := `{"id":"` + vpnObjID + `",` + fields + `}`
		coll := strings.ReplaceAll(path[strings.LastIndex(path, "/")+1:], "-", "_")
		item := path + "/" + vpnObjID
		seen, sc := fakeNeutron(t, map[string]reply{
			"GET " + path:    {Body: `{"` + coll + `":[` + obj + `]}`},
			"GET " + item:    {Body: `{"` + key + `":` + obj + `}`},
			"POST " + path:   {Status: http.StatusCreated, Body: `{"` + key + `":` + obj + `}`},
			"PUT " + item:    {Body: `{"` + key + `":` + obj + `}`},
			"DELETE " + item: {Status: http.StatusNoContent},
		})
		cl := &clientRef{sc}
		for _, f := range []func(*clientRef) error{list, get, create, update, del} {
			if err := f(cl); err != nil {
				t.Fatalf("%s: %v", kind, err)
			}
		}
		assertRequests(t, *seen, []req{
			{"GET", path, "name=" + strings.SplitN(strings.TrimPrefix(fields, `"name":"`), `"`, 2)[0], "", ""},
			{"GET", item, "", "", ""},
			{"POST", path, "", "", createBody},
			{"PUT", item, "", "", updateBody},
			{"DELETE", item, "", "", ""},
		})
		_, cl.sc = fakeNeutron(t, nil)
		is404(t, list(cl), "listing "+plural+": ")
		is404(t, get(cl), "getting "+kind+" "+vpnObjID+": ")
		is404(t, create(cl), "creating "+kind+": ")
		is404(t, update(cl), "updating "+kind+" "+vpnObjID+": ")
		is404(t, del(cl), "deleting "+kind+" "+vpnObjID+": ")
		_, cl.sc = fakeNeutron(t, map[string]reply{"GET " + item: {Body: `{}`}})
		if err := get(cl); err == nil || err.Error() != "getting "+kind+" "+vpnObjID+": the response carried no resource" {
			t.Errorf("get without %q = %v", key, err)
		}
	})
}

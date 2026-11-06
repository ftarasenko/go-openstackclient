package network_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/agents"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/bgp/peers"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/bgp/speakers"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/taas/tapmirrors"

	"github.com/ftarasenko/koc/pkg/openstack/network"
)

const (
	agentID   = "8c9d0e1f-2a3b-4c4d-9e5f-7a8b9c0d1e2f"
	speakerID = "9d0e1f2a-3b4c-4d5e-8f6a-8b9c0d1e2f3a"
	peerID    = "0e1f2a3b-4c5d-4e6f-9a7b-9c0d1e2f3a4b"
	mirrorID  = "1f2a3b4c-5d6e-4f7a-8b8c-0d1e2f3a4b5c"
)

func TestAgents(t *testing.T) {
	ag := `{"id":"` + agentID + `","agent_type":"L3 agent","host":"net-1","alive":true,"admin_state_up":true}`
	agPath := "/agents/" + agentID
	seen, sc := fakeNeutron(t, map[string]reply{
		"GET /agents":      {Body: `{"agents":[` + ag + `]}`},
		"GET " + agPath:    {Body: `{"agent":` + ag + `}`},
		"PUT " + agPath:    {Body: `{"agent":` + ag + `}`},
		"DELETE " + agPath: {Status: http.StatusNoContent},
		"GET /networks/" + netID + "/dhcp-agents":      {Body: `{"agents":[` + ag + `]}`},
		"GET /routers/" + routerID + "/l3-agents":      {Body: `{"agents":[{"id":"` + agentID + `","ha_state":"active"}]}`},
		"POST " + agPath + "/dhcp-networks":            {Status: http.StatusCreated},
		"DELETE " + agPath + "/dhcp-networks/" + netID: {Status: http.StatusNoContent},
		"POST " + agPath + "/l3-routers":               {Status: http.StatusCreated},
		"DELETE " + agPath + "/l3-routers/" + routerID: {Status: http.StatusNoContent},
		"PUT " + agPath + "/l3-routers/" + routerID:    {Status: http.StatusNoContent},
		"GET /service-providers":                       {Body: `{"service_providers":[{"service_type":"L3_ROUTER_NAT","name":"ovn","default":true}]}`},
	})
	ctx := context.Background()
	if all, err := network.ListAgents(ctx, sc, agents.ListOpts{AgentType: "L3 agent", Host: "net-1"}); err != nil || len(all) != 1 || !all[0].Alive {
		t.Fatalf("ListAgents = %+v, %v", all, err)
	}
	if got, err := network.GetAgent(ctx, sc, agentID); err != nil || got.Host != "net-1" {
		t.Fatalf("GetAgent = %+v, %v", got, err)
	}
	up := false
	if _, err := network.UpdateAgent(ctx, sc, agentID, agents.UpdateOpts{AdminStateUp: &up}); err != nil {
		t.Fatalf("UpdateAgent: %v", err)
	}
	if err := network.DeleteAgent(ctx, sc, agentID); err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}
	if all, err := network.ListNetworkDHCPAgents(ctx, sc, netID); err != nil || len(all) != 1 || all[0].ID != agentID {
		t.Fatalf("ListNetworkDHCPAgents = %+v, %v", all, err)
	}
	if all, err := network.ListRouterL3Agents(ctx, sc, routerID); err != nil || len(all) != 1 || all[0].HAState != "active" {
		t.Fatalf("ListRouterL3Agents = %+v, %v", all, err)
	}
	if err := network.AddDHCPAgentNetwork(ctx, sc, agentID, netID); err != nil {
		t.Fatalf("AddDHCPAgentNetwork: %v", err)
	}
	if err := network.RemoveDHCPAgentNetwork(ctx, sc, agentID, netID); err != nil {
		t.Fatalf("RemoveDHCPAgentNetwork: %v", err)
	}
	if err := network.AddL3AgentRouter(ctx, sc, agentID, network.L3AgentRouterOpts{RouterID: routerID}); err != nil {
		t.Fatalf("AddL3AgentRouter: %v", err)
	}
	prio := 0
	if err := network.AddL3AgentRouter(ctx, sc, agentID, network.L3AgentRouterOpts{RouterID: routerID, HAChassisPriority: &prio}); err != nil {
		t.Fatalf("AddL3AgentRouter(priority 0): %v", err)
	}
	if err := network.RemoveL3AgentRouter(ctx, sc, agentID, routerID); err != nil {
		t.Fatalf("RemoveL3AgentRouter: %v", err)
	}
	if err := network.SetL3AgentRouterPriority(ctx, sc, agentID, routerID, 5); err != nil {
		t.Fatalf("SetL3AgentRouterPriority: %v", err)
	}
	if all, err := network.ListServiceProviders(ctx, sc); err != nil || len(all) != 1 || !all[0].Default || all[0].Name != "ovn" {
		t.Fatalf("ListServiceProviders = %+v, %v", all, err)
	}
	assertRequests(t, *seen, []req{
		{"GET", "/agents", "agent_type=L3+agent&host=net-1", "", ""},
		{"GET", agPath, "", "", ""},
		{"PUT", agPath, "", "", `{"agent":{"admin_state_up":false}}`},
		{"DELETE", agPath, "", "", ""},
		{"GET", "/networks/" + netID + "/dhcp-agents", "", "", ""},
		{"GET", "/routers/" + routerID + "/l3-agents", "", "", ""},
		{"POST", agPath + "/dhcp-networks", "", "", `{"network_id":"` + netID + `"}`},
		{"DELETE", agPath + "/dhcp-networks/" + netID, "", "", ""},
		{"POST", agPath + "/l3-routers", "", "", `{"router_id":"` + routerID + `"}`},
		{"POST", agPath + "/l3-routers", "", "", `{"ha_chassis_priority":0,"router_id":"` + routerID + `"}`},
		{"DELETE", agPath + "/l3-routers/" + routerID, "", "", ""},
		{"PUT", agPath + "/l3-routers/" + routerID, "", "", `{"ha_chassis_priority":5}`},
		{"GET", "/service-providers", "", "", ""},
	})
	_, gone := fakeNeutron(t, nil)
	is404(t, second(network.ListAgents(ctx, gone, agents.ListOpts{})), "listing network agents: ")
	is404(t, second(network.GetAgent(ctx, gone, "a")), "getting network agent a: ")
	is404(t, second(network.UpdateAgent(ctx, gone, "a", agents.UpdateOpts{})), "updating network agent a: ")
	is404(t, network.DeleteAgent(ctx, gone, "a"), "deleting network agent a: ")
	is404(t, second(network.ListNetworkDHCPAgents(ctx, gone, "n")), "listing DHCP agents hosting network n: ")
	is404(t, second(network.ListRouterL3Agents(ctx, gone, "r")), "listing L3 agents hosting router r: ")
	is404(t, network.AddDHCPAgentNetwork(ctx, gone, "a", "n"), "adding network n to DHCP agent a: ")
	is404(t, network.RemoveDHCPAgentNetwork(ctx, gone, "a", "n"), "removing network n from DHCP agent a: ")
	is404(t, network.AddL3AgentRouter(ctx, gone, "a", network.L3AgentRouterOpts{RouterID: "r"}), "adding router r to L3 agent a: ")
	is404(t, network.RemoveL3AgentRouter(ctx, gone, "a", "r"), "removing router r from L3 agent a: ")
	is404(t, network.SetL3AgentRouterPriority(ctx, gone, "a", "r", 1), "setting router r on L3 agent a: ")
	is404(t, second(network.ListServiceProviders(ctx, gone)), "listing service providers: ")
}

func TestBGP(t *testing.T) {
	sp := `{"id":"` + speakerID + `","name":"s1","local_as":64512,"ip_version":4,"peers":[],"networks":[]}`
	pr := `{"id":"` + peerID + `","name":"p1","peer_ip":"192.0.2.1","remote_as":64513,"auth_type":"none"}`
	ag := `{"id":"` + agentID + `","agent_type":"BGP dynamic routing agent","host":"net-1"}`
	spPath := "/bgp-speakers/" + speakerID
	drPath := "/agents/" + agentID + "/bgp-drinstances"
	seen, sc := fakeNeutron(t, map[string]reply{
		"GET /bgp-speakers":                         {Body: `{"bgp_speakers":[` + sp + `]}`},
		"GET " + drPath:                             {Body: `{"bgp_speakers":[` + sp + `]}`},
		"GET " + spPath:                             {Body: `{"bgp_speaker":` + sp + `}`},
		"POST /bgp-speakers":                        {Status: http.StatusCreated, Body: `{"bgp_speaker":` + sp + `}`},
		"PUT " + spPath:                             {Body: `{"bgp_speaker":` + sp + `}`},
		"DELETE " + spPath:                          {Status: http.StatusNoContent},
		"PUT " + spPath + "/add_gateway_network":    {Body: `{"network_id":"` + netID + `"}`},
		"PUT " + spPath + "/remove_gateway_network": {},
		"PUT " + spPath + "/add_bgp_peer":           {Body: `{"bgp_peer_id":"` + peerID + `"}`},
		"PUT " + spPath + "/remove_bgp_peer":        {},
		"GET " + spPath + "/get_advertised_routes":  {Body: `{"advertised_routes":[{"destination":"198.51.100.0/24","next_hop":"192.0.2.10"}]}`},
		"GET /bgp-peers":                            {Body: `{"bgp_peers":[` + pr + `]}`},
		"GET /bgp-peers/" + peerID:                  {Body: `{"bgp_peer":` + pr + `}`},
		"POST /bgp-peers":                           {Status: http.StatusCreated, Body: `{"bgp_peer":` + pr + `}`},
		"PUT /bgp-peers/" + peerID:                  {Body: `{"bgp_peer":` + pr + `}`},
		"DELETE /bgp-peers/" + peerID:               {Status: http.StatusNoContent},
		"POST " + drPath:                            {Status: http.StatusCreated},
		"DELETE " + drPath + "/" + speakerID:        {Status: http.StatusNoContent},
		"GET " + spPath + "/bgp-dragents":           {Body: `{"agents":[` + ag + `]}`},
	})
	ctx := context.Background()
	if all, err := network.ListBGPSpeakers(ctx, sc); err != nil || len(all) != 1 || all[0].LocalAS != 64512 {
		t.Fatalf("ListBGPSpeakers = %+v, %v", all, err)
	}
	if all, err := network.ListDRAgentBGPSpeakers(ctx, sc, agentID); err != nil || len(all) != 1 {
		t.Fatalf("ListDRAgentBGPSpeakers = %+v, %v", all, err)
	}
	if got, err := network.GetBGPSpeaker(ctx, sc, speakerID); err != nil || got.Name != "s1" {
		t.Fatalf("GetBGPSpeaker = %+v, %v", got, err)
	}
	if _, err := network.CreateBGPSpeaker(ctx, sc, speakers.CreateOpts{Name: "s1", LocalAS: "64512", IPVersion: 4}); err != nil {
		t.Fatalf("CreateBGPSpeaker: %v", err)
	}
	if _, err := network.UpdateBGPSpeaker(ctx, sc, speakerID, speakers.UpdateOpts{Name: "s2"}); err != nil {
		t.Fatalf("UpdateBGPSpeaker: %v", err)
	}
	if err := network.DeleteBGPSpeaker(ctx, sc, speakerID); err != nil {
		t.Fatalf("DeleteBGPSpeaker: %v", err)
	}
	if err := network.AddBGPSpeakerGatewayNetwork(ctx, sc, speakerID, netID); err != nil {
		t.Fatalf("AddBGPSpeakerGatewayNetwork: %v", err)
	}
	if err := network.RemoveBGPSpeakerGatewayNetwork(ctx, sc, speakerID, netID); err != nil {
		t.Fatalf("RemoveBGPSpeakerGatewayNetwork: %v", err)
	}
	if err := network.AddBGPSpeakerPeer(ctx, sc, speakerID, peerID); err != nil {
		t.Fatalf("AddBGPSpeakerPeer: %v", err)
	}
	if err := network.RemoveBGPSpeakerPeer(ctx, sc, speakerID, peerID); err != nil {
		t.Fatalf("RemoveBGPSpeakerPeer: %v", err)
	}
	if routes, err := network.ListBGPSpeakerAdvertisedRoutes(ctx, sc, speakerID); err != nil || len(routes) != 1 || routes[0].NextHop != "192.0.2.10" {
		t.Fatalf("ListBGPSpeakerAdvertisedRoutes = %+v, %v", routes, err)
	}
	if all, err := network.ListBGPPeers(ctx, sc); err != nil || len(all) != 1 || all[0].RemoteAS != 64513 {
		t.Fatalf("ListBGPPeers = %+v, %v", all, err)
	}
	if got, err := network.GetBGPPeer(ctx, sc, peerID); err != nil || got.PeerIP != "192.0.2.1" {
		t.Fatalf("GetBGPPeer = %+v, %v", got, err)
	}
	if _, err := network.CreateBGPPeer(ctx, sc, peers.CreateOpts{Name: "p1", PeerIP: "192.0.2.1", RemoteAS: 64513, AuthType: "none"}); err != nil {
		t.Fatalf("CreateBGPPeer: %v", err)
	}
	if _, err := network.UpdateBGPPeer(ctx, sc, peerID, peers.UpdateOpts{Name: "p2"}); err != nil {
		t.Fatalf("UpdateBGPPeer: %v", err)
	}
	if err := network.DeleteBGPPeer(ctx, sc, peerID); err != nil {
		t.Fatalf("DeleteBGPPeer: %v", err)
	}
	if err := network.AddDRAgentBGPSpeaker(ctx, sc, agentID, speakerID); err != nil {
		t.Fatalf("AddDRAgentBGPSpeaker: %v", err)
	}
	if err := network.RemoveDRAgentBGPSpeaker(ctx, sc, agentID, speakerID); err != nil {
		t.Fatalf("RemoveDRAgentBGPSpeaker: %v", err)
	}
	if all, err := network.ListBGPSpeakerDRAgents(ctx, sc, speakerID); err != nil || len(all) != 1 || all[0].ID != agentID {
		t.Fatalf("ListBGPSpeakerDRAgents = %+v, %v", all, err)
	}
	assertRequests(t, *seen, []req{
		{"GET", "/bgp-speakers", "", "", ""},
		{"GET", drPath, "", "", ""},
		{"GET", spPath, "", "", ""},
		{"POST", "/bgp-speakers", "", "", `{"bgp_speaker":{"advertise_floating_ip_host_routes":false,"advertise_tenant_networks":false,"ip_version":4,"local_as":"64512","name":"s1"}}`},
		{"PUT", spPath, "", "", `{"bgp_speaker":{"advertise_floating_ip_host_routes":false,"advertise_tenant_networks":false,"name":"s2"}}`},
		{"DELETE", spPath, "", "", ""},
		{"PUT", spPath + "/add_gateway_network", "", "", `{"network_id":"` + netID + `"}`},
		{"PUT", spPath + "/remove_gateway_network", "", "", `{"network_id":"` + netID + `"}`},
		{"PUT", spPath + "/add_bgp_peer", "", "", `{"bgp_peer_id":"` + peerID + `"}`},
		{"PUT", spPath + "/remove_bgp_peer", "", "", `{"bgp_peer_id":"` + peerID + `"}`},
		{"GET", spPath + "/get_advertised_routes", "", "", ""},
		{"GET", "/bgp-peers", "", "", ""},
		{"GET", "/bgp-peers/" + peerID, "", "", ""},
		{"POST", "/bgp-peers", "", "", `{"bgp_peer":{"auth_type":"none","name":"p1","peer_ip":"192.0.2.1","remote_as":64513}}`},
		{"PUT", "/bgp-peers/" + peerID, "", "", `{"bgp_peer":{"name":"p2"}}`},
		{"DELETE", "/bgp-peers/" + peerID, "", "", ""},
		{"POST", drPath, "", "", `{"bgp_speaker_id":"` + speakerID + `"}`},
		{"DELETE", drPath + "/" + speakerID, "", "", ""},
		{"GET", spPath + "/bgp-dragents", "", "", ""},
	})
	_, gone := fakeNeutron(t, nil)
	is404(t, second(network.ListBGPSpeakers(ctx, gone)), "listing BGP speakers: ")
	is404(t, second(network.ListDRAgentBGPSpeakers(ctx, gone, "a")), "listing BGP speakers hosted by agent a: ")
	is404(t, second(network.GetBGPSpeaker(ctx, gone, "s")), "getting BGP speaker s: ")
	is404(t, second(network.CreateBGPSpeaker(ctx, gone, speakers.CreateOpts{})), "creating BGP speaker: ")
	is404(t, second(network.UpdateBGPSpeaker(ctx, gone, "s", speakers.UpdateOpts{})), "updating BGP speaker s: ")
	is404(t, network.DeleteBGPSpeaker(ctx, gone, "s"), "deleting BGP speaker s: ")
	is404(t, network.AddBGPSpeakerGatewayNetwork(ctx, gone, "s", "n"), "adding network n to BGP speaker s: ")
	is404(t, network.RemoveBGPSpeakerGatewayNetwork(ctx, gone, "s", "n"), "removing network n from BGP speaker s: ")
	is404(t, network.AddBGPSpeakerPeer(ctx, gone, "s", "p"), "adding BGP peer p to BGP speaker s: ")
	is404(t, network.RemoveBGPSpeakerPeer(ctx, gone, "s", "p"), "removing BGP peer p from BGP speaker s: ")
	is404(t, second(network.ListBGPSpeakerAdvertisedRoutes(ctx, gone, "s")), "listing routes advertised by BGP speaker s: ")
	is404(t, second(network.ListBGPPeers(ctx, gone)), "listing BGP peers: ")
	is404(t, second(network.GetBGPPeer(ctx, gone, "p")), "getting BGP peer p: ")
	is404(t, second(network.CreateBGPPeer(ctx, gone, peers.CreateOpts{})), "creating BGP peer: ")
	is404(t, second(network.UpdateBGPPeer(ctx, gone, "p", peers.UpdateOpts{})), "updating BGP peer p: ")
	is404(t, network.DeleteBGPPeer(ctx, gone, "p"), "deleting BGP peer p: ")
	is404(t, network.AddDRAgentBGPSpeaker(ctx, gone, "a", "s"), "adding BGP speaker s to dynamic routing agent a: ")
	is404(t, network.RemoveDRAgentBGPSpeaker(ctx, gone, "a", "s"), "removing BGP speaker s from dynamic routing agent a: ")
	is404(t, second(network.ListBGPSpeakerDRAgents(ctx, gone, "s")), "listing dynamic routing agents hosting BGP speaker s: ")
}

// mirrorBody is a tap-mirror create builder of the caller's own, with the
// tunnel IDs as numbers, which tapmirrors.CreateOpts cannot send.
type mirrorBody map[string]any

func (b mirrorBody) ToTapMirrorCreateMap() (map[string]any, error) {
	return map[string]any{"tap_mirror": map[string]any(b)}, nil
}

func TestTapMirrors(t *testing.T) {
	// neutron echoes the tunnel IDs as it was sent them: here as numbers,
	// which gophercloud's tapmirrors.TapMirror cannot decode.
	m := `{"id":"` + mirrorID + `","name":"m1","port_id":"` + portID + `","directions":{"IN":101,"OUT":"102"},` +
		`"remote_ip":"192.0.2.20","mirror_type":"erspanv1","tenant_id":"t","project_id":"t"}`
	path := "/taas/tap_mirrors/" + mirrorID
	seen, sc := fakeNeutron(t, map[string]reply{
		"GET /taas/tap_mirrors":  {Body: `{"tap_mirrors":[` + m + `]}`},
		"GET " + path:            {Body: `{"tap_mirror":` + m + `}`},
		"POST /taas/tap_mirrors": {Status: http.StatusCreated, Body: `{"tap_mirror":` + m + `}`},
		"PUT " + path:            {Body: `{"tap_mirror":` + m + `}`},
		"DELETE " + path:         {Status: http.StatusNoContent},
	})
	ctx := context.Background()
	all, err := network.ListTapMirrors(ctx, sc, tapmirrors.ListOpts{ProjectID: "t"})
	if err != nil || len(all) != 1 || all[0].Directions["IN"] != float64(101) || all[0].Directions["OUT"] != "102" {
		t.Fatalf("ListTapMirrors = %+v, %v", all, err)
	}
	if all, err := network.ListTapMirrors(ctx, sc, nil); err != nil || len(all) != 1 {
		t.Fatalf("ListTapMirrors(nil) = %+v, %v", all, err)
	}
	if got, err := network.GetTapMirror(ctx, sc, mirrorID); err != nil || got.MirrorType != "erspanv1" || got.TenantID != "t" {
		t.Fatalf("GetTapMirror = %+v, %v", got, err)
	}
	if _, err := network.CreateTapMirror(ctx, sc, mirrorBody{"port_id": portID, "directions": map[string]int{"IN": 101}}); err != nil {
		t.Fatalf("CreateTapMirror: %v", err)
	}
	name := "m2"
	if _, err := network.UpdateTapMirror(ctx, sc, mirrorID, tapmirrors.UpdateOpts{Name: &name}); err != nil {
		t.Fatalf("UpdateTapMirror: %v", err)
	}
	if err := network.DeleteTapMirror(ctx, sc, mirrorID); err != nil {
		t.Fatalf("DeleteTapMirror: %v", err)
	}
	assertRequests(t, *seen, []req{
		{"GET", "/taas/tap_mirrors", "project_id=t", "", ""},
		{"GET", "/taas/tap_mirrors", "", "", ""},
		{"GET", path, "", "", ""},
		{"POST", "/taas/tap_mirrors", "", "", `{"tap_mirror":{"directions":{"IN":101},"port_id":"` + portID + `"}}`},
		{"PUT", path, "", "", `{"tap_mirror":{"name":"m2"}}`},
		{"DELETE", path, "", "", ""},
	})
	// A body with no tap_mirror is an error, not a zero mirror.
	_, bare := fakeNeutron(t, map[string]reply{"GET " + path: {Body: `{}`}})
	if _, err := network.GetTapMirror(ctx, bare, mirrorID); err == nil {
		t.Fatal("GetTapMirror({}) = nil error")
	}
	_, gone := fakeNeutron(t, nil)
	is404(t, second(network.ListTapMirrors(ctx, gone, tapmirrors.ListOpts{})), "listing tap mirrors: ")
	is404(t, second(network.GetTapMirror(ctx, gone, "m")), "getting tap mirror m: ")
	is404(t, second(network.CreateTapMirror(ctx, gone, mirrorBody{})), "creating tap mirror: ")
	is404(t, second(network.UpdateTapMirror(ctx, gone, "m", tapmirrors.UpdateOpts{})), "updating tap mirror m: ")
	is404(t, network.DeleteTapMirror(ctx, gone, "m"), "deleting tap mirror m: ")
}

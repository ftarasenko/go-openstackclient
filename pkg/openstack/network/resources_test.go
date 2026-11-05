package network_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/layer3/routers"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/networks"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/ports"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/subnets"

	"github.com/ftarasenko/go-openstackclient/pkg/openstack/network"
)

const (
	subnetID = "2c3d4e5f-6a7b-4c8d-9e0f-1a2b3c4d5e6f"
	portID   = "3d4e5f6a-7b8c-4d9e-8f0a-2b3c4d5e6f7a"
	routerID = "4e5f6a7b-8c9d-4e0f-9a1b-3c4d5e6f7a8b"
)

func TestNetworks(t *testing.T) {
	n := `{"id":"` + netID + `","name":"private","mtu":1450,"router:external":true,"provider:network_type":"vxlan",` +
		`"qos_policy_id":"qos-1","pvlan":true,"availability_zones":["nova"]}`
	seen, sc := fakeNeutron(t, map[string]reply{
		"GET /networks":                     {Body: `{"networks":[` + n + `]}`},
		"GET /agents/agent-1/dhcp-networks": {Body: `{"networks":[` + n + `]}`},
		"GET /networks/" + netID:            {Body: `{"network":` + n + `}`},
		"POST /networks":                    {Status: http.StatusCreated, Body: `{"network":` + n + `}`},
		"PUT /networks/" + netID:            {Body: `{"network":` + n + `}`},
		"DELETE /networks/" + netID:         {Status: http.StatusNoContent},
	})
	ctx := context.Background()
	all, err := network.ListNetworks(ctx, sc, networks.ListOpts{Name: "private"})
	if err != nil || len(all) != 1 || all[0].MTU != 1450 || !all[0].External || all[0].NetworkType != "vxlan" ||
		all[0].QoSPolicyID != "qos-1" || all[0].PVLAN == nil || !*all[0].PVLAN || all[0].AvailabilityZones[0] != "nova" {
		t.Fatalf("ListNetworks = %+v, %v", all, err)
	}
	if hosted, err := network.ListDHCPAgentNetworks(ctx, sc, "agent-1"); err != nil || len(hosted) != 1 || hosted[0].Name != "private" {
		t.Fatalf("ListDHCPAgentNetworks = %+v, %v", hosted, err)
	}
	if got, err := network.GetNetwork(ctx, sc, netID); err != nil || got.MTU != 1450 {
		t.Fatalf("GetNetwork = %+v, %v", got, err)
	}
	if _, err := network.CreateNetwork(ctx, sc, network.WithNetworkCreateAttrs(networks.CreateOpts{Name: "private"},
		map[string]any{"pvlan": true})); err != nil {
		t.Fatalf("CreateNetwork: %v", err)
	}
	name := "renamed"
	if _, err := network.UpdateNetwork(ctx, sc, netID, networks.UpdateOpts{Name: &name}); err != nil {
		t.Fatalf("UpdateNetwork: %v", err)
	}
	if err := network.DeleteNetwork(ctx, sc, netID); err != nil {
		t.Fatalf("DeleteNetwork: %v", err)
	}
	assertRequests(t, *seen, []req{
		{"GET", "/networks", "name=private", "", ""},
		{"GET", "/agents/agent-1/dhcp-networks", "", "", ""},
		{"GET", "/networks/" + netID, "", "", ""},
		{"POST", "/networks", "", "", `{"network":{"name":"private","pvlan":true}}`},
		{"PUT", "/networks/" + netID, "", "", `{"network":{"name":"renamed"}}`},
		{"DELETE", "/networks/" + netID, "", "", ""},
	})

	_, bad := fakeNeutron(t, map[string]reply{"GET /networks": {Body: `{"networks":[{"id":"n","mtu":"big"}]}`}})
	if _, err := network.ListNetworks(ctx, bad, networks.ListOpts{}); err == nil || !strings.HasPrefix(err.Error(), "parsing network list: ") {
		t.Errorf("malformed list: %v", err)
	}
	_, gone := fakeNeutron(t, nil)
	is404(t, second(network.ListNetworks(ctx, gone, networks.ListOpts{})), "listing networks: ")
	is404(t, second(network.ListDHCPAgentNetworks(ctx, gone, "a")), "listing networks hosted by DHCP agent a: ")
	is404(t, second(network.GetNetwork(ctx, gone, "n")), "getting network n: ")
	is404(t, second(network.CreateNetwork(ctx, gone, networks.CreateOpts{})), "creating network: ")
	is404(t, second(network.UpdateNetwork(ctx, gone, "n", networks.UpdateOpts{})), "updating network n: ")
	is404(t, network.DeleteNetwork(ctx, gone, "n"), "deleting network n: ")
}

func TestSubnets(t *testing.T) {
	s := `{"id":"` + subnetID + `","name":"sub1","network_id":"` + netID + `","cidr":"192.0.2.0/24","ip_version":4,` +
		`"leak_routes":true,"router:external":false,"revision_number":3}`
	seen, sc := fakeNeutron(t, map[string]reply{
		"GET /subnets":                {Body: `{"subnets":[` + s + `]}`},
		"GET /subnets/" + subnetID:    {Body: `{"subnet":` + s + `}`},
		"POST /subnets":               {Status: http.StatusCreated, Body: `{"subnet":` + s + `}`},
		"PUT /subnets/" + subnetID:    {Body: `{"subnet":` + s + `}`},
		"DELETE /subnets/" + subnetID: {Status: http.StatusNoContent},
	})
	ctx := context.Background()
	all, err := network.ListSubnets(ctx, sc, subnets.ListOpts{NetworkID: netID})
	if err != nil || len(all) != 1 || all[0].CIDR != "192.0.2.0/24" || all[0].LeakRoutes == nil || !*all[0].LeakRoutes ||
		all[0].RouterExternal == nil || *all[0].RouterExternal {
		t.Fatalf("ListSubnets = %+v, %v", all, err)
	}
	if got, err := network.GetSubnet(ctx, sc, subnetID); err != nil || got.RevisionNumber != 3 || got.LeakRoutes == nil {
		t.Fatalf("GetSubnet = %+v, %v", got, err)
	}
	if _, err := network.CreateSubnet(ctx, sc, network.WithSubnetCreateAttrs(
		subnets.CreateOpts{NetworkID: netID, CIDR: "192.0.2.0/24", IPVersion: 4}, map[string]any{"leak_routes": true})); err != nil {
		t.Fatalf("CreateSubnet: %v", err)
	}
	rev := 3
	if _, err := network.UpdateSubnet(ctx, sc, subnetID, network.WithSubnetUpdateAttrs(
		subnets.UpdateOpts{RevisionNumber: &rev}, map[string]any{"leak_routes": false})); err != nil {
		t.Fatalf("UpdateSubnet: %v", err)
	}
	if err := network.DeleteSubnet(ctx, sc, subnetID); err != nil {
		t.Fatalf("DeleteSubnet: %v", err)
	}
	assertRequests(t, *seen, []req{
		{"GET", "/subnets", "network_id=" + netID, "", ""},
		{"GET", "/subnets/" + subnetID, "", "", ""},
		{"POST", "/subnets", "", "", `{"subnet":{"cidr":"192.0.2.0/24","ip_version":4,"leak_routes":true,"network_id":"` + netID + `"}}`},
		{"PUT", "/subnets/" + subnetID, "", "revision_number=3", `{"subnet":{"leak_routes":false}}`},
		{"DELETE", "/subnets/" + subnetID, "", "", ""},
	})

	_, bad := fakeNeutron(t, map[string]reply{"GET /subnets": {Body: `{"subnets":[{"id":"s","leak_routes":"yes"}]}`}})
	if _, err := network.ListSubnets(ctx, bad, subnets.ListOpts{}); err == nil || !strings.HasPrefix(err.Error(), "parsing subnet list: ") {
		t.Errorf("malformed list: %v", err)
	}
	_, gone := fakeNeutron(t, nil)
	is404(t, second(network.ListSubnets(ctx, gone, subnets.ListOpts{})), "listing subnets: ")
	is404(t, second(network.GetSubnet(ctx, gone, "s")), "getting subnet s: ")
	is404(t, second(network.CreateSubnet(ctx, gone, subnets.CreateOpts{NetworkID: "n", IPVersion: 4, CIDR: "192.0.2.0/24"})), "creating subnet: ")
	is404(t, second(network.UpdateSubnet(ctx, gone, "s", subnets.UpdateOpts{})), "updating subnet s: ")
	is404(t, network.DeleteSubnet(ctx, gone, "s"), "deleting subnet s: ")
}

func TestPorts(t *testing.T) {
	p := `{"id":"` + portID + `","name":"p1","network_id":"` + netID + `","port_security_enabled":false,` +
		`"binding:vnic_type":"direct","binding:profile":{"k":"v"},"dns_name":"web","trusted":true,` +
		`"trunk_details":{"trunk_id":"trunk-1","sub_ports":[{"port_id":"sub-1","segmentation_id":100,"segmentation_type":"vlan","mac_address":"fa:16:3e:00:00:01"}]}}`
	seen, sc := fakeNeutron(t, map[string]reply{
		"GET /ports":              {Body: `{"ports":[` + p + `]}`},
		"GET /ports/" + portID:    {Body: `{"port":` + p + `}`},
		"POST /ports":             {Status: http.StatusCreated, Body: `{"port":` + p + `}`},
		"PUT /ports/" + portID:    {Body: `{"port":` + p + `}`},
		"DELETE /ports/" + portID: {Status: http.StatusNoContent},
	})
	ctx := context.Background()
	all, err := network.ListPorts(ctx, sc, ports.ListOpts{DeviceID: "srv-1"})
	if err != nil || len(all) != 1 || all[0].PortSecurityEnabled == nil || *all[0].PortSecurityEnabled ||
		all[0].BindingVNICType != "direct" || all[0].BindingProfile["k"] != "v" || all[0].Trusted == nil ||
		all[0].TrunkDetails.TrunkID != "trunk-1" || all[0].TrunkDetails.SubPorts[0].SegmentationID != 100 {
		t.Fatalf("ListPorts = %+v, %v", all, err)
	}
	if got, err := network.GetPort(ctx, sc, portID); err != nil || got.DNSName != "web" {
		t.Fatalf("GetPort = %+v, %v", got, err)
	}
	if _, err := network.CreatePort(ctx, sc, network.WithPortCreateAttrs(ports.CreateOpts{NetworkID: netID},
		map[string]any{"binding:vnic_type": "direct"})); err != nil {
		t.Fatalf("CreatePort: %v", err)
	}
	name := "p2"
	if _, err := network.UpdatePort(ctx, sc, portID, ports.UpdateOpts{Name: &name}); err != nil {
		t.Fatalf("UpdatePort: %v", err)
	}
	if err := network.DeletePort(ctx, sc, portID); err != nil {
		t.Fatalf("DeletePort: %v", err)
	}
	assertRequests(t, *seen, []req{
		{"GET", "/ports", "device_id=srv-1", "", ""},
		{"GET", "/ports/" + portID, "", "", ""},
		{"POST", "/ports", "", "", `{"port":{"binding:vnic_type":"direct","network_id":"` + netID + `"}}`},
		{"PUT", "/ports/" + portID, "", "", `{"port":{"name":"p2"}}`},
		{"DELETE", "/ports/" + portID, "", "", ""},
	})

	_, bad := fakeNeutron(t, map[string]reply{"GET /ports": {Body: `{"ports":[{"id":"p","trusted":"yes"}]}`}})
	if _, err := network.ListPorts(ctx, bad, ports.ListOpts{}); err == nil || !strings.HasPrefix(err.Error(), "parsing port list: ") {
		t.Errorf("malformed list: %v", err)
	}
	_, gone := fakeNeutron(t, nil)
	is404(t, second(network.ListPorts(ctx, gone, ports.ListOpts{})), "listing ports: ")
	is404(t, second(network.GetPort(ctx, gone, "p")), "getting port p: ")
	is404(t, second(network.CreatePort(ctx, gone, ports.CreateOpts{NetworkID: "n"})), "creating port: ")
	is404(t, second(network.UpdatePort(ctx, gone, "p", ports.UpdateOpts{})), "updating port p: ")
	is404(t, network.DeletePort(ctx, gone, "p"), "deleting port p: ")
}

func TestRouters(t *testing.T) {
	r := `{"id":"` + routerID + `","name":"r1","ha":true,"distributed":false,"availability_zones":["nova"],"evpn_vni":7,` +
		`"external_gateway_info":{"network_id":"` + netID + `"},"routes":[{"destination":"198.51.100.0/24","nexthop":"192.0.2.254"}]}`
	iface := `{"id":"` + routerID + `","subnet_id":"` + subnetID + `","port_id":"` + portID + `"}`
	seen, sc := fakeNeutron(t, map[string]reply{
		"GET /routers":                                          {Body: `{"routers":[` + r + `]}`},
		"GET /agents/agent-1/l3-routers":                        {Body: `{"routers":[` + r + `]}`},
		"GET /routers/" + routerID:                              {Body: `{"router":` + r + `}`},
		"POST /routers":                                         {Status: http.StatusCreated, Body: `{"router":` + r + `}`},
		"PUT /routers/" + routerID:                              {Body: `{"router":` + r + `}`},
		"DELETE /routers/" + routerID:                           {Status: http.StatusNoContent},
		"PUT /routers/" + routerID + "/add_router_interface":    {Body: iface},
		"PUT /routers/" + routerID + "/remove_router_interface": {Body: iface},
		"GET /ports?device_id=" + routerID: {Body: `{"ports":[` +
			`{"id":"gw","device_owner":"network:router_gateway","fixed_ips":[{"subnet_id":"pub","ip_address":"198.51.100.2"}]},` +
			`{"id":"` + portID + `","device_owner":"network:router_interface","fixed_ips":[` +
			`{"subnet_id":"` + subnetID + `","ip_address":"192.0.2.1"},{"subnet_id":"v6","ip_address":"2001:db8::1"}]}]}`},
	})
	ctx := context.Background()
	all, err := network.ListRouters(ctx, sc, routers.ListOpts{Name: "r1"})
	if err != nil || len(all) != 1 || all[0].Name != "r1" || all[0].Attrs.HA == nil || !*all[0].Attrs.HA || all[0].Attrs.Distributed == nil ||
		all[0].Attrs.EVPNVNI == nil || *all[0].Attrs.EVPNVNI != 7 || len(all[0].Routes) != 1 || all[0].GatewayInfo.NetworkID != netID {
		t.Fatalf("ListRouters = %+v, %v", all, err)
	}
	if hosted, err := network.ListL3AgentRouters(ctx, sc, "agent-1"); err != nil || len(hosted) != 1 || hosted[0].Attrs.AvailabilityZones == nil {
		t.Fatalf("ListL3AgentRouters = %+v, %v", hosted, err)
	}
	if got, err := network.GetRouter(ctx, sc, routerID); err != nil || got.Attrs.HA == nil || got.Name != "r1" {
		t.Fatalf("GetRouter = %+v, %v", got, err)
	}
	if _, err := network.CreateRouter(ctx, sc, network.WithRouterCreateAttrs(routers.CreateOpts{Name: "r1"},
		map[string]any{"enable_ndp_proxy": true})); err != nil {
		t.Fatalf("CreateRouter: %v", err)
	}
	if _, err := network.UpdateRouter(ctx, sc, routerID, network.ClearGatewayOpts{}); err != nil {
		t.Fatalf("UpdateRouter: %v", err)
	}
	if info, err := network.AddRouterInterface(ctx, sc, routerID, routers.AddInterfaceOpts{SubnetID: subnetID}); err != nil || info.PortID != portID {
		t.Fatalf("AddRouterInterface = %+v, %v", info, err)
	}
	if _, err := network.RemoveRouterInterface(ctx, sc, routerID, routers.RemoveInterfaceOpts{PortID: portID}); err != nil {
		t.Fatalf("RemoveRouterInterface: %v", err)
	}
	ifaces, err := network.ListRouterInterfaces(ctx, sc, routerID)
	if err != nil || len(ifaces) != 2 || ifaces[0] != (network.RouterInterface{PortID: portID, IPAddress: "192.0.2.1", SubnetID: subnetID}) ||
		ifaces[1].SubnetID != "v6" {
		t.Fatalf("ListRouterInterfaces = %+v, %v", ifaces, err)
	}
	if err := network.DeleteRouter(ctx, sc, routerID); err != nil {
		t.Fatalf("DeleteRouter: %v", err)
	}
	assertRequests(t, *seen, []req{
		{"GET", "/routers", "name=r1", "", ""},
		{"GET", "/agents/agent-1/l3-routers", "", "", ""},
		{"GET", "/routers/" + routerID, "", "", ""},
		{"POST", "/routers", "", "", `{"router":{"enable_ndp_proxy":true,"name":"r1"}}`},
		// An empty gateway is sent, not dropped: that is the removal.
		{"PUT", "/routers/" + routerID, "", "", `{"router":{"external_gateway_info":{}}}`},
		{"PUT", "/routers/" + routerID + "/add_router_interface", "", "", `{"subnet_id":"` + subnetID + `"}`},
		{"PUT", "/routers/" + routerID + "/remove_router_interface", "", "", `{"port_id":"` + portID + `"}`},
		{"GET", "/ports", "device_id=" + routerID, "", ""},
		{"DELETE", "/routers/" + routerID, "", "", ""},
	})

	_, bad := fakeNeutron(t, map[string]reply{
		"GET /routers":             {Body: `{"routers":[{"id":"r","ha":"yes"}]}`},
		"GET /routers/r":           {Body: `{"router":{"id":"r","ha":"yes"}}`},
		"GET /routers/q":           {Body: `{"router":{"id":1}}`},
		"GET /agents/a/l3-routers": {Body: `{"routers":[{"id":"r","ha":"yes"}]}`},
		"GET /agents/b/l3-routers": {Body: `{"routers":[{"id":1}]}`},
		"GET /ports?device_id=r":   {Body: `{"ports":[{"id":1}]}`},
	})
	if _, err := network.ListRouters(ctx, bad, routers.ListOpts{}); err == nil || !strings.HasPrefix(err.Error(), "parsing router list: ") {
		t.Errorf("malformed list: %v", err)
	}
	for _, id := range []string{"r", "q"} {
		if _, err := network.GetRouter(ctx, bad, id); err == nil || !strings.HasPrefix(err.Error(), "getting router "+id+": ") {
			t.Errorf("malformed router %s: %v", id, err)
		}
	}
	for _, agent := range []string{"a", "b"} {
		if _, err := network.ListL3AgentRouters(ctx, bad, agent); err == nil || !strings.HasPrefix(err.Error(), "listing routers hosted by agent "+agent+": ") {
			t.Errorf("malformed agent list %s: %v", agent, err)
		}
	}
	if _, err := network.ListRouterInterfaces(ctx, bad, "r"); err == nil || !strings.HasPrefix(err.Error(), "listing ports of router r: ") {
		t.Errorf("malformed router ports: %v", err)
	}
	_, gone := fakeNeutron(t, nil)
	is404(t, second(network.ListRouters(ctx, gone, routers.ListOpts{})), "listing routers: ")
	is404(t, second(network.GetRouter(ctx, gone, "r")), "getting router r: ")
	is404(t, second(network.CreateRouter(ctx, gone, routers.CreateOpts{})), "creating router: ")
	is404(t, second(network.UpdateRouter(ctx, gone, "r", routers.UpdateOpts{})), "updating router r: ")
	is404(t, network.DeleteRouter(ctx, gone, "r"), "deleting router r: ")
	is404(t, second(network.AddRouterInterface(ctx, gone, "r", routers.AddInterfaceOpts{PortID: "p"})), "adding an interface to router r: ")
	is404(t, second(network.RemoveRouterInterface(ctx, gone, "r", routers.RemoveInterfaceOpts{PortID: "p"})), "removing an interface from router r: ")
	is404(t, second(network.ListRouterInterfaces(ctx, gone, "r")), "listing ports of router r: ")
}

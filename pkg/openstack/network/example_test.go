package network_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/fwaas_v2/rules"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/layer3/floatingips"

	"github.com/ftarasenko/go-openstackclient/pkg/openstack"
	"github.com/ftarasenko/go-openstackclient/pkg/openstack/network"
)

// Read a project's network quotas and the defaults neutron would apply.
func ExampleGetQuota() {
	neutronAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Println(r.Method, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v2.0/quotas/demo/default" {
			_, _ = io.WriteString(w, `{"quota":{"network":100,"port":500}}`)
			return
		}
		_, _ = io.WriteString(w, `{"quota":{"network":10,"port":50}}`)
	}))
	defer neutronAPI.Close()

	pc := &gophercloud.ProviderClient{
		EndpointLocator: func(gophercloud.EndpointOpts) (string, error) { return neutronAPI.URL + "/", nil },
	}
	sess := openstack.FromProvider(pc, gophercloud.EndpointOpts{Region: "RegionOne"}, openstack.Options{})
	nc, err := sess.Network()
	if err != nil {
		fmt.Println(err)
		return
	}
	ctx := context.Background()
	q, err := network.GetQuota(ctx, nc, "demo")
	if err != nil {
		fmt.Println(err)
		return
	}
	d, err := network.GetDefaultQuota(ctx, nc, "demo")
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Printf("networks %d of a default %d, ports %d of %d\n", q.Network, d.Network, q.Port, d.Port)
	// Output:
	// GET /v2.0/quotas/demo
	// GET /v2.0/quotas/demo/default
	// networks 10 of a default 100, ports 50 of 500
}

// Allocate a floating IP on an external network found by name, with an
// extension attribute gophercloud's CreateOpts does not model. The lookup is
// GET /v2.0/networks?name=public, and the create sends
// {"floatingip":{"floating_network_id":"net-1","qos_policy_id":"qos-1"}}.
func ExampleCreateFloatingIP() {
	neutronAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, `{"networks":[{"id":"net-1","name":"public"}]}`)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"floatingip":{"id":"fip-1","floating_ip_address":"203.0.113.10","qos_policy_id":"qos-1"}}`)
	}))
	defer neutronAPI.Close()

	pc := &gophercloud.ProviderClient{
		EndpointLocator: func(gophercloud.EndpointOpts) (string, error) { return neutronAPI.URL + "/", nil },
	}
	nc, err := openstack.FromProvider(pc, gophercloud.EndpointOpts{}, openstack.Options{}).Network()
	if err != nil {
		fmt.Println(err)
		return
	}
	ctx := context.Background()
	netID, err := network.FindNetworkID(ctx, nc, "public")
	if err != nil {
		fmt.Println(err)
		return
	}
	fip, err := network.CreateFloatingIP(ctx, nc, network.WithFloatingIPCreateAttrs(
		floatingips.CreateOpts{FloatingNetworkID: netID}, map[string]any{"qos_policy_id": "qos-1"}))
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(fip.FloatingIP.FloatingIP, fip.QoSPolicyID)
	// Output:
	// 203.0.113.10 qos-1
}

// Show what upstream's router show calls interfaces_info: one entry per fixed
// IP of each of the router's ports other than its gateway. neutron is asked
// for GET /v2.0/ports?device_id=<router>.
func ExampleListRouterInterfaces() {
	neutronAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ports":[`+
			`{"id":"gw","device_owner":"network:router_gateway","fixed_ips":[{"subnet_id":"public","ip_address":"198.51.100.2"}]},`+
			`{"id":"port-1","device_owner":"network:router_interface","fixed_ips":[{"subnet_id":"private","ip_address":"192.0.2.1"}]}]}`)
	}))
	defer neutronAPI.Close()

	pc := &gophercloud.ProviderClient{
		EndpointLocator: func(gophercloud.EndpointOpts) (string, error) { return neutronAPI.URL + "/", nil },
	}
	nc, err := openstack.FromProvider(pc, gophercloud.EndpointOpts{}, openstack.Options{}).Network()
	if err != nil {
		fmt.Println(err)
		return
	}
	ifaces, err := network.ListRouterInterfaces(context.Background(), nc, "router-1")
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, i := range ifaces {
		fmt.Println(i.PortID, i.SubnetID, i.IPAddress)
	}
	// Output:
	// port-1 private 192.0.2.1
}

// Add a minimum_packet_rate rule, a type gophercloud's qos/rules package does
// not model, to a QoS policy found by name, then read the type back off the
// policy as the per-rule calls need it. The lookup is
// GET /v2.0/qos/policies?name=gold, and the create is
// POST /v2.0/qos/policies/qos-1/minimum_packet_rate_rules.
func ExampleCreateQoSRule() {
	neutronAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"minimum_packet_rate_rule":{"id":"rule-1","min_kpps":1000,"direction":"egress"}}`)
		case r.URL.Path == "/v2.0/qos/policies":
			_, _ = io.WriteString(w, `{"policies":[{"id":"qos-1","name":"gold"}]}`)
		default:
			_, _ = io.WriteString(w, `{"policy":{"id":"qos-1","rules":[{"id":"rule-1","type":"minimum_packet_rate"}]}}`)
		}
	}))
	defer neutronAPI.Close()

	pc := &gophercloud.ProviderClient{
		EndpointLocator: func(gophercloud.EndpointOpts) (string, error) { return neutronAPI.URL + "/", nil },
	}
	nc, err := openstack.FromProvider(pc, gophercloud.EndpointOpts{}, openstack.Options{}).Network()
	if err != nil {
		fmt.Println(err)
		return
	}
	ctx := context.Background()
	policyID, err := network.FindQoSPolicyID(ctx, nc, "gold")
	if err != nil {
		fmt.Println(err)
		return
	}
	rule, err := network.CreateQoSRule(ctx, nc, policyID, "minimum_packet_rate",
		map[string]any{"min_kpps": 1000, "direction": "egress"})
	if err != nil {
		fmt.Println(err)
		return
	}
	ruleType, err := network.FindQoSRuleType(ctx, nc, policyID, fmt.Sprint(rule["id"]))
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(rule["id"], ruleType, rule["min_kpps"], rule["direction"])
	// Output:
	// rule-1 minimum_packet_rate 1000 egress
}

// Which L3 agents host a router, and which one is active.
func ExampleListRouterL3Agents() {
	neutronAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v2.0/routers" {
			_, _ = io.WriteString(w, `{"routers":[{"id":"router-1","name":"edge"}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"agents":[`+
			`{"id":"agent-1","host":"net-1","alive":true,"ha_state":"active"},`+
			`{"id":"agent-2","host":"net-2","alive":true,"ha_state":"standby"}]}`)
	}))
	defer neutronAPI.Close()

	pc := &gophercloud.ProviderClient{
		EndpointLocator: func(gophercloud.EndpointOpts) (string, error) { return neutronAPI.URL + "/", nil },
	}
	nc, err := openstack.FromProvider(pc, gophercloud.EndpointOpts{}, openstack.Options{}).Network()
	if err != nil {
		fmt.Println(err)
		return
	}
	ctx := context.Background()
	routerID, err := network.FindRouterID(ctx, nc, "edge")
	if err != nil {
		fmt.Println(err)
		return
	}
	hosts, err := network.ListRouterL3Agents(ctx, nc, routerID)
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, a := range hosts {
		fmt.Println(a.Host, a.HAState)
	}
	// Output:
	// net-1 active
	// net-2 standby
}

// Firewall rules, with the remote-group attributes rules.Rule lacks.
func ExampleListFirewallRules() {
	neutronAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"firewall_rules":[`+
			`{"id":"rule-1","name":"web","action":"allow","protocol":"tcp","destination_port":"443"},`+
			`{"id":"rule-2","name":"from-dmz","action":"deny","source_firewall_group_id":"group-dmz"}]}`)
	}))
	defer neutronAPI.Close()

	pc := &gophercloud.ProviderClient{
		EndpointLocator: func(gophercloud.EndpointOpts) (string, error) { return neutronAPI.URL + "/", nil },
	}
	nc, err := openstack.FromProvider(pc, gophercloud.EndpointOpts{}, openstack.Options{}).Network()
	if err != nil {
		fmt.Println(err)
		return
	}
	all, err := network.ListFirewallRules(context.Background(), nc, rules.ListOpts{})
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, r := range all {
		from := r.SourceFirewallGroupID
		if from == "" {
			from = "any"
		}
		fmt.Println(r.Name, r.Action, "from", from)
	}
	// Output:
	// web allow from any
	// from-dmz deny from group-dmz
}

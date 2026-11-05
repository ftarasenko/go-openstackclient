package network

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/agents"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/layer3/routers"
)

// ListAgents lists every network agent matching opts. neutron matches
// agent_type on the full name ("L3 agent", "DHCP agent").
func ListAgents(ctx context.Context, c *gophercloud.ServiceClient, opts agents.ListOptsBuilder) ([]agents.Agent, error) {
	pages, err := agents.List(c, opts).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing network agents")
	}
	all, err := agents.ExtractAgents(pages)
	return all, wrap(err, "parsing network agent list")
}

// GetAgent reads one network agent.
func GetAgent(ctx context.Context, c *gophercloud.ServiceClient, id string) (*agents.Agent, error) {
	a, err := agents.Get(ctx, c, id).Extract()
	return a, wrap(err, "getting network agent %s", id)
}

// UpdateAgent updates an agent's description or admin state.
func UpdateAgent(ctx context.Context, c *gophercloud.ServiceClient, id string, opts agents.UpdateOptsBuilder) (*agents.Agent, error) {
	a, err := agents.Update(ctx, c, id, opts).Extract()
	return a, wrap(err, "updating network agent %s", id)
}

// DeleteAgent deletes a network agent.
func DeleteAgent(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(agents.Delete(ctx, c, id).ExtractErr(), "deleting network agent %s", id)
}

// ListNetworkDHCPAgents lists the DHCP agents hosting a network
// (GET /networks/{id}/dhcp-agents), the direction gophercloud's agents
// package does not model.
func ListNetworkDHCPAgents(ctx context.Context, c *gophercloud.ServiceClient, networkID string) ([]agents.Agent, error) {
	var body struct {
		Agents []agents.Agent `json:"agents"`
	}
	if err := getJSON(ctx, c, c.ServiceURL("networks", networkID, "dhcp-agents"), &body); err != nil {
		return nil, wrap(err, "listing DHCP agents hosting network %s", networkID)
	}
	return body.Agents, nil
}

// ListRouterL3Agents lists the L3 agents hosting a router, with each
// agent's HA state.
func ListRouterL3Agents(ctx context.Context, c *gophercloud.ServiceClient, routerID string) ([]routers.L3Agent, error) {
	pages, err := routers.ListL3Agents(c, routerID).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing L3 agents hosting router %s", routerID)
	}
	all, err := routers.ExtractL3Agents(pages)
	return all, wrap(err, "parsing L3 agent list")
}

// AddDHCPAgentNetwork schedules a network on a DHCP agent.
func AddDHCPAgentNetwork(ctx context.Context, c *gophercloud.ServiceClient, agentID, networkID string) error {
	err := agents.ScheduleDHCPNetwork(ctx, c, agentID, agents.ScheduleDHCPNetworkOpts{NetworkID: networkID}).ExtractErr()
	return wrap(err, "adding network %s to DHCP agent %s", networkID, agentID)
}

// RemoveDHCPAgentNetwork removes a network from a DHCP agent.
func RemoveDHCPAgentNetwork(ctx context.Context, c *gophercloud.ServiceClient, agentID, networkID string) error {
	err := agents.RemoveDHCPNetwork(ctx, c, agentID, networkID).ExtractErr()
	return wrap(err, "removing network %s from DHCP agent %s", networkID, agentID)
}

// L3AgentRouterOpts schedules a router on an L3 agent. It is
// agents.ScheduleL3RouterOpts plus ha_chassis_priority, which ML2/OVN's L3
// scheduler accepts and gophercloud does not model; nil omits it.
type L3AgentRouterOpts struct {
	RouterID          string `json:"router_id" required:"true"`
	HAChassisPriority *int   `json:"ha_chassis_priority,omitempty"`
}

// ToAgentScheduleL3RouterMap implements agents.ScheduleL3RouterOptsBuilder.
func (opts L3AgentRouterOpts) ToAgentScheduleL3RouterMap() (map[string]any, error) {
	return gophercloud.BuildRequestBody(opts, "")
}

// AddL3AgentRouter schedules a router on an L3 agent.
func AddL3AgentRouter(ctx context.Context, c *gophercloud.ServiceClient, agentID string, opts L3AgentRouterOpts) error {
	err := agents.ScheduleL3Router(ctx, c, agentID, opts).ExtractErr()
	return wrap(err, "adding router %s to L3 agent %s", opts.RouterID, agentID)
}

// RemoveL3AgentRouter removes a router from an L3 agent.
func RemoveL3AgentRouter(ctx context.Context, c *gophercloud.ServiceClient, agentID, routerID string) error {
	err := agents.RemoveL3Router(ctx, c, agentID, routerID).ExtractErr()
	return wrap(err, "removing router %s from L3 agent %s", routerID, agentID)
}

// SetL3AgentRouterPriority sets a router's HA chassis priority on an L3
// agent: PUT /agents/{agent}/l3-routers/{router} with
// {"ha_chassis_priority": N} and no envelope, as openstacksdk sends it. Only
// ML2/OVN's L3 scheduler implements it.
func SetL3AgentRouterPriority(ctx context.Context, c *gophercloud.ServiceClient, agentID, routerID string, priority int) error {
	resp, err := c.Put(ctx, c.ServiceURL("agents", agentID, "l3-routers", routerID),
		map[string]any{"ha_chassis_priority": priority}, nil,
		&gophercloud.RequestOpts{OkCodes: []int{200, 201, 202, 204}})
	if resp != nil {
		closeBody(resp.Body)
	}
	return wrap(err, "setting router %s on L3 agent %s", routerID, agentID)
}

// ServiceProvider is one of neutron's service providers.
type ServiceProvider struct {
	ServiceType string `json:"service_type"`
	Name        string `json:"name"`
	Default     bool   `json:"default"`
}

// ListServiceProviders lists neutron's service providers
// (GET /service-providers), which gophercloud has no package for.
func ListServiceProviders(ctx context.Context, c *gophercloud.ServiceClient) ([]ServiceProvider, error) {
	var body struct {
		ServiceProviders []ServiceProvider `json:"service_providers"`
	}
	if err := getJSON(ctx, c, c.ServiceURL("service-providers"), &body); err != nil {
		return nil, wrap(err, "listing service providers")
	}
	return body.ServiceProviders, nil
}

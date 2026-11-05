package network

import (
	"context"
	"fmt"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/agents"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/layer3/extraroutes"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/layer3/routers"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/ports"
)

// Router is gophercloud's router plus the attributes it does not model.
// routers.Router has its own UnmarshalJSON, which a Router inherits, so the
// functions here decode the body twice, once into each part; decode one
// yourself the same way.
type Router struct {
	routers.Router
	Attrs RouterAttrs `json:"-"`
}

// RouterAttrs are router attributes gophercloud's routers.Router does not
// model. Each is a pointer, nil when neutron did not send it: neutron sends
// distributed and ha only to an admin, so absence is information.
// Distributed is also a plain bool on routers.Router, false when absent.
type RouterAttrs struct {
	Distributed       *bool     `json:"distributed"`
	HA                *bool     `json:"ha"`
	AvailabilityZones *[]string `json:"availability_zones"`
	FlavorID          *string   `json:"flavor_id"`

	EnableNDPProxy         *bool `json:"enable_ndp_proxy"`
	EnableDefaultRouteBFD  *bool `json:"enable_default_route_bfd"`
	EnableDefaultRouteECMP *bool `json:"enable_default_route_ecmp"`
	EVPNVNI                *int  `json:"evpn_vni"`
	// EnableSNAT is 2026.1's read-only top-level enable_snat.
	EnableSNAT *bool `json:"enable_snat"`
}

// RouterInterface is one fixed IP of one of a router's internal ports, the
// shape of upstream's interfaces_info.
type RouterInterface struct {
	PortID    string `json:"port_id"`
	IPAddress string `json:"ip_address"`
	SubnetID  string `json:"subnet_id"`
}

// ClearGatewayOpts is the update that removes a router's external gateway.
// routers.UpdateOpts drops an empty GatewayInfo (omitempty), which would
// make the request a no-op; this always sends "external_gateway_info": {}.
type ClearGatewayOpts struct{}

// ToRouterUpdateMap implements routers.UpdateOptsBuilder.
func (ClearGatewayOpts) ToRouterUpdateMap() (map[string]any, error) {
	return map[string]any{"router": map[string]any{"external_gateway_info": map[string]any{}}}, nil
}

func extractRouters(extract func(v any) error) ([]Router, error) {
	var base []routers.Router
	var attrs []RouterAttrs
	if err := extract(&base); err != nil {
		return nil, err
	}
	if err := extract(&attrs); err != nil {
		return nil, err
	}
	out := make([]Router, len(base))
	for i := range base {
		out[i].Router = base[i]
		if i < len(attrs) {
			out[i].Attrs = attrs[i]
		}
	}
	return out, nil
}

func extractRouter(res gophercloud.Result) (*Router, error) {
	var r Router
	if err := res.ExtractIntoStructPtr(&r.Router, "router"); err != nil {
		return nil, err
	}
	if err := res.ExtractIntoStructPtr(&r.Attrs, "router"); err != nil {
		return nil, err
	}
	return &r, nil
}

// ListRouters lists every router matching opts, following pagination.
func ListRouters(ctx context.Context, c *gophercloud.ServiceClient, opts routers.ListOptsBuilder) ([]Router, error) {
	pages, err := routers.List(c, opts).AllPages(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing routers: %w", err)
	}
	all, err := extractRouters(func(v any) error { return routers.ExtractRoutersInto(pages, v) })
	if err != nil {
		return nil, fmt.Errorf("parsing router list: %w", err)
	}
	return all, nil
}

// ListL3AgentRouters lists the routers an L3 agent hosts. The subresource
// takes no filters.
func ListL3AgentRouters(ctx context.Context, c *gophercloud.ServiceClient, agentID string) ([]Router, error) {
	res := agents.ListL3Routers(ctx, c, agentID)
	all, err := extractRouters(func(v any) error { return res.ExtractIntoSlicePtr(v, "routers") })
	if err != nil {
		return nil, fmt.Errorf("listing routers hosted by agent %s: %w", agentID, err)
	}
	return all, nil
}

// GetRouter reads one router.
func GetRouter(ctx context.Context, c *gophercloud.ServiceClient, id string) (*Router, error) {
	r, err := extractRouter(routers.Get(ctx, c, id).Result)
	if err != nil {
		return nil, fmt.Errorf("getting router %s: %w", id, err)
	}
	return r, nil
}

// CreateRouter creates a router. Wrap opts with WithRouterCreateAttrs to send
// attributes gophercloud does not model.
func CreateRouter(ctx context.Context, c *gophercloud.ServiceClient, opts routers.CreateOptsBuilder) (*Router, error) {
	r, err := extractRouter(routers.Create(ctx, c, opts).Result)
	if err != nil {
		return nil, fmt.Errorf("creating router: %w", err)
	}
	return r, nil
}

// UpdateRouter updates a router. ClearGatewayOpts removes its gateway.
func UpdateRouter(ctx context.Context, c *gophercloud.ServiceClient, id string, opts routers.UpdateOptsBuilder) (*Router, error) {
	r, err := extractRouter(routers.Update(ctx, c, id, opts).Result)
	if err != nil {
		return nil, fmt.Errorf("updating router %s: %w", id, err)
	}
	return r, nil
}

// DeleteRouter deletes a router.
func DeleteRouter(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	if err := routers.Delete(ctx, c, id).ExtractErr(); err != nil {
		return fmt.Errorf("deleting router %s: %w", id, err)
	}
	return nil
}

// AddRouterInterface attaches a subnet or a port to a router.
func AddRouterInterface(ctx context.Context, c *gophercloud.ServiceClient, routerID string, opts routers.AddInterfaceOptsBuilder) (*routers.InterfaceInfo, error) {
	info, err := routers.AddInterface(ctx, c, routerID, opts).Extract()
	if err != nil {
		return nil, fmt.Errorf("adding an interface to router %s: %w", routerID, err)
	}
	return info, nil
}

// RemoveRouterInterface detaches a subnet or a port from a router.
func RemoveRouterInterface(ctx context.Context, c *gophercloud.ServiceClient, routerID string, opts routers.RemoveInterfaceOptsBuilder) (*routers.InterfaceInfo, error) {
	info, err := routers.RemoveInterface(ctx, c, routerID, opts).Extract()
	if err != nil {
		return nil, fmt.Errorf("removing an interface from router %s: %w", routerID, err)
	}
	return info, nil
}

// ListRouterInterfaces derives a router's interfaces the way upstream's
// router show does: neutron's router body carries none, so the router's ports
// are listed by device_id, and every fixed IP of each port other than the
// gateway becomes one entry.
func ListRouterInterfaces(ctx context.Context, c *gophercloud.ServiceClient, routerID string) ([]RouterInterface, error) {
	pages, err := ports.List(c, ports.ListOpts{DeviceID: routerID}).AllPages(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing ports of router %s: %w", routerID, err)
	}
	all, err := ports.ExtractPorts(pages)
	if err != nil {
		return nil, fmt.Errorf("parsing ports of router %s: %w", routerID, err)
	}
	out := make([]RouterInterface, 0, len(all))
	for _, p := range all {
		if p.DeviceOwner == "network:router_gateway" {
			continue
		}
		for _, ip := range p.FixedIPs {
			out = append(out, RouterInterface{PortID: p.ID, IPAddress: ip.IPAddress, SubnetID: ip.SubnetID})
		}
	}
	return out, nil
}

// AddRouterRoutes adds static routes to a router, leaving its other routes
// in place, and returns the router as the extraroutes call does.
func AddRouterRoutes(ctx context.Context, c *gophercloud.ServiceClient, routerID string, routes []routers.Route) (*routers.Router, error) {
	r, err := extraroutes.Add(ctx, c, routerID, extraroutes.Opts{Routes: &routes}).Extract()
	return r, wrap(err, "updating the routes of router %s", routerID)
}

// RemoveRouterRoutes removes static routes from a router.
func RemoveRouterRoutes(ctx context.Context, c *gophercloud.ServiceClient, routerID string, routes []routers.Route) (*routers.Router, error) {
	r, err := extraroutes.Remove(ctx, c, routerID, extraroutes.Opts{Routes: &routes}).Extract()
	return r, wrap(err, "updating the routes of router %s", routerID)
}

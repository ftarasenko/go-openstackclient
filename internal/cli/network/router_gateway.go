package network

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/layer3/routers"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// "router add gateway" / "router remove gateway" set and clear a router's
// external gateway. Neutron has no dedicated endpoint for either: the gateway
// lives in the router's external_gateway_info attribute, so both are a PUT on
// the router itself — set it to a network, or set it to null.
//
// Flag names follow upstream OSC; UNVERIFIED against KeyStack docs
// (https://docs.keystack.ru/ returned HTTP 403 at implementation time).

// newRouterAddGatewayCommand builds "router add gateway <router> <network>".
func newRouterAddGatewayCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var fixedIPs []string
	cmd := &cobra.Command{
		Use:   "gateway <router> <network>",
		Short: "Set a router's external gateway",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			return runRouterAddGateway(ctx, client, o, args[0], args[1], fixedIPs, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringArrayVar(&fixedIPs, flagFixedIP, nil,
		"gateway address as subnet=<name|id>[,ip-address=<ip>] (repeatable)")
	return cmd
}

func runRouterAddGateway(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	routerArg, networkArg string, fixedIPs []string, w io.Writer,
) error {
	routerID, err := resolveRouterID(ctx, client, routerArg)
	if err != nil {
		return err
	}
	networkID, err := resolveNetworkID(ctx, client, networkArg)
	if err != nil {
		return err
	}
	external, err := parseExternalFixedIPs(ctx, client, fixedIPs)
	if err != nil {
		return err
	}
	gateway := routers.GatewayInfo{NetworkID: networkID, ExternalFixedIPs: external}
	r, ext, err := extractRouterDetail(routers.Update(ctx, client, routerID, routers.UpdateOpts{GatewayInfo: &gateway}).Result)
	if err != nil {
		return fmt.Errorf("setting the external gateway of router %s: %w", routerArg, err)
	}
	return writeRouterDetail(o, w, r, ext)
}

// newRouterRemoveGatewayCommand builds "router remove gateway <router> [<network>]".
//
// Upstream's RemoveGatewayFromRouter takes a required <network> plus --fixed-ip
// to pick one of several gateways, and refuses to run without neutron's
// external-gateway-multihoming extension (post-Zed). On the single-gateway
// API a router has at most one gateway, so koc keeps <network> optional and
// treats both as a guard: when given, the router's current gateway must match
// them or nothing is removed.
func newRouterRemoveGatewayCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var fixedIPs []string
	cmd := &cobra.Command{
		Use:   "gateway <router> [<network>]",
		Short: "Clear a router's external gateway",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			var networkArg string
			if len(args) == 2 {
				networkArg = args[1]
			}
			return runRouterRemoveGateway(ctx, client, o, args[0], networkArg, fixedIPs, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringArrayVar(&fixedIPs, flagFixedIP, nil,
		"only remove a gateway holding this address, as subnet=<name|id>[,ip-address=<ip>] (repeatable)")
	return cmd
}

// runRouterRemoveGateway clears the gateway. gophercloud's UpdateOpts tags
// GatewayInfo `omitempty`, so a pointer to a zero GatewayInfo would be dropped
// from the body and the request would become a no-op instead of a removal —
// neutron needs an explicit `"external_gateway_info": {}`. The builder below
// writes that key unconditionally. networkArg and fixedIPs, when given, must
// match the current gateway first (checkGatewayMatches).
func runRouterRemoveGateway(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	routerArg, networkArg string, fixedIPs []string, w io.Writer,
) error {
	routerID, err := resolveRouterID(ctx, client, routerArg)
	if err != nil {
		return err
	}
	if networkArg != "" || len(fixedIPs) > 0 {
		if err := checkGatewayMatches(ctx, client, routerArg, routerID, networkArg, fixedIPs); err != nil {
			return err
		}
	}
	r, ext, err := extractRouterDetail(routers.Update(ctx, client, routerID, clearGatewayOpts{}).Result)
	if err != nil {
		return fmt.Errorf("clearing the external gateway of router %s: %w", routerArg, err)
	}
	return writeRouterDetail(o, w, r, ext)
}

// checkGatewayMatches reads the router and errors unless its gateway is on
// networkArg (when given) and holds an address matching each --fixed-ip spec.
// A spec matches a gateway address when every key it names agrees, so unlike
// "add gateway" either key alone identifies an address here.
func checkGatewayMatches(ctx context.Context, client *gophercloud.ServiceClient,
	routerArg, routerID, networkArg string, fixedIPs []string,
) error {
	current, err := routers.Get(ctx, client, routerID).Extract()
	if err != nil {
		return fmt.Errorf("reading router %s: %w", routerArg, err)
	}
	gw := current.GatewayInfo
	if gw.NetworkID == "" {
		return fmt.Errorf("router %s has no external gateway", routerArg)
	}
	if networkArg != "" {
		networkID, err := resolveNetworkID(ctx, client, networkArg)
		if err != nil {
			return err
		}
		if networkID != gw.NetworkID {
			return fmt.Errorf("router %s has no gateway on network %s (its gateway is on %s)", routerArg, networkArg, gw.NetworkID)
		}
	}
	for _, spec := range fixedIPs {
		want, err := resolveGatewayFixedIPSpec(ctx, client, spec)
		if err != nil {
			return err
		}
		if !slices.ContainsFunc(gw.ExternalFixedIPs, func(have routers.ExternalFixedIP) bool {
			return (want.SubnetID == "" || want.SubnetID == have.SubnetID) &&
				(want.IPAddress == "" || want.IPAddress == have.IPAddress)
		}) {
			return fmt.Errorf("router %s has no gateway address matching --%s %q", routerArg, flagFixedIP, spec)
		}
	}
	return nil
}

// resolveGatewayFixedIPSpec parses one identifying --fixed-ip spec and
// resolves its subnet, requiring at least one of the two keys.
func resolveGatewayFixedIPSpec(ctx context.Context, client *gophercloud.ServiceClient, spec string) (routers.ExternalFixedIP, error) {
	parsed, err := parseFixedIPSpec(spec)
	if err != nil {
		return routers.ExternalFixedIP{}, err
	}
	out := routers.ExternalFixedIP{IPAddress: parsed.ipAddress}
	if parsed.subnetRef != "" {
		if out.SubnetID, err = resolveSubnetID(ctx, client, parsed.subnetRef); err != nil {
			return out, err
		}
	}
	if out.SubnetID == "" && out.IPAddress == "" {
		return out, fmt.Errorf("--%s %q requires subnet= or ip-address=", flagFixedIP, spec)
	}
	return out, nil
}

// clearGatewayOpts implements routers.UpdateOptsBuilder to emit exactly
// {"router": {"external_gateway_info": {}}}.
type clearGatewayOpts struct{}

func (clearGatewayOpts) ToRouterUpdateMap() (map[string]any, error) {
	return map[string]any{"router": map[string]any{"external_gateway_info": map[string]any{}}}, nil
}

// parseExternalFixedIPs parses repeated "subnet=<name|id>[,ip-address=<ip>]"
// values into the gateway's external_fixed_ips, resolving subnet names to IDs.
func parseExternalFixedIPs(ctx context.Context, client *gophercloud.ServiceClient, specs []string) ([]routers.ExternalFixedIP, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	out := make([]routers.ExternalFixedIP, 0, len(specs))
	for _, spec := range specs {
		parsed, err := parseFixedIPSpec(spec)
		if err != nil {
			return nil, err
		}
		fixed := routers.ExternalFixedIP{IPAddress: parsed.ipAddress}
		if parsed.subnetSet {
			id, rerr := resolveSubnetID(ctx, client, parsed.subnetRef)
			if rerr != nil {
				return nil, rerr
			}
			fixed.SubnetID = id
		}
		if fixed.SubnetID == "" {
			return nil, fmt.Errorf("--fixed-ip %q requires subnet=", spec)
		}
		out = append(out, fixed)
	}
	return out, nil
}

// fixedIPSpec is one parsed --fixed-ip spec, before the subnet reference is
// resolved to an ID. subnetSet records that the key was present even when its
// value was empty, so an explicit "subnet=" still goes through resolution and
// fails the way it always has rather than being short-circuited here.
type fixedIPSpec struct {
	subnetRef string
	subnetSet bool
	ipAddress string
}

// parseFixedIPSpec parses one --fixed-ip spec. Resolving the subnet name to an
// ID needs a neutron client, so it stays with the caller and this half is pure:
// the key table, its aliases (subnet / subnet-id / subnet_id) and the rejection
// of unknown keys are all exercisable without a mock endpoint.
func parseFixedIPSpec(spec string) (fixedIPSpec, error) {
	var parsed fixedIPSpec
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, err := splitKV(part)
		if err != nil {
			return parsed, fmt.Errorf("parsing --fixed-ip %q: %w", spec, err)
		}
		switch k {
		case "subnet", "subnet-id", "subnet_id":
			parsed.subnetRef, parsed.subnetSet = v, true
		case "ip-address", "ip_address":
			parsed.ipAddress = v
		default:
			return parsed, fmt.Errorf("parsing --fixed-ip %q: unknown key %q", spec, k)
		}
	}
	return parsed, nil
}

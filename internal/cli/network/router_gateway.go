package network

import (
	"context"
	"fmt"
	"io"
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
	r, err := routers.Update(ctx, client, routerID, routers.UpdateOpts{GatewayInfo: &gateway}).Extract()
	if err != nil {
		return fmt.Errorf("setting the external gateway of router %s: %w", routerArg, err)
	}
	fields, values := routerShowFields(r)
	return o.WriteSingle(w, fields, values)
}

// newRouterRemoveGatewayCommand builds "router remove gateway <router>".
func newRouterRemoveGatewayCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "gateway <router>",
		Short: "Clear a router's external gateway",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			return runRouterRemoveGateway(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

// runRouterRemoveGateway clears the gateway. gophercloud's UpdateOpts tags
// GatewayInfo `omitempty`, so a pointer to a zero GatewayInfo would be dropped
// from the body and the request would become a no-op instead of a removal —
// neutron needs an explicit `"external_gateway_info": {}`. The builder below
// writes that key unconditionally.
func runRouterRemoveGateway(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	routerArg string, w io.Writer,
) error {
	routerID, err := resolveRouterID(ctx, client, routerArg)
	if err != nil {
		return err
	}
	r, err := routers.Update(ctx, client, routerID, clearGatewayOpts{}).Extract()
	if err != nil {
		return fmt.Errorf("clearing the external gateway of router %s: %w", routerArg, err)
	}
	fields, values := routerShowFields(r)
	return o.WriteSingle(w, fields, values)
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

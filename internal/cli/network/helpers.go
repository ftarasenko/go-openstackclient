package network

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/layer3/routers"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/security/groups"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/networks"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/ports"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/subnets"
)

// boolPtr returns a pointer to b, for the *bool option fields used across
// neutron opts structs.
func boolPtr(b bool) *bool { return &b }

// enableDisable resolves the mutually-influenced --enable/--disable flag pair
// into an optional *bool. It returns nil when neither was set so the attribute
// is left untouched on update.
func enableDisable(cmd interface{ Changed(string) bool }, enable, disable bool) *bool {
	switch {
	case cmd.Changed("enable") && enable:
		return boolPtr(true)
	case cmd.Changed("disable") && disable:
		return boolPtr(false)
	default:
		return nil
	}
}

// mutuallyExclusive returns an error when both named boolean flags were set on
// the same invocation. It backs the contradictory-flag guards for pairs such as
// --enable/--disable and --ingress/--egress.
func mutuallyExclusive(flags interface{ Changed(string) bool }, a, b string) error {
	if flags.Changed(a) && flags.Changed(b) {
		return fmt.Errorf("--%s and --%s are mutually exclusive", a, b)
	}
	return nil
}

// resolveNetworkID resolves a network name or ID to an ID. It filters by name;
// a single match wins. When no network matches by name the argument is assumed
// to already be an ID and returned unchanged (matching OSC's name-or-ID
// lookup). Multiple name matches are ambiguous and error. The sibling
// resolvers (subnet/router/port/security group) follow the same policy.
func resolveNetworkID(ctx context.Context, client *gophercloud.ServiceClient, nameOrID string) (string, error) {
	return resolveByName(client, "network", nameOrID, func(c *gophercloud.ServiceClient) ([]networks.Network, error) {
		pages, err := networks.List(c, networks.ListOpts{Name: nameOrID}).AllPages(ctx)
		if err != nil {
			return nil, err
		}
		return networks.ExtractNetworks(pages)
	}, func(n networks.Network) string { return n.ID })
}

func resolveSubnetID(ctx context.Context, client *gophercloud.ServiceClient, nameOrID string) (string, error) {
	return resolveByName(client, "subnet", nameOrID, func(c *gophercloud.ServiceClient) ([]subnets.Subnet, error) {
		pages, err := subnets.List(c, subnets.ListOpts{Name: nameOrID}).AllPages(ctx)
		if err != nil {
			return nil, err
		}
		return subnets.ExtractSubnets(pages)
	}, func(s subnets.Subnet) string { return s.ID })
}

func resolveRouterID(ctx context.Context, client *gophercloud.ServiceClient, nameOrID string) (string, error) {
	return resolveByName(client, "router", nameOrID, func(c *gophercloud.ServiceClient) ([]routers.Router, error) {
		pages, err := routers.List(c, routers.ListOpts{Name: nameOrID}).AllPages(ctx)
		if err != nil {
			return nil, err
		}
		return routers.ExtractRouters(pages)
	}, func(r routers.Router) string { return r.ID })
}

func resolvePortID(ctx context.Context, client *gophercloud.ServiceClient, nameOrID string) (string, error) {
	return resolveByName(client, "port", nameOrID, func(c *gophercloud.ServiceClient) ([]ports.Port, error) {
		pages, err := ports.List(c, ports.ListOpts{Name: nameOrID}).AllPages(ctx)
		if err != nil {
			return nil, err
		}
		return ports.ExtractPorts(pages)
	}, func(p ports.Port) string { return p.ID })
}

func resolveSecGroupID(ctx context.Context, client *gophercloud.ServiceClient, nameOrID string) (string, error) {
	return resolveByName(client, "security group", nameOrID, func(c *gophercloud.ServiceClient) ([]groups.SecGroup, error) {
		pages, err := groups.List(c, groups.ListOpts{Name: nameOrID}).AllPages(ctx)
		if err != nil {
			return nil, err
		}
		return groups.ExtractGroups(pages)
	}, func(g groups.SecGroup) string { return g.ID })
}

// resolveByName runs a name-filtered list and applies pickID; it backs every
// neutron name→ID resolver.
func resolveByName[T any](client *gophercloud.ServiceClient, kind, nameOrID string,
	list func(*gophercloud.ServiceClient) ([]T, error), idOf func(T) string,
) (string, error) {
	all, err := list(client)
	if err != nil {
		return "", fmt.Errorf("looking up %s %q: %w", kind, nameOrID, err)
	}
	return pickID(nameOrID, len(all), func(i int) string { return idOf(all[i]) }, kind)
}

// resolveSecGroupIDs resolves a list of security group names or IDs to IDs,
// preserving order.
func resolveSecGroupIDs(ctx context.Context, client *gophercloud.ServiceClient, nameOrIDs []string) ([]string, error) {
	ids := make([]string, 0, len(nameOrIDs))
	for _, nameOrID := range nameOrIDs {
		id, err := resolveSecGroupID(ctx, client, nameOrID)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// pickID applies the shared name-or-ID resolution policy: exactly one match by
// name wins, zero matches falls back to treating the argument as an ID, and
// more than one match is ambiguous.
func pickID(nameOrID string, n int, id func(int) string, kind string) (string, error) {
	switch n {
	case 1:
		return id(0), nil
	case 0:
		return nameOrID, nil
	default:
		return "", fmt.Errorf("%s %q is ambiguous: %d matches, use the ID", kind, nameOrID, n)
	}
}

// parseFixedIP parses an OSC-style --fixed-ip value into a ports.IP. The value
// is a comma-separated list of key=value pairs; recognized keys are "subnet"
// (a subnet name or ID) and "ip-address". The subnet is resolved to an ID.
func parseFixedIP(ctx context.Context, client *gophercloud.ServiceClient, spec string) (ports.IP, error) {
	var ip ports.IP
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, err := splitKV(part)
		if err != nil {
			return ip, fmt.Errorf("parsing --fixed-ip %q: %w", spec, err)
		}
		switch k {
		case "subnet":
			sid, err := resolveSubnetID(ctx, client, v)
			if err != nil {
				return ip, err
			}
			ip.SubnetID = sid
		case "ip-address", "ip_address":
			ip.IPAddress = v
		default:
			return ip, fmt.Errorf("parsing --fixed-ip %q: unknown key %q", spec, k)
		}
	}
	return ip, nil
}

// parseFixedIPFilter parses an OSC-style --fixed-ip value for `port list` into
// a ports.FixedIPOpts filter. The value is a comma-separated list of key=value
// pairs; recognized keys are "subnet" (name or ID, resolved to an ID),
// "ip-address" and "ip-substring". Unlike parseFixedIP (which builds a ports.IP
// for create), this targets the neutron fixed_ips query filter.
func parseFixedIPFilter(ctx context.Context, client *gophercloud.ServiceClient, spec string) (ports.FixedIPOpts, error) {
	var f ports.FixedIPOpts
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, err := splitKV(part)
		if err != nil {
			return f, fmt.Errorf("parsing --fixed-ip %q: %w", spec, err)
		}
		switch k {
		case "subnet":
			sid, err := resolveSubnetID(ctx, client, v)
			if err != nil {
				return f, err
			}
			f.SubnetID = sid
		case "ip-address", "ip_address":
			f.IPAddress = v
		case "ip-substring", "ip_substring":
			f.IPAddressSubstr = v
		default:
			return f, fmt.Errorf("parsing --fixed-ip %q: unknown key %q", spec, k)
		}
	}
	if f == (ports.FixedIPOpts{}) {
		return f, fmt.Errorf("--fixed-ip %q requires ip-address, ip-substring, or subnet", spec)
	}
	return f, nil
}

// parseAllocationPool parses an OSC-style --allocation-pool value
// ("start=<ip>,end=<ip>") into a subnets.AllocationPool.
func parseAllocationPool(spec string) (subnets.AllocationPool, error) {
	var p subnets.AllocationPool
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, err := splitKV(part)
		if err != nil {
			return p, fmt.Errorf("parsing --allocation-pool %q: %w", spec, err)
		}
		switch k {
		case "start":
			p.Start = v
		case "end":
			p.End = v
		default:
			return p, fmt.Errorf("parsing --allocation-pool %q: unknown key %q", spec, k)
		}
	}
	if p.Start == "" || p.End == "" {
		return p, fmt.Errorf("--allocation-pool %q requires both start= and end=", spec)
	}
	return p, nil
}

// parsePortRange parses a --dst-port value ("<port>" or "<min>:<max>") into a
// minimum/maximum pair.
func parsePortRange(spec string) (lo, hi int, err error) {
	loStr, hiStr, found := strings.Cut(spec, ":")
	lo, err = strconv.Atoi(strings.TrimSpace(loStr))
	if err != nil {
		return 0, 0, fmt.Errorf("parsing --dst-port %q: %w", spec, err)
	}
	if !found {
		return lo, lo, nil
	}
	hi, err = strconv.Atoi(strings.TrimSpace(hiStr))
	if err != nil {
		return 0, 0, fmt.Errorf("parsing --dst-port %q: %w", spec, err)
	}
	return lo, hi, nil
}

// splitKV splits a "key=value" pair on the first '='.
func splitKV(s string) (string, string, error) {
	k, v, found := strings.Cut(s, "=")
	if !found {
		return "", "", fmt.Errorf("expected key=value, got %q", s)
	}
	return strings.TrimSpace(k), v, nil
}

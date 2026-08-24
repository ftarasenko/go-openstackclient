package network

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/subnets"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// subnet unset removes individual entries from a subnet's list attributes, the
// same shape as `port unset`: neutron's PUT /subnets/<id> replaces a list
// wholesale, so each removal reads the subnet, filters the list, and writes back
// the remainder.
//
// The read-modify-write is guarded with the subnet's revision_number (neutron's
// If-Match), so a concurrent change is rejected rather than silently clobbered
// by a list computed from stale data. Flag names follow upstream OSC; UNVERIFIED
// against KeyStack docs (https://docs.keystack.ru/ returned HTTP 403 at
// implementation time).
type subnetUnsetFlags struct {
	allocationPool []string
	dnsNameserver  []string
	hostRoute      []string
	serviceType    []string
	gateway        bool
}

func newSubnetUnsetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &subnetUnsetFlags{}
	cmd := &cobra.Command{
		Use:   "unset <subnet>",
		Short: "Remove individual allocation pools, nameservers, host routes or service types from a subnet",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			if len(f.allocationPool) == 0 && len(f.dnsNameserver) == 0 &&
				len(f.hostRoute) == 0 && len(f.serviceType) == 0 && !f.gateway {
				return fmt.Errorf("subnet unset requires at least one attribute flag")
			}
			ctx := cmd.Context()
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			return runSubnetUnset(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringArrayVar(&f.allocationPool, "allocation-pool", nil,
		"allocation pool to remove as start=<ip>,end=<ip> (repeatable)")
	fl.StringArrayVar(&f.dnsNameserver, flagDNSNameserver, nil, "DNS nameserver to remove (repeatable)")
	fl.StringArrayVar(&f.hostRoute, "host-route", nil,
		"host route to remove as destination=<cidr>,gateway=<ip> (repeatable)")
	fl.StringArrayVar(&f.serviceType, "service-type", nil, "service type to remove (repeatable)")
	fl.BoolVar(&f.gateway, "gateway", false, "clear the subnet's gateway IP")
	return cmd
}

func runSubnetUnset(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	nameOrID string, f *subnetUnsetFlags, w io.Writer,
) error {
	id, err := resolveSubnetID(ctx, client, nameOrID)
	if err != nil {
		return err
	}
	current, err := subnets.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("reading subnet %s before unset: %w", nameOrID, err)
	}

	revision := current.RevisionNumber
	opts := subnets.UpdateOpts{RevisionNumber: &revision}

	if len(f.allocationPool) > 0 {
		remove, perr := parseAllocationPools(f.allocationPool)
		if perr != nil {
			return perr
		}
		kept := make([]subnets.AllocationPool, 0, len(current.AllocationPools))
		for _, have := range current.AllocationPools {
			if !slices.Contains(remove, have) {
				kept = append(kept, have)
			}
		}
		opts.AllocationPools = kept
	}

	if len(f.dnsNameserver) > 0 {
		kept := make([]string, 0, len(current.DNSNameservers))
		for _, have := range current.DNSNameservers {
			if !slices.Contains(f.dnsNameserver, have) {
				kept = append(kept, have)
			}
		}
		opts.DNSNameservers = &kept
	}

	if len(f.hostRoute) > 0 {
		remove, perr := parseHostRoutes(f.hostRoute)
		if perr != nil {
			return perr
		}
		kept := make([]subnets.HostRoute, 0, len(current.HostRoutes))
		for _, have := range current.HostRoutes {
			if !slices.Contains(remove, have) {
				kept = append(kept, have)
			}
		}
		opts.HostRoutes = &kept
	}

	if len(f.serviceType) > 0 {
		kept := make([]string, 0, len(current.ServiceTypes))
		for _, have := range current.ServiceTypes {
			if !slices.Contains(f.serviceType, have) {
				kept = append(kept, have)
			}
		}
		opts.ServiceTypes = &kept
	}

	if f.gateway {
		// Neutron drops the gateway on an explicit null. GatewayIP is a *string so
		// "" stays distinguishable from "unchanged", and gophercloud's
		// ToSubnetUpdateMap rewrites the "" to null on the way out.
		empty := ""
		opts.GatewayIP = &empty
	}

	s, err := subnets.Update(ctx, client, id, opts).Extract()
	if err != nil {
		return fmt.Errorf("updating subnet %s: %w", nameOrID, err)
	}
	fields, values := subnetShowFields(s)
	return o.WriteSingle(w, fields, values)
}

// parseAllocationPools parses repeated "start=<ip>,end=<ip>" values, reusing the
// single-pool parser "subnet create" already registers.
func parseAllocationPools(specs []string) ([]subnets.AllocationPool, error) {
	pools := make([]subnets.AllocationPool, 0, len(specs))
	for _, spec := range specs {
		pool, err := parseAllocationPool(spec)
		if err != nil {
			return nil, err
		}
		pools = append(pools, pool)
	}
	return pools, nil
}

// parseHostRoutes parses repeated "destination=<cidr>,gateway=<ip>" values.
func parseHostRoutes(specs []string) ([]subnets.HostRoute, error) {
	routes := make([]subnets.HostRoute, 0, len(specs))
	for _, spec := range specs {
		var route subnets.HostRoute
		for _, part := range strings.Split(spec, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			k, v, err := splitKV(part)
			if err != nil {
				return nil, fmt.Errorf("parsing --host-route %q: %w", spec, err)
			}
			switch k {
			case "destination":
				route.DestinationCIDR = v
			case "gateway", "nexthop":
				route.NextHop = v
			default:
				return nil, fmt.Errorf("parsing --host-route %q: unknown key %q", spec, k)
			}
		}
		if route.DestinationCIDR == "" || route.NextHop == "" {
			return nil, fmt.Errorf("--host-route %q needs both destination= and gateway=", spec)
		}
		routes = append(routes, route)
	}
	return routes, nil
}

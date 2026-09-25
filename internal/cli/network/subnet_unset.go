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
	extraProperty  []string
	tagWriteFlags
}

// changesAttrs reports whether any flag other than the tag pair was given.
func (f *subnetUnsetFlags) changesAttrs() bool {
	return len(f.allocationPool) > 0 || len(f.dnsNameserver) > 0 || len(f.hostRoute) > 0 ||
		len(f.serviceType) > 0 || f.gateway || len(f.extraProperty) > 0
}

func newSubnetUnsetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &subnetUnsetFlags{}
	cmd := &cobra.Command{
		Use:   "unset <subnet>",
		Short: "Remove individual allocation pools, nameservers, host routes, service types or tags from a subnet",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			if !f.changesAttrs() && !f.given() {
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
	fl.StringArrayVar(&f.allocationPool, subnetFlagAllocationPool, nil,
		"allocation pool to remove as start=<ip>,end=<ip> (repeatable)")
	fl.StringArrayVar(&f.dnsNameserver, flagDNSNameserver, nil, "DNS nameserver to remove (repeatable)")
	fl.StringArrayVar(&f.hostRoute, subnetFlagHostRoute, nil,
		"host route to remove as destination=<cidr>,gateway=<ip> (repeatable)")
	fl.StringArrayVar(&f.serviceType, subnetFlagServiceType, nil, "service type to remove (repeatable)")
	fl.BoolVar(&f.gateway, subnetFlagGateway, false, "clear the subnet's gateway IP")
	bindExtraPropertyUnsetFlag(fl, &f.extraProperty)
	bindTagUnsetFlags(cmd, &f.tagWriteFlags, "subnet")
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

	s := current
	if f.changesAttrs() {
		revision := current.RevisionNumber
		opts := subnets.UpdateOpts{RevisionNumber: &revision}
		attrs, err := subnetUnsetLists(f, current, &opts)
		if err != nil {
			return err
		}
		if f.gateway {
			// Neutron drops the gateway on an explicit null. GatewayIP is a *string so
			// "" stays distinguishable from "unchanged", and gophercloud's
			// ToSubnetUpdateMap rewrites the "" to null on the way out.
			empty := ""
			opts.GatewayIP = &empty
		}
		extra, err := parseExtraProperties(f.extraProperty, true)
		if err != nil {
			return err
		}
		attrs = mergeAttrs(attrs, extra)
		if s, err = subnets.Update(ctx, client, id, withSubnetUpdateAttrs(opts, attrs)).Extract(); err != nil {
			return fmt.Errorf("updating subnet %s: %w", nameOrID, err)
		}
	}
	// Tags are a sub-resource; a tags-only unset sends no subnet PUT, as upstream.
	if s.Tags, err = applyTagsForUnset(ctx, client, tagResourceSubnets, id, s.Tags, &f.tagWriteFlags); err != nil {
		return err
	}
	fields, values := subnetShowFields(s)
	return o.WriteSingle(w, fields, values)
}

// subnetUnsetLists computes the surviving entries of each list a flag names.
// As upstream (_update_arguments), naming an entry the subnet does not carry
// is an error rather than a silent no-op. An emptied allocation-pool list is
// returned as a body attribute, because UpdateOpts drops an empty slice there.
func subnetUnsetLists(f *subnetUnsetFlags, current *subnets.Subnet, opts *subnets.UpdateOpts) (map[string]any, error) {
	var attrs map[string]any
	if len(f.allocationPool) > 0 {
		remove, err := parseAllocationPools(f.allocationPool)
		if err != nil {
			return nil, err
		}
		kept, err := removeEach(current.AllocationPools, remove, subnetFlagAllocationPool,
			func(p subnets.AllocationPool) string { return "start=" + p.Start + ",end=" + p.End })
		if err != nil {
			return nil, err
		}
		if len(kept) == 0 {
			attrs = map[string]any{"allocation_pools": []subnets.AllocationPool{}}
		} else {
			opts.AllocationPools = kept
		}
	}
	if len(f.dnsNameserver) > 0 {
		kept, err := removeEach(current.DNSNameservers, f.dnsNameserver, flagDNSNameserver, subnetListEntry)
		if err != nil {
			return nil, err
		}
		opts.DNSNameservers = &kept
	}
	if len(f.hostRoute) > 0 {
		remove, err := parseHostRoutes(f.hostRoute)
		if err != nil {
			return nil, err
		}
		kept, err := removeEach(current.HostRoutes, remove, subnetFlagHostRoute,
			func(r subnets.HostRoute) string { return "destination=" + r.DestinationCIDR + ",gateway=" + r.NextHop })
		if err != nil {
			return nil, err
		}
		opts.HostRoutes = &kept
	}
	if len(f.serviceType) > 0 {
		kept, err := removeEach(current.ServiceTypes, f.serviceType, subnetFlagServiceType, subnetListEntry)
		if err != nil {
			return nil, err
		}
		opts.ServiceTypes = &kept
	}
	return attrs, nil
}

// subnetListEntry formats a plain string list entry for removeEach errors.
func subnetListEntry(s string) string { return s }

// removeEach removes one occurrence of every entry of remove from a copy of
// have, failing on the first entry have does not contain. The result is never
// nil, so an emptied list is sent as [].
func removeEach[T comparable](have, remove []T, option string, format func(T) string) ([]T, error) {
	out := slices.Clone(have)
	if out == nil {
		out = []T{}
	}
	for _, r := range remove {
		i := slices.Index(out, r)
		if i < 0 {
			return nil, fmt.Errorf("subnet does not contain %s %s", option, format(r))
		}
		out = slices.Delete(out, i, i+1)
	}
	return out, nil
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

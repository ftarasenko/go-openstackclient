package loadbalancer

import (
	"context"
	"fmt"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/loadbalancer/v2/loadbalancers"
	"github.com/spf13/pflag"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/resolve"
)

// changedSet records which flags an invocation actually gave, so the sparse-update
// seams take plain data rather than a *pflag.FlagSet. Tests construct one
// directly.
type changedSet map[string]bool

func changedFlags(fl *pflag.FlagSet) changedSet {
	set := make(changedSet)
	fl.VisitAll(func(f *pflag.Flag) {
		if f.Changed {
			set[f.Name] = true
		}
	})
	return set
}

// triState folds an --enable/--disable pair into an optional *bool: nil when
// neither was given, so an attribute nobody mentioned is left untouched.
func triState(fl *pflag.FlagSet, enable, disable bool) *bool {
	switch {
	case fl.Changed("enable") && enable:
		return gophercloud.Enabled
	case fl.Changed("disable") && disable:
		return gophercloud.Disabled
	}
	return nil
}

// lbRefs are the cross-service references a loadbalancer command may name: a
// keystone project and neutron subnet/network/port. resolvedLBRefs holds the IDs
// they map to.
type lbRefs struct {
	project       string
	projectDomain string
	vipSubnet     string
	vipNetwork    string
	vipPort       string
}

type resolvedLBRefs struct {
	projectID    string
	vipSubnetID  string
	vipNetworkID string
	vipPortID    string
}

// resolveLBRefs turns names into IDs, deriving each secondary service client only
// if a reference actually needs it: an empty ref stays empty and a UUID passes
// through untouched, so the common case costs no extra round trip.
func resolveLBRefs(ctx context.Context, session *auth.Client, refs lbRefs) (resolvedLBRefs, error) {
	out := resolvedLBRefs{
		projectID:    refs.project,
		vipSubnetID:  refs.vipSubnet,
		vipNetworkID: refs.vipNetwork,
		vipPortID:    refs.vipPort,
	}
	needsLookup := func(ref string) bool { return ref != "" && !resolve.IsUUID(ref) }

	if needsLookup(refs.project) {
		identity, err := session.Identity()
		if err != nil {
			return out, err
		}
		out.projectID, err = resolve.ProjectIDInDomain(ctx, identity, refs.project, refs.projectDomain)
		if err != nil {
			return out, err
		}
	}

	if !needsLookup(refs.vipSubnet) && !needsLookup(refs.vipNetwork) && !needsLookup(refs.vipPort) {
		return out, nil
	}
	network, err := session.Network()
	if err != nil {
		return out, err
	}
	if needsLookup(refs.vipNetwork) {
		out.vipNetworkID, err = resolve.NetworkID(ctx, network, refs.vipNetwork)
		if err != nil {
			return out, err
		}
	}
	if needsLookup(refs.vipSubnet) {
		out.vipSubnetID, err = resolve.SubnetID(ctx, network, refs.vipSubnet)
		if err != nil {
			return out, err
		}
	}
	if needsLookup(refs.vipPort) {
		out.vipPortID, err = resolve.PortID(ctx, network, refs.vipPort)
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

// resolveLoadBalancerID turns a load balancer name or ID into an ID. Octavia's
// ?name= filter is exact, and names are not unique across a project, so more than
// one match is rejected rather than picking arbitrarily.
func resolveLoadBalancerID(ctx context.Context, client *gophercloud.ServiceClient, ref string) (string, error) {
	if ref == "" || resolve.IsUUID(ref) {
		return ref, nil
	}
	pages, err := loadbalancers.List(client, loadbalancers.ListOpts{Name: ref}).AllPages(ctx)
	if err != nil {
		return "", fmt.Errorf("looking up load balancer %q: %w", ref, err)
	}
	all, err := loadbalancers.ExtractLoadBalancers(pages)
	if err != nil {
		return "", fmt.Errorf("looking up load balancer %q: %w", ref, err)
	}
	switch len(all) {
	case 0:
		// Consistent with the rest of koc: assume the ref is already an ID and let
		// the server produce the 404.
		return ref, nil
	case 1:
		return all[0].ID, nil
	default:
		return "", fmt.Errorf("load balancer name %q is ambiguous: %d matches, use the ID", ref, len(all))
	}
}

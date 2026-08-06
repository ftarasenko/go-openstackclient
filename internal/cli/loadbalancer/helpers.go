package loadbalancer

import (
	"context"
	"fmt"
	"strings"

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
	return resolveByName("load balancer", ref, func() ([]string, error) {
		pages, err := loadbalancers.List(client, loadbalancers.ListOpts{Name: ref}).AllPages(ctx)
		if err != nil {
			return nil, err
		}
		all, err := loadbalancers.ExtractLoadBalancers(pages)
		if err != nil {
			return nil, err
		}
		ids := make([]string, 0, len(all))
		for _, lb := range all {
			ids = append(ids, lb.ID)
		}
		return ids, nil
	})
}

// The assign* helpers below build the sparse UpdateOpts every octavia noun uses:
// each field is a pointer, so an attribute is sent only when its flag was
// actually given and `set --name x` cannot blank an unrelated one. They also flip
// a shared "touched" bit, so a set with no attribute flags can be rejected before
// any request is made.

func assignString(changed changedSet, flag, value string, dst **string, touched *bool) {
	if !changed[flag] {
		return
	}
	v := value
	*dst = &v
	*touched = true
}

func assignInt(changed changedSet, flag string, value int, dst **int, touched *bool) {
	if !changed[flag] {
		return
	}
	v := value
	*dst = &v
	*touched = true
}

func assignStrings(changed changedSet, flag string, value []string, dst **[]string, touched *bool) {
	if !changed[flag] {
		return
	}
	v := value
	*dst = &v
	*touched = true
}

func assignBool(changed changedSet, flag string, value bool, dst **bool, touched *bool) {
	if !changed[flag] {
		return
	}
	v := value
	*dst = &v
	*touched = true
}

// setIfNonZero fills a *int create option only for a non-zero value, so an
// unset numeric flag leaves octavia's own default in place rather than asserting
// zero.
func setIfNonZero(dst **int, value int) {
	if value == 0 {
		return
	}
	v := value
	*dst = &v
}

// parseKeyValues turns repeated "name=value" flag values into a map, naming the
// flag in any error so the message points at what the operator typed.
func parseKeyValues(pairs []string, flag string) (map[string]string, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(pairs))
	for _, pair := range pairs {
		k, v, found := strings.Cut(pair, "=")
		if !found || strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("parsing %s %q: expected name=value", flag, pair)
		}
		out[strings.TrimSpace(k)] = v
	}
	return out, nil
}

// resolveByName is the shared name→ID policy for octavia nouns whose collection
// endpoint filters exactly on name: a UUID passes through with no call, one match
// wins, zero matches falls back to treating the ref as an ID (letting the server
// produce the 404), and several matches is ambiguous.
func resolveByName(kind, ref string, list func() ([]string, error)) (string, error) {
	if ref == "" || resolve.IsUUID(ref) {
		return ref, nil
	}
	ids, err := list()
	if err != nil {
		return "", fmt.Errorf("looking up %s %q: %w", kind, ref, err)
	}
	switch len(ids) {
	case 0:
		return ref, nil
	case 1:
		return ids[0], nil
	default:
		return "", fmt.Errorf("%s name %q is ambiguous: %d matches, use the ID", kind, ref, len(ids))
	}
}

// parseCommaKeyValues parses one comma-separated "k=v,k=v" flag value, the shape
// OSC uses for compound options (--session-persistence, --l7rule).
func parseCommaKeyValues(spec, flag string) (map[string]string, error) {
	out := make(map[string]string)
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, found := strings.Cut(part, "=")
		if !found || strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("parsing %s %q: expected key=value pairs", flag, spec)
		}
		out[strings.TrimSpace(k)] = v
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("parsing %s %q: no key=value pairs given", flag, spec)
	}
	return out, nil
}

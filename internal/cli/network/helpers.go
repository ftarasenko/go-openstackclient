package network

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/ports"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/subnets"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/resolve"
	networkapi "github.com/ftarasenko/go-openstackclient/pkg/openstack/network"
	osresolve "github.com/ftarasenko/go-openstackclient/pkg/openstack/resolve"
)

// boolPtr returns a pointer to b, for the *bool option fields used across
// neutron opts structs.
func boolPtr(b bool) *bool { return &b }

// enableDisable resolves a positive/negative boolean flag pair into an optional
// *bool: non-nil only when one of the two was actually given, so an attribute
// nobody mentioned is left untouched on update. onName/offName default to
// "enable"/"disable" when omitted, which is the pair most neutron nouns use;
// pass them explicitly for the others (--share/--no-share,
// --default/--no-default, --dhcp/--no-dhcp).
func enableDisable(cmd interface{ Changed(string) bool }, on, off bool, names ...string) *bool {
	onName, offName := "enable", "disable"
	if len(names) == 2 {
		onName, offName = names[0], names[1]
	}
	switch {
	case cmd.Changed(onName) && on:
		return boolPtr(true)
	case cmd.Changed(offName) && off:
		return boolPtr(false)
	default:
		return nil
	}
}

// resolveProjectRef turns a --project value into a project ID, deriving the
// identity client from the authenticated session only when one is actually
// needed: an empty ref yields "" (no filter) and a UUID passes straight through,
// so the common cases cost no keystone call. domainRef, when set, disambiguates
// a project name that exists in more than one domain.
func resolveProjectRef(ctx context.Context, session *auth.Client, ref, domainRef string) (string, error) {
	if ref == "" || resolve.IsUUID(ref) {
		return ref, nil
	}
	identity, err := session.Identity()
	if err != nil {
		return "", err
	}
	return resolve.ProjectIDInDomain(ctx, identity, ref, domainRef)
}

// mutuallyExclusive returns an error when both named boolean flags were set on
// the same invocation. It backs the contradictory-flag guards for pairs such as
// --enable/--disable and --ingress/--egress.
// keepUnmatched returns the elements of have that no removal spec matches,
// preserving order. Every "unset" verb needs it: neutron's update replaces the
// whole list, so the survivors are computed client-side from what was just read.
func keepUnmatched[T any](have []T, matches func(T) bool) []T {
	kept := make([]T, 0, len(have))
	for _, v := range have {
		if !matches(v) {
			kept = append(kept, v)
		}
	}
	return kept
}

func mutuallyExclusive(flags interface{ Changed(string) bool }, a, b string) error {
	if flags.Changed(a) && flags.Changed(b) {
		return fmt.Errorf("--%s and --%s are mutually exclusive", a, b)
	}
	return nil
}

// resolveNetworkID and its siblings resolve a name or ID to an ID through
// pkg/openstack/network's strict lookups: a UUID passes through with no
// request, otherwise a name-filtered list must match exactly once. The CLI
// keeps its own ambiguity wording (cliLookup); everything else in the error
// is the lookup's.
func resolveNetworkID(ctx context.Context, client *gophercloud.ServiceClient, nameOrID string) (string, error) {
	return cliLookup(networkapi.FindNetworkID(ctx, client, nameOrID))
}

func resolveSubnetID(ctx context.Context, client *gophercloud.ServiceClient, nameOrID string) (string, error) {
	return cliLookup(networkapi.FindSubnetID(ctx, client, nameOrID))
}

func resolveRouterID(ctx context.Context, client *gophercloud.ServiceClient, nameOrID string) (string, error) {
	return cliLookup(networkapi.FindRouterID(ctx, client, nameOrID))
}

func resolvePortID(ctx context.Context, client *gophercloud.ServiceClient, nameOrID string) (string, error) {
	return cliLookup(networkapi.FindPortID(ctx, client, nameOrID))
}

// resolveAddressScopeID and resolveAddressGroupID resolve a name or ID to an ID.
// Neither noun had a resolver, so `address scope show <name>` put the name
// straight into the URL and neutron answered 404 for a resource that plainly
// existed in `list`.
func resolveAddressScopeID(ctx context.Context, client *gophercloud.ServiceClient, nameOrID string) (string, error) {
	return cliLookup(networkapi.FindAddressScopeID(ctx, client, nameOrID))
}

func resolveAddressGroupID(ctx context.Context, client *gophercloud.ServiceClient, nameOrID string) (string, error) {
	return cliLookup(networkapi.FindAddressGroupID(ctx, client, nameOrID))
}

func resolveSecGroupID(ctx context.Context, client *gophercloud.ServiceClient, nameOrID string) (string, error) {
	return cliLookup(networkapi.FindSecurityGroupID(ctx, client, nameOrID))
}

// relabel re-wraps a pkg/openstack/network error, which names neutron's ID,
// with the CLI's text naming the operator's reference.
func relabel(err error, format string, args ...any) error {
	return fmt.Errorf(format+": %w", append(args, errors.Unwrap(err))...)
}

// cliLookup words an ambiguous match as the CLI always has (pickID).
func cliLookup(id string, err error) (string, error) {
	var amb *osresolve.AmbiguousError
	if errors.As(err, &amb) {
		return pickID(amb.Ref, amb.Count, nil, amb.Kind)
	}
	return id, err
}

// resolveByName backs every neutron name→ID resolver. A UUID is returned as-is
// with no request: every neutron resource this package resolves has a UUID ID,
// so no GET-by-ID step is needed to recognise one. Anything else is looked up
// with the name-filtered list and settled by pickID — one match wins, none is
// an error, several are ambiguous.
//
// The zero-match error mirrors upstream openstacksdk's find_* ("No Network
// found for typo"). koc used to fall back to the literal reference instead, so
// `koc network delete typo` sent DELETE /v2.0/networks/typo and surfaced
// neutron's bare 404.
func resolveByName[T any](client *gophercloud.ServiceClient, kind, nameOrID string,
	list func(*gophercloud.ServiceClient) ([]T, error), idOf func(T) string,
) (string, error) {
	if resolve.IsUUID(nameOrID) {
		return nameOrID, nil
	}
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

// pickID settles a name (or address) lookup that has already run: exactly one
// match wins, zero matches is a "no <kind> found" error, and more than one is
// ambiguous. UUIDs never reach it — the callers short-circuit them.
func pickID(nameOrID string, n int, id func(int) string, kind string) (string, error) {
	switch n {
	case 1:
		return id(0), nil
	case 0:
		return "", fmt.Errorf("no %s found for %q", kind, nameOrID)
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

// fieldNames keeps a field builder's columns and drops its values. Builders
// called on a zero value give the catalog -c is checked against before a write.
func fieldNames(fields []string, _ []any) []string { return fields }

package network

import (
	"context"
	"fmt"
	"io"
	"maps"
	"slices"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/ports"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// port unset removes individual entries from a port's list attributes, which
// neutron itself cannot do: PUT /ports/<id> replaces a list wholesale. So each
// removal reads the port, filters the list, and writes back the remainder.
//
// That read-modify-write is not atomic. A concurrent change to the same port
// between the GET and the PUT would be overwritten, so the PUT is guarded with
// the port's revision_number (neutron's If-Match) and fails rather than
// clobbering. Flag names follow upstream OSC; UNVERIFIED against KeyStack docs
// (https://docs.keystack.ru/ returned HTTP 403 at implementation time).
type portUnsetFlags struct {
	fixedIP         []string
	securityGroup   []string
	allowedAddress  []string
	bindingProfile  []string
	host            bool
	device          bool
	deviceOwner     bool
	qosPolicy       bool
	dataPlaneStatus bool
	numaPolicy      bool
	hints           bool
	pvlanCommunity  bool
	extraProperty   []string
	tagWriteFlags
}

// empty reports whether no unset flag at all was given.
func (f *portUnsetFlags) empty() bool {
	return len(f.fixedIP) == 0 && len(f.securityGroup) == 0 && len(f.allowedAddress) == 0 &&
		len(f.bindingProfile) == 0 && len(f.extraProperty) == 0 &&
		!f.host && !f.device && !f.deviceOwner && !f.qosPolicy && !f.dataPlaneStatus &&
		!f.numaPolicy && !f.hints && !f.pvlanCommunity && !f.given()
}

func newPortUnsetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &portUnsetFlags{}
	cmd := &cobra.Command{
		Use:   "unset <port>",
		Short: "Unset port properties",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			if f.empty() {
				return fmt.Errorf("port unset requires at least one attribute flag")
			}
			ctx := cmd.Context()
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			return runPortUnset(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringArrayVar(&f.fixedIP, flagFixedIP, nil,
		"fixed IP to remove as subnet=<name|id>,ip-address=<ip> (repeatable)")
	fl.StringArrayVar(&f.securityGroup, flagSecurityGroup, nil, "security group to remove (name or ID, repeatable)")
	fl.StringArrayVar(&f.allowedAddress, flagAllowedAddress, nil,
		"allowed address pair to remove as ip-address=<ip>[,mac-address=<mac>] (repeatable)")
	fl.StringArrayVar(&f.bindingProfile, flagPortBindingProfile, nil, "binding:profile key to remove (repeatable)")
	fl.BoolVar(&f.host, flagPortHost, false, "clear the port's binding host ID")
	fl.BoolVar(&f.device, flagPortDevice, false, "clear the port's device ID")
	fl.BoolVar(&f.deviceOwner, flagDeviceOwner, false, "clear the port's device owner")
	fl.BoolVar(&f.qosPolicy, flagQoSPolicy, false, "detach the port's QoS policy")
	fl.BoolVar(&f.dataPlaneStatus, flagPortDataPlaneStatus, false, "clear the port's data plane status")
	fl.BoolVar(&f.numaPolicy, flagPortNUMAPolicy, false,
		"clear the port's NUMA affinity policy (requires the port-numa-affinity-policy extension)")
	fl.BoolVar(&f.hints, flagPortHints, false, "clear the port's hints (requires the port-hints extension)")
	fl.BoolVar(&f.pvlanCommunity, flagPortPVLANCommunity, false,
		"clear the port's PVLAN community (requires the pvlan extension)")
	bindExtraPropertyUnsetFlag(fl, &f.extraProperty)
	bindTagUnsetFlags(cmd, &f.tagWriteFlags, "port")
	return cmd
}

func runPortUnset(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	nameOrID string, f *portUnsetFlags, w io.Writer,
) error {
	id, err := resolvePortID(ctx, client, nameOrID)
	if err != nil {
		return err
	}
	current, err := getPort(ctx, client, id)
	if err != nil {
		return fmt.Errorf("reading port %s before unset: %w", nameOrID, err)
	}

	opts := ports.UpdateOpts{}
	// Pin the update to the revision we just read, so a concurrent change to the
	// port is rejected by neutron instead of being silently overwritten by the
	// list we computed from stale data.
	revision := current.RevisionNumber
	opts.RevisionNumber = &revision

	changed, err := portUnsetLists(ctx, client, f, current, &opts)
	if err != nil {
		return err
	}
	empty := ""
	if f.device {
		opts.DeviceID = &empty
		changed = true
	}
	if f.deviceOwner {
		opts.DeviceOwner = &empty
		changed = true
	}
	attrs, err := portUnsetAttrs(f, current)
	if err != nil {
		return err
	}
	changed = changed || len(attrs) > 0
	return updatePort(ctx, client, o, nameOrID, id, opts, attrs, changed, current, applyTagsForUnset, &f.tagWriteFlags, w)
}

// portUnsetLists removes the entries each list flag names. As upstream
// (port.py UnsetPort), every spec must name an entry the port carries: one
// that does not is an error ("Port does not contain …") rather than a silent
// no-op, and each spec removes exactly one entry.
func portUnsetLists(ctx context.Context, client *gophercloud.ServiceClient, f *portUnsetFlags,
	current *portExt, opts *ports.UpdateOpts,
) (bool, error) {
	changed := false
	if len(f.fixedIP) > 0 {
		remove, err := buildFixedIPs(ctx, client, f.fixedIP)
		if err != nil {
			return false, err
		}
		kept, err := removeEachMatch(current.FixedIPs, remove, f.fixedIP, fixedIPMatches, "fixed-ip")
		if err != nil {
			return false, err
		}
		opts.FixedIPs = kept
		changed = true
	}

	if len(f.securityGroup) > 0 {
		removeIDs, err := resolveSecGroupIDs(ctx, client, f.securityGroup)
		if err != nil {
			return false, err
		}
		kept, err := removeEachMatch(current.SecurityGroups, removeIDs, f.securityGroup,
			func(have, want string) bool { return have == want }, "security group")
		if err != nil {
			return false, err
		}
		opts.SecurityGroups = &kept
		changed = true
	}

	if len(f.allowedAddress) > 0 {
		remove, err := parseAddressPairs(f.allowedAddress)
		if err != nil {
			return false, err
		}
		kept, err := removeEachMatch(current.AllowedAddressPairs, remove, f.allowedAddress,
			addressPairMatches, "allowed-address-pair")
		if err != nil {
			return false, err
		}
		opts.AllowedAddressPairs = &kept
		changed = true
	}
	return changed, nil
}

// removeEachMatch removes, from a copy of have, the one entry each element of
// remove matches. A spec that matches nothing fails with upstream's wording
// ("port does not contain <noun> <spec>"); one that matches several — possible
// only when it leaves a key out — fails too, rather than guessing which entry
// was meant. specs are the operator's spellings, quoted in the errors. The
// result is never nil, so an emptied list is sent as [].
func removeEachMatch[T any](have, remove []T, specs []string, matches func(have, want T) bool,
	noun string,
) ([]T, error) {
	out := slices.Clone(have)
	if out == nil {
		out = []T{}
	}
	for i, want := range remove {
		hit, count := -1, 0
		for j, h := range out {
			if matches(h, want) {
				hit, count = j, count+1
			}
		}
		switch {
		case count == 0:
			return nil, fmt.Errorf("port does not contain %s %s", noun, specs[i])
		case count > 1:
			return nil, fmt.Errorf("%s %s matches %d entries of the port: give every key to name one", noun, specs[i], count)
		}
		out = slices.Delete(out, hit, hit+1)
	}
	return out, nil
}

// portUnsetAttrs builds the extension attributes unset clears. qos_policy_id,
// data_plane_status, numa_affinity_policy, hints, pvlan_community and
// --extra-property go out as null, as upstream sends them; binding:host_id
// keeps koc's empty string.
func portUnsetAttrs(f *portUnsetFlags, current *portExt) (map[string]any, error) {
	attrs := map[string]any{}
	if len(f.bindingProfile) > 0 {
		profile := maps.Clone(current.BindingProfile)
		if profile == nil {
			profile = map[string]any{}
		}
		for _, key := range f.bindingProfile {
			if _, ok := profile[key]; !ok {
				return nil, fmt.Errorf("port does not contain binding:profile key %q", key)
			}
			delete(profile, key)
		}
		attrs["binding:profile"] = profile
	}
	if f.host {
		attrs["binding:host_id"] = ""
	}
	if f.qosPolicy {
		attrs["qos_policy_id"] = nil
	}
	if f.dataPlaneStatus {
		attrs["data_plane_status"] = nil
	}
	if f.numaPolicy {
		attrs["numa_affinity_policy"] = nil
	}
	if f.hints {
		attrs["hints"] = nil
	}
	if f.pvlanCommunity {
		attrs["pvlan_community"] = nil
	}
	extra, err := parseExtraProperties(f.extraProperty, true)
	if err != nil {
		return nil, err
	}
	return mergeAttrs(attrs, extra), nil
}

// fixedIPMatches reports whether have is the fixed IP the removal spec want
// names. Upstream removes the spec as a dict ({subnet_id, ip_address}, the
// subnet already resolved to its ID) by equality, and neutron always returns
// both keys, so upstream only ever matches a spec that gives both. koc compares
// the keys the spec gives — subnet= alone names the port's one IP on that
// subnet, ip-address= alone its one entry with that address — and
// removeEachMatch rejects a partial spec that is ambiguous.
func fixedIPMatches(have, want ports.IP) bool {
	return (want.SubnetID == "" || want.SubnetID == have.SubnetID) &&
		(want.IPAddress == "" || want.IPAddress == have.IPAddress)
}

// addressPairMatches applies the same rule to allowed address pairs. Neutron
// fills an omitted mac-address in with the port's own MAC, so upstream's
// ip-address-only spec never equals a stored pair; koc matches it on the IP.
func addressPairMatches(have, want ports.AddressPair) bool {
	return want.IPAddress == have.IPAddress &&
		(want.MACAddress == "" || want.MACAddress == have.MACAddress)
}

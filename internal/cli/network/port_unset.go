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
	extraProperty   []string
	tagWriteFlags
}

// empty reports whether no unset flag at all was given.
func (f *portUnsetFlags) empty() bool {
	return len(f.fixedIP) == 0 && len(f.securityGroup) == 0 && len(f.allowedAddress) == 0 &&
		len(f.bindingProfile) == 0 && len(f.extraProperty) == 0 &&
		!f.host && !f.device && !f.deviceOwner && !f.qosPolicy && !f.dataPlaneStatus && !f.given()
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

// portUnsetLists filters the list attributes, keeping every entry no removal
// spec matches.
func portUnsetLists(ctx context.Context, client *gophercloud.ServiceClient, f *portUnsetFlags,
	current *portExt, opts *ports.UpdateOpts,
) (bool, error) {
	changed := false
	if len(f.fixedIP) > 0 {
		remove, err := buildFixedIPs(ctx, client, f.fixedIP)
		if err != nil {
			return false, err
		}
		opts.FixedIPs = keepUnmatched(current.FixedIPs,
			func(have ports.IP) bool { return matchesAnyFixedIP(have, remove) })
		changed = true
	}

	if len(f.securityGroup) > 0 {
		removeIDs, err := resolveSecGroupIDs(ctx, client, f.securityGroup)
		if err != nil {
			return false, err
		}
		kept := keepUnmatched(current.SecurityGroups,
			func(have string) bool { return slices.Contains(removeIDs, have) })
		opts.SecurityGroups = &kept
		changed = true
	}

	if len(f.allowedAddress) > 0 {
		remove, err := parseAddressPairs(f.allowedAddress)
		if err != nil {
			return false, err
		}
		kept := keepUnmatched(current.AllowedAddressPairs,
			func(have ports.AddressPair) bool { return matchesAnyAddressPair(have, remove) })
		opts.AllowedAddressPairs = &kept
		changed = true
	}
	return changed, nil
}

// portUnsetAttrs builds the extension attributes unset clears. qos_policy_id,
// data_plane_status and --extra-property go out as null, as upstream sends
// them; binding:host_id keeps koc's empty string.
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
	extra, err := parseExtraProperties(f.extraProperty, true)
	if err != nil {
		return nil, err
	}
	return mergeAttrs(attrs, extra), nil
}

// matchesAnyFixedIP reports whether have should be removed. A removal spec that
// names only a subnet removes every fixed IP on that subnet; one that names only
// an address removes that address whatever its subnet; naming both requires both
// to match. This mirrors how OSC's --fixed-ip removal reads.
func matchesAnyFixedIP(have ports.IP, remove []ports.IP) bool {
	for _, want := range remove {
		subnetMatches := want.SubnetID == "" || want.SubnetID == have.SubnetID
		addressMatches := want.IPAddress == "" || want.IPAddress == have.IPAddress
		if subnetMatches && addressMatches {
			return true
		}
	}
	return false
}

// matchesAnyAddressPair applies the same partial-match rule to allowed address
// pairs: a spec with no mac-address removes the pair for that IP regardless of
// which MAC it carries.
func matchesAnyAddressPair(have ports.AddressPair, remove []ports.AddressPair) bool {
	for _, want := range remove {
		ipMatches := want.IPAddress == "" || want.IPAddress == have.IPAddress
		macMatches := want.MACAddress == "" || want.MACAddress == have.MACAddress
		if ipMatches && macMatches {
			return true
		}
	}
	return false
}

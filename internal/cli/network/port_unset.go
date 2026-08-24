package network

import (
	"context"
	"fmt"
	"io"
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
	fixedIP        []string
	securityGroup  []string
	allowedAddress []string
	host           bool
}

func newPortUnsetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &portUnsetFlags{}
	cmd := &cobra.Command{
		Use:   "unset <port>",
		Short: "Remove individual fixed IPs, security groups or allowed addresses from a port",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			if len(f.fixedIP) == 0 && len(f.securityGroup) == 0 && len(f.allowedAddress) == 0 && !f.host {
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
	fl.BoolVar(&f.host, "host", false, "clear the port's binding host ID")
	return cmd
}

func runPortUnset(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	nameOrID string, f *portUnsetFlags, w io.Writer,
) error {
	id, err := resolvePortID(ctx, client, nameOrID)
	if err != nil {
		return err
	}
	current, err := ports.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("reading port %s before unset: %w", nameOrID, err)
	}

	opts := ports.UpdateOpts{}
	// Pin the update to the revision we just read, so a concurrent change to the
	// port is rejected by neutron instead of being silently overwritten by the
	// list we computed from stale data.
	revision := current.RevisionNumber
	opts.RevisionNumber = &revision

	if len(f.fixedIP) > 0 {
		remove, perr := buildFixedIPs(ctx, client, f.fixedIP)
		if perr != nil {
			return perr
		}
		kept := make([]ports.IP, 0, len(current.FixedIPs))
		for _, have := range current.FixedIPs {
			if !matchesAnyFixedIP(have, remove) {
				kept = append(kept, have)
			}
		}
		opts.FixedIPs = kept
	}

	if len(f.securityGroup) > 0 {
		removeIDs, rerr := resolveSecGroupIDs(ctx, client, f.securityGroup)
		if rerr != nil {
			return rerr
		}
		kept := make([]string, 0, len(current.SecurityGroups))
		for _, have := range current.SecurityGroups {
			if !slices.Contains(removeIDs, have) {
				kept = append(kept, have)
			}
		}
		opts.SecurityGroups = &kept
	}

	if len(f.allowedAddress) > 0 {
		remove, perr := parseAddressPairs(f.allowedAddress)
		if perr != nil {
			return perr
		}
		kept := make([]ports.AddressPair, 0, len(current.AllowedAddressPairs))
		for _, have := range current.AllowedAddressPairs {
			if !matchesAnyAddressPair(have, remove) {
				kept = append(kept, have)
			}
		}
		opts.AllowedAddressPairs = &kept
	}

	var builder ports.UpdateOptsBuilder = opts
	if f.host {
		empty := ""
		builder = portUpdateOptsExt{UpdateOptsBuilder: opts, HostID: &empty}
	}

	var p portExt
	if err := ports.Update(ctx, client, id, builder).ExtractInto(&p); err != nil {
		return fmt.Errorf("updating port %s: %w", nameOrID, err)
	}
	fields, values := portShowFields(&p)
	return o.WriteSingle(w, fields, values)
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

// Package crossservice implements the koc commands that upstream registers
// under `openstack.common` — the nouns that span services rather than belonging
// to one. `availability zone list` merges nova, cinder and neutron;
// `limits show` merges nova and cinder.
//
// It is the second cross-service noun after internal/cli/quota, and follows the
// same shape: one client per service, derived lazily from a single
// authenticated session, and a service that is absent from the catalog is
// reported rather than fatal.
package crossservice

import (
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// NewCommands builds every common noun and returns them for the koc root,
// matching OSC where these live at the top level.
func NewCommands(a *auth.Options, o *output.Options) []*cobra.Command {
	availability := &cobra.Command{Use: "availability", Short: "Availability zone commands"}
	zone := &cobra.Command{Use: "zone", Short: "Manage availability zones"}
	zone.AddCommand(newAvailabilityZoneListCommand(a, o))
	availability.AddCommand(zone)

	limits := &cobra.Command{Use: "limits", Short: "Show resource limits"}
	limits.AddCommand(newLimitsShowCommand(a, o))

	usage := &cobra.Command{Use: "usage", Short: "Show compute resource usage"}
	usage.AddCommand(newUsageListCommand(a, o), newUsageShowCommand(a, o))

	return []*cobra.Command{availability, limits, usage}
}

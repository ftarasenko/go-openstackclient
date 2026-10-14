// Package baremetal implements the "koc baremetal" command tree, mirroring the
// upstream "openstack baremetal" (ironic) noun-verb surface.
package baremetal

import (
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// NewCommand builds the "baremetal" command group.
func NewCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "baremetal",
		Short:   "Bare metal (ironic) commands",
		Aliases: []string{"bm"},
	}
	cmd.AddCommand(newNodeCommand(a, o))
	cmd.AddCommand(newPortCommand(a, o))
	cmd.AddCommand(newDriverCommand(a, o))
	cmd.AddCommand(newConductorCommand(a, o))
	cmd.AddCommand(newIntrospectionCommand(a, o))
	cmd.AddCommand(newInspectionCommand(a, o))
	cmd.AddCommand(newAllocationCommand(a, o))
	cmd.AddCommand(newDeployCommand(a, o))
	cmd.AddCommand(newRunbookCommand(a, o))
	cmd.AddCommand(newVolumeCommand(a, o))
	cmd.AddCommand(newShardCommand(a, o))
	return cmd
}

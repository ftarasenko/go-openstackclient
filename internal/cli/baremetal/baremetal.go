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
	return cmd
}

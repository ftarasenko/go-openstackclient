// Package compute implements the "koc" compute (nova) command surface that is
// not tied to servers: flavors and keypairs. It mirrors the upstream
// "openstack flavor" and "openstack keypair" noun-verb commands.
//
// The separate server command tree is owned by another author; this package
// deliberately only provides the flavor and keypair groups and returns them as
// a slice so the root command can graft them on individually.
package compute

import (
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// NewCommand builds the compute command groups owned by this package: the
// top-level "flavor" and "keypair" commands. It returns a slice so the caller
// adds each group to the root command directly (there is no wrapping "compute"
// noun in the OSC surface).
func NewCommand(a *auth.Options, o *output.Options) []*cobra.Command {
	return []*cobra.Command{
		newFlavorCommand(a, o),
		newKeypairCommand(a, o),
	}
}

// Package image implements the "koc image" command tree, mirroring the upstream
// "openstack image" (glance v2) noun-verb surface.
package image

import (
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// NewCommand builds the "image" command group.
func NewCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "image",
		Short: "Image (glance) commands",
	}
	cmd.AddCommand(newImageListCommand(a, o))
	cmd.AddCommand(newImageShowCommand(a, o))
	cmd.AddCommand(newImageCreateCommand(a, o))
	cmd.AddCommand(newImageDeleteCommand(a, o))
	cmd.AddCommand(newImageSetCommand(a, o))
	cmd.AddCommand(newImageUnsetCommand(a, o))
	cmd.AddCommand(newImageSaveCommand(a, o))
	cmd.AddCommand(newImageAddCommand(a, o))
	cmd.AddCommand(newImageRemoveCommand(a, o))
	cmd.AddCommand(newImageMemberCommand(a, o))
	return cmd
}

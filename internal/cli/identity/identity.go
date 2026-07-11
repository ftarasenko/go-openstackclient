// Package identity implements the "koc" identity (keystone v3) command tree,
// mirroring the upstream "openstack" identity noun-verb surface (endpoint,
// domain, project, user, role, service, region, catalog, application
// credential, token and group).
package identity

import (
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// NewCommand builds every identity noun command and returns them as a slice so
// the caller can attach them directly to the koc root (matching OSC, where
// these nouns live at the top level rather than under an "identity" group).
func NewCommand(a *auth.Options, o *output.Options) []*cobra.Command {
	return []*cobra.Command{
		newEndpointCommand(a, o),
		newDomainCommand(a, o),
		newProjectCommand(a, o),
		newUserCommand(a, o),
		newRoleCommand(a, o),
		newServiceCommand(a, o),
		newRegionCommand(a, o),
		newCatalogCommand(a, o),
		newApplicationCommand(a, o),
		newTokenCommand(a, o),
		newGroupCommand(a, o),
	}
}

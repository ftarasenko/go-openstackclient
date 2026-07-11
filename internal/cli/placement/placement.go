// Package placement implements the "koc resource" and "koc trait" command
// trees, mirroring the upstream "openstack resource provider" / "openstack
// trait" (placement) noun-verb surface.
//
// Flag and command names follow upstream python-openstackclient
// (`openstack resource provider ...`). The KeyStack command reference at
// https://docs.keystack.ru/ was not reachable at implementation time (HTTP
// 403), so these are UNVERIFIED against KeyStack and fall back to upstream OSC
// semantics.
//
// Placement is a known gophercloud-coverage-risk area. Every operation in this
// package is covered by a typed gophercloud call (resourceproviders,
// allocations, traits), so no raw client.Get/Put/Delete fallback was needed; if
// a future operation lacks typed coverage, add a small helper that issues the
// raw request carrying the placement microversion header.
package placement

import (
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// NewCommand builds the placement command groups. It returns the top-level
// "resource" group (with its "provider" subgroup) and the top-level "trait"
// group, matching `openstack resource provider ...` and `openstack trait ...`.
func NewCommand(a *auth.Options, o *output.Options) []*cobra.Command {
	resource := &cobra.Command{
		Use:   "resource",
		Short: "Placement resource commands",
	}
	resource.AddCommand(newProviderCommand(a, o))

	trait := &cobra.Command{
		Use:   "trait",
		Short: "Placement trait commands",
	}
	trait.AddCommand(newTraitListCommand(a, o))

	return []*cobra.Command{resource, trait}
}

// newProviderCommand builds "resource provider ...".
func newProviderCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "provider",
		Short: "Manage placement resource providers",
	}
	cmd.AddCommand(newProviderListCommand(a, o))
	cmd.AddCommand(newProviderShowCommand(a, o))
	cmd.AddCommand(newProviderDeleteCommand(a, o))
	cmd.AddCommand(newProviderTraitCommand(a, o))
	cmd.AddCommand(newProviderAllocationCommand(a, o))
	return cmd
}

// newProviderTraitCommand builds "resource provider trait ...".
func newProviderTraitCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "trait",
		Short: "Manage traits associated with a resource provider",
	}
	cmd.AddCommand(newProviderTraitListCommand(a, o))
	return cmd
}

// newProviderAllocationCommand builds "resource provider allocation ...".
func newProviderAllocationCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "allocation",
		Short: "Manage resource provider allocations",
	}
	cmd.AddCommand(newProviderAllocationDeleteCommand(a, o))
	return cmd
}

// Package network implements the "koc network", "subnet", "router", "port",
// "floating ip", "security group" and "network agent" command trees, mirroring
// the upstream "openstack" (neutron v2) noun-verb surface.
//
// Flag names throughout this package follow upstream python-openstackclient
// (OSC). The KeyStack command reference at https://docs.keystack.ru/ returned
// HTTP 403 at implementation time, so the flags are UNVERIFIED against KeyStack
// and fall back to upstream OSC semantics.
package network

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// NewCommand builds the neutron command groups. It returns the top-level nouns
// as a slice so the root command can attach them alongside the other services.
// The two-word OSC commands ("floating ip", "security group") are modeled as a
// parent command plus a child, matching the OSC invocation syntax. "network
// agent" is nested under the "network" command.
func NewCommand(a *auth.Options, o *output.Options) []*cobra.Command {
	networkCmd := newNetworkCommand(a, o)
	networkCmd.AddCommand(newAgentCommand(a, o))

	floating := &cobra.Command{
		Use:   "floating",
		Short: "Manage floating IPs",
	}
	floating.AddCommand(newFloatingIPCommand(a, o))

	security := &cobra.Command{
		Use:   "security",
		Short: "Manage security groups",
	}
	security.AddCommand(newSecurityGroupCommand(a, o))

	return []*cobra.Command{
		networkCmd,
		newSubnetCommand(a, o),
		newRouterCommand(a, o),
		newPortCommand(a, o),
		floating,
		security,
	}
}

// newNetworkClient authenticates once and derives the neutron service client
// shared by every network subcommand. The network service uses no microversion
// header, so sc.Microversion is left empty (handled in auth.Client.Network).
func newNetworkClient(ctx context.Context, a *auth.Options) (*gophercloud.ServiceClient, error) {
	client, err := a.Authenticate(ctx)
	if err != nil {
		return nil, err
	}
	return client.Network()
}

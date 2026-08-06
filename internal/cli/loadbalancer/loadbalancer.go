// Package loadbalancer implements the "koc loadbalancer" command tree, mirroring
// the upstream "openstack loadbalancer" (octavia) noun-verb surface provided by
// the python-octaviaclient OSC plugin.
//
// Command and flag names follow that plugin (namespace
// openstack.load_balancer.v2). The KeyStack command reference at
// https://docs.keystack.ru/ was not reachable at implementation time (HTTP 403),
// so they are UNVERIFIED against KeyStack and fall back to upstream semantics.
//
// Octavia's two-word nouns ("loadbalancer stats show", "loadbalancer status
// show") are modeled as nested parent commands so cobra resolves them
// unambiguously, per the AGENTS.md command pattern.
package loadbalancer

import (
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// NewCommand builds the "loadbalancer" command group.
func NewCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "loadbalancer",
		Short:   "Load balancer (octavia) commands",
		Aliases: []string{"lb"},
	}
	cmd.AddCommand(
		newLBListCommand(a, o),
		newLBShowCommand(a, o),
		newLBCreateCommand(a, o),
		newLBSetCommand(a, o),
		newLBDeleteCommand(a, o),
		newLBFailoverCommand(a, o),
		newLBStatsCommand(a, o),
		newLBStatusCommand(a, o),
	)
	return cmd
}

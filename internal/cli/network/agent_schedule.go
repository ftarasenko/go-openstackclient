package network

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
	networkapi "github.com/ftarasenko/go-openstackclient/pkg/openstack/network"
)

// This file holds the agent scheduling verbs: "network agent add/remove
// network|router" and "network agent router set". They mirror upstream's
// network_agent.py, including its one surprising rule: the --dhcp / --l3 flag
// is what selects the action, and without it upstream looks the agent and the
// target up and then does nothing. koc does the same (so a typo'd agent ID
// still fails), rather than inventing an error upstream does not raise.
//
// One deliberate deviation: upstream builds a CommandError when the DHCP
// add/remove call fails but never raises it, so a failure exits 0. koc returns
// the error.

const flagHAChassisPriority = "ha-chassis-priority"

const haChassisPriorityHelp = "HA chassis priority, 0-32767 (ML2/OVN L3 agents only)"

func newAgentAddCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Add a network or router to an agent",
	}
	cmd.AddCommand(newAgentAddNetworkCommand(a, o), newAgentAddRouterCommand(a, o))
	return cmd
}

func newAgentRemoveCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remove",
		Short: "Remove a network or router from an agent",
	}
	cmd.AddCommand(newAgentRemoveNetworkCommand(a, o), newAgentRemoveRouterCommand(a, o))
	return cmd
}

func newAgentRouterCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "router",
		Short: "Manage routers hosted by an L3 agent",
	}
	cmd.AddCommand(newAgentRouterSetCommand(a, o))
	return cmd
}

// agentTargetCommand is the shared shape of three of the add/remove verbs:
// "<agent-id> <target>", one boolean flag that selects the action, and a seam
// (add router has a second flag and builds its own).
func agentTargetCommand(a *auth.Options, o *output.Options, use, short, flag, flagHelp string,
	run func(ctx context.Context, client *gophercloud.ServiceClient, agentID, target string, act bool, w io.Writer) error,
) *cobra.Command {
	act := new(bool)
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			return run(ctx, client, args[0], args[1], *act, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(act, flag, false, flagHelp)
	return cmd
}

func newAgentAddNetworkCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return agentTargetCommand(a, o, "network --dhcp <agent-id> <network>",
		"Add a network to a DHCP agent", "dhcp", "add the network to a DHCP agent", runAgentAddNetwork)
}

func newAgentRemoveNetworkCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return agentTargetCommand(a, o, "network --dhcp <agent-id> <network>",
		"Remove a network from a DHCP agent", "dhcp", "remove the network from a DHCP agent", runAgentRemoveNetwork)
}

func newAgentRemoveRouterCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return agentTargetCommand(a, o, "router --l3 <agent-id> <router>",
		"Remove a router from an L3 agent", "l3", "remove the router from an L3 agent", runAgentRemoveRouter)
}

// agentTarget checks the agent exists (upstream's get_agent) and resolves the
// network or router the verb acts on.
func agentTarget(ctx context.Context, client *gophercloud.ServiceClient, agentID, ref string,
	resolveID func(context.Context, *gophercloud.ServiceClient, string) (string, error),
) (string, error) {
	if _, err := networkapi.GetAgent(ctx, client, agentID); err != nil {
		return "", err
	}
	return resolveID(ctx, client, ref)
}

func runAgentAddNetwork(ctx context.Context, client *gophercloud.ServiceClient, agentID, ref string, dhcp bool, w io.Writer) error {
	netID, err := agentTarget(ctx, client, agentID, ref, resolveNetworkID)
	if err != nil || !dhcp {
		return err
	}
	if err := networkapi.AddDHCPAgentNetwork(ctx, client, agentID, netID); err != nil {
		return relabel(err, "adding network %q to DHCP agent %s", ref, agentID)
	}
	_, err = fmt.Fprintf(w, "Added network %s to DHCP agent %s\n", ref, agentID)
	return err
}

func runAgentRemoveNetwork(ctx context.Context, client *gophercloud.ServiceClient, agentID, ref string, dhcp bool, w io.Writer) error {
	netID, err := agentTarget(ctx, client, agentID, ref, resolveNetworkID)
	if err != nil || !dhcp {
		return err
	}
	if err := networkapi.RemoveDHCPAgentNetwork(ctx, client, agentID, netID); err != nil {
		return relabel(err, "removing network %q from DHCP agent %s", ref, agentID)
	}
	_, err = fmt.Fprintf(w, "Removed network %s from DHCP agent %s\n", ref, agentID)
	return err
}

func runAgentRemoveRouter(ctx context.Context, client *gophercloud.ServiceClient, agentID, ref string, l3 bool, w io.Writer) error {
	routerID, err := agentTarget(ctx, client, agentID, ref, resolveRouterID)
	if err != nil || !l3 {
		return err
	}
	if err := networkapi.RemoveL3AgentRouter(ctx, client, agentID, routerID); err != nil {
		return relabel(err, "removing router %q from L3 agent %s", ref, agentID)
	}
	_, err = fmt.Fprintf(w, "Removed router %s from L3 agent %s\n", ref, agentID)
	return err
}

type agentAddRouterFlags struct {
	l3       bool
	priority int
	// prioritySet records whether --ha-chassis-priority was given: 0 is a
	// valid priority, so the zero value cannot mean "absent".
	prioritySet bool
}

func newAgentAddRouterCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &agentAddRouterFlags{}
	cmd := &cobra.Command{
		Use:   "router --l3 [--ha-chassis-priority <n>] <agent-id> <router>",
		Short: "Add a router to an L3 agent (--ha-chassis-priority needs ML2/OVN)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			f.prioritySet = cmd.Flags().Changed(flagHAChassisPriority)
			ctx := cmd.Context()
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			return runAgentAddRouter(ctx, client, args[0], args[1], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&f.l3, "l3", false, "add the router to an L3 agent")
	fl.IntVar(&f.priority, flagHAChassisPriority, 0, haChassisPriorityHelp)
	return cmd
}

func runAgentAddRouter(ctx context.Context, client *gophercloud.ServiceClient, agentID, ref string,
	f *agentAddRouterFlags, w io.Writer,
) error {
	routerID, err := agentTarget(ctx, client, agentID, ref, resolveRouterID)
	if err != nil || !f.l3 {
		return err
	}
	// HAChassisPriority stays nil (omitted) unless --ha-chassis-priority
	// was given, as upstream.
	opts := networkapi.L3AgentRouterOpts{RouterID: routerID}
	if f.prioritySet {
		p := f.priority
		opts.HAChassisPriority = &p
	}
	if err := networkapi.AddL3AgentRouter(ctx, client, agentID, opts); err != nil {
		return relabel(err, "adding router %q to L3 agent %s", ref, agentID)
	}
	_, err = fmt.Fprintf(w, "Added router %s to L3 agent %s\n", ref, agentID)
	return err
}

func newAgentRouterSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var priority int
	cmd := &cobra.Command{
		Use:   "set --ha-chassis-priority <n> <agent-id> <router>",
		Short: "Set a router's HA chassis priority on an L3 agent (ML2/OVN only)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			if !cmd.Flags().Changed(flagHAChassisPriority) {
				return fmt.Errorf("--%s is required", flagHAChassisPriority)
			}
			ctx := cmd.Context()
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			return runAgentRouterSet(ctx, client, args[0], args[1], priority, cmd.OutOrStdout())
		},
	}
	cmd.Flags().IntVar(&priority, flagHAChassisPriority, 0, haChassisPriorityHelp)
	return cmd
}

func runAgentRouterSet(ctx context.Context, client *gophercloud.ServiceClient, agentID, ref string, priority int, w io.Writer) error {
	routerID, err := agentTarget(ctx, client, agentID, ref, resolveRouterID)
	if err != nil {
		return err
	}
	if err := networkapi.SetL3AgentRouterPriority(ctx, client, agentID, routerID, priority); err != nil {
		return relabel(err, "setting router %q on L3 agent %s", ref, agentID)
	}
	_, err = fmt.Fprintf(w, "Set HA chassis priority %d for router %s on L3 agent %s\n", priority, ref, agentID)
	return err
}

package network

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/agents"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// "bgp dragent ..." mirrors upstream network/v2/dynamic_routing/bgp_dragent.py:
// scheduling a BGP speaker onto a dynamic routing agent, and listing those
// agents.

// bgpDRAgentType is the agent_type neutron-bgp-dragent registers with.
const bgpDRAgentType = "BGP dynamic routing agent"

func newBGPDRAgentCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dragent",
		Short: "Manage BGP dynamic routing agents (requires the bgp_dragent_scheduler extension)",
	}
	add := &cobra.Command{Use: "add", Short: "Add a BGP speaker to a dynamic routing agent"}
	add.AddCommand(bgpDRAgentSpeakerCommand(a, o, "Add a BGP speaker to a dynamic routing agent", runBGPDRAgentAddSpeaker))
	remove := &cobra.Command{Use: "remove", Short: "Remove a BGP speaker from a dynamic routing agent"}
	remove.AddCommand(bgpDRAgentSpeakerCommand(a, o, "Remove a BGP speaker from a dynamic routing agent", runBGPDRAgentRemoveSpeaker))
	cmd.AddCommand(add, remove, newBGPDRAgentListCommand(a, o))
	return cmd
}

func bgpDRAgentSpeakerCommand(a *auth.Options, o *output.Options, short string,
	run func(context.Context, *gophercloud.ServiceClient, string, string, io.Writer) error,
) *cobra.Command {
	return &cobra.Command{
		Use:   "speaker <agent-id> <bgp-speaker>",
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
			return run(ctx, client, args[0], args[1], cmd.OutOrStdout())
		},
	}
}

// runBGPDRAgentAddSpeaker posts to /agents/{id}/bgp-drinstances. The agent is
// an ID only, as upstream's.
func runBGPDRAgentAddSpeaker(ctx context.Context, client *gophercloud.ServiceClient, agentID, speakerRef string, w io.Writer) error {
	speakerID, err := resolveBGPSpeakerID(ctx, client, speakerRef)
	if err != nil {
		return err
	}
	opts := agents.ScheduleBGPSpeakerOpts{SpeakerID: speakerID}
	if err := agents.ScheduleBGPSpeaker(ctx, client, agentID, opts).ExtractErr(); err != nil {
		return explainMissingService(ctx, client,
			fmt.Errorf("adding BGP speaker %s to dynamic routing agent %s: %w", speakerRef, agentID, err), extBGPDRAgentSchedule)
	}
	_, err = fmt.Fprintf(w, "Added BGP speaker %s to dynamic routing agent %s\n", speakerRef, agentID)
	return err
}

func runBGPDRAgentRemoveSpeaker(ctx context.Context, client *gophercloud.ServiceClient, agentID, speakerRef string, w io.Writer) error {
	speakerID, err := resolveBGPSpeakerID(ctx, client, speakerRef)
	if err != nil {
		return err
	}
	if err := agents.RemoveBGPSpeaker(ctx, client, agentID, speakerID).ExtractErr(); err != nil {
		return explainMissingService(ctx, client,
			fmt.Errorf("removing BGP speaker %s from dynamic routing agent %s: %w", speakerRef, agentID, err), extBGPDRAgentSchedule)
	}
	_, err = fmt.Fprintf(w, "Removed BGP speaker %s from dynamic routing agent %s\n", speakerRef, agentID)
	return err
}

func newBGPDRAgentListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var speaker string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List dynamic routing agents",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			return runBGPDRAgentList(ctx, client, o, speaker, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&speaker, "bgp-speaker", "", "list the dynamic routing agents hosting this BGP speaker (name or ID)")
	return cmd
}

// runBGPDRAgentList lists every agent of the dynamic-routing type, or with a
// speaker the agents hosting it (GET /bgp-speakers/{id}/bgp-dragents), in the
// columns of "network agent list" — upstream's ListDRAgent uses the same ones.
func runBGPDRAgentList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, speakerRef string, w io.Writer) error {
	if speakerRef == "" {
		pages, err := agents.List(client, agents.ListOpts{AgentType: bgpDRAgentType}).AllPages(ctx)
		if err != nil {
			return fmt.Errorf("listing dynamic routing agents: %w", err)
		}
		all, err := agents.ExtractAgents(pages)
		if err != nil {
			return fmt.Errorf("parsing dynamic routing agent list: %w", err)
		}
		return writeAgentRows(o, w, all)
	}
	speakerID, err := resolveBGPSpeakerID(ctx, client, speakerRef)
	if err != nil {
		return err
	}
	pages, err := agents.ListDRAgentHostingBGPSpeakers(client, speakerID).AllPages(ctx)
	if err != nil {
		return explainMissingService(ctx, client,
			fmt.Errorf("listing dynamic routing agents hosting BGP speaker %s: %w", speakerRef, err), extBGPDRAgentSchedule)
	}
	all, err := agents.ExtractAgents(pages)
	if err != nil {
		return fmt.Errorf("parsing dynamic routing agent list: %w", err)
	}
	return writeAgentRows(o, w, all)
}

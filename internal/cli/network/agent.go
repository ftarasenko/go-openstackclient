package network

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/agents"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// newAgentCommand builds "network agent ...".
func newAgentCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Manage network agents",
	}
	cmd.AddCommand(newAgentListCommand(a, o))
	cmd.AddCommand(newAgentShowCommand(a, o))
	cmd.AddCommand(newAgentDeleteCommand(a, o))
	cmd.AddCommand(newAgentSetCommand(a, o))
	return cmd
}

func agentShowFields(ag *agents.Agent) ([]string, []any) {
	fields := []string{
		"id", "agent_type", "binary", "host", "availability_zone",
		"admin_state_up", "alive", "topic", "description",
		"heartbeat_timestamp", "started_at", "created_at",
	}
	values := []any{
		ag.ID, ag.AgentType, ag.Binary, ag.Host, ag.AvailabilityZone,
		ag.AdminStateUp, ag.Alive, ag.Topic, ag.Description,
		ag.HeartbeatTimestamp, ag.StartedAt, ag.CreatedAt,
	}
	return fields, values
}

type agentListFlags struct {
	agentType string
	host      string
}

func newAgentListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &agentListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List network agents",
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
			return runAgentList(ctx, client, o, f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.agentType, "agent-type", "", "filter by agent type (e.g. l3, dhcp, open-vswitch)")
	fl.StringVar(&f.host, "host", "", "filter by agent host")
	return cmd
}

func runAgentList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *agentListFlags, w io.Writer) error {
	opts := agents.ListOpts{AgentType: f.agentType, Host: f.host}
	pages, err := agents.List(client, opts).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing network agents: %w", err)
	}
	all, err := agents.ExtractAgents(pages)
	if err != nil {
		return fmt.Errorf("parsing network agent list: %w", err)
	}
	t := output.Table{Columns: []string{"ID", "Agent Type", "Host", "Availability Zone", "Alive", "State", "Binary"}, Rows: make([][]any, 0, len(all))}
	for _, ag := range all {
		t.Rows = append(t.Rows, []any{ag.ID, ag.AgentType, ag.Host, ag.AvailabilityZone, aliveString(ag.Alive), adminState(ag.AdminStateUp), ag.Binary})
	}
	return o.WriteList(w, t)
}

func aliveString(alive bool) string {
	if alive {
		return ":-)"
	}
	return "XXX"
}

func newAgentShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <agent>",
		Short: "Show details of a network agent",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			return runAgentShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
	return cmd
}

func runAgentShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, id string, w io.Writer) error {
	ag, err := agents.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("getting network agent %s: %w", id, err)
	}
	fields, values := agentShowFields(ag)
	return o.WriteSingle(w, fields, values)
}

func newAgentDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <agent> [<agent> ...]",
		Short: "Delete network agent(s)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			return runAgentDelete(ctx, client, args, cmd.OutOrStdout())
		},
	}
	return cmd
}

func runAgentDelete(ctx context.Context, client *gophercloud.ServiceClient, ids []string, w io.Writer) error {
	var errs []error
	for _, id := range ids {
		if err := agents.Delete(ctx, client, id).ExtractErr(); err != nil {
			errs = append(errs, fmt.Errorf("deleting network agent %s: %w", id, err))
			continue
		}
		if _, err := fmt.Fprintf(w, "Deleted agent %s\n", id); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

type agentSetFlags struct {
	enable  bool
	disable bool
}

func newAgentSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &agentSetFlags{}
	cmd := &cobra.Command{
		Use:   "set <agent>",
		Short: "Set network agent properties",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			return runAgentSet(ctx, client, o, args[0], f, cmd.Flags(), cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&f.enable, "enable", false, "enable the agent (admin state up)")
	fl.BoolVar(&f.disable, "disable", false, "disable the agent (admin state down)")
	return cmd
}

func runAgentSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, id string, f *agentSetFlags, flags flagSet, w io.Writer) error {
	if err := mutuallyExclusive(flags, "enable", "disable"); err != nil {
		return err
	}
	state := enableDisable(flags, f.enable, f.disable)
	if state == nil {
		return fmt.Errorf("agent set requires --enable or --disable")
	}
	ag, err := agents.Update(ctx, client, id, agents.UpdateOpts{AdminStateUp: state}).Extract()
	if err != nil {
		return fmt.Errorf("updating network agent %s: %w", id, err)
	}
	fields, values := agentShowFields(ag)
	return o.WriteSingle(w, fields, values)
}

package network

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/agents"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
	"github.com/ftarasenko/go-openstackclient/internal/output"
	networkapi "github.com/ftarasenko/go-openstackclient/pkg/openstack/network"
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
	cmd.AddCommand(newAgentAddCommand(a, o))
	cmd.AddCommand(newAgentRemoveCommand(a, o))
	cmd.AddCommand(newAgentRouterCommand(a, o))
	return cmd
}

func agentShowFields(ag *agents.Agent) ([]string, []any) {
	fields := []string{
		"id", "agent_type", "binary", "host", "availability_zone",
		"admin_state_up", "alive", "topic", "description", "configuration",
		"last_heartbeat_at", "started_at", "created_at",
	}
	values := []any{
		ag.ID, ag.AgentType, ag.Binary, ag.Host, ag.AvailabilityZone,
		ag.AdminStateUp, ag.Alive, ag.Topic, ag.Description, ag.Configurations,
		ag.HeartbeatTimestamp, ag.StartedAt, ag.CreatedAt,
	}
	return fields, values
}

var agentColumns = fieldNames(agentShowFields(&agents.Agent{}))

type agentListFlags struct {
	agentType string
	host      string
	network   string
	router    string
	long      bool
}

// agentTypeNames maps upstream's --agent-type choices to the agent_type value
// neutron stores and filters on: the API matches the full string ("L3 agent"),
// so sending the short name matched nothing. A value outside the table is sent
// verbatim, which lets the full name be given directly too.
var agentTypeNames = map[string]string{
	"bgp":                    "BGP dynamic routing agent",
	"dhcp":                   "DHCP agent",
	"open-vswitch":           "Open vSwitch agent",
	"linux-bridge":           "Linux bridge agent",
	"ofa":                    "OFA driver agent",
	"l3":                     "L3 agent",
	"loadbalancer":           "Loadbalancer agent",
	"metering":               "Metering agent",
	"metadata":               "Metadata agent",
	"macvtap":                "Macvtap agent",
	"nic":                    "NIC Switch agent",
	"baremetal":              "Baremetal Node",
	"ovn-controller":         "OVN Controller agent",
	"ovn-controller-gateway": "OVN Controller Gateway agent",
	"ovn-metadata":           "OVN Metadata agent",
	"ovn-agent":              "OVN Neutron agent",
}

func agentTypeFilter(t string) string {
	if full, ok := agentTypeNames[t]; ok {
		return full
	}
	return t
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
			if err := mutuallyExclusive(cmd.Flags(), "network", "router"); err != nil {
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
	fl.StringVar(&f.agentType, "agent-type", "",
		"filter by agent type (bgp, dhcp, open-vswitch, linux-bridge, ofa, l3, loadbalancer, metering, "+
			"metadata, macvtap, nic, baremetal, ovn-controller, ovn-controller-gateway, ovn-metadata, ovn-agent)")
	fl.StringVar(&f.host, "host", "", "filter by agent host")
	// As upstream, --network and --router list a different collection, which
	// takes no filters: --agent-type and --host do not apply to them.
	fl.StringVar(&f.network, "network", "", "list the DHCP agents hosting this network (name or ID)")
	fl.StringVar(&f.router, "router", "", "list the L3 agents hosting this router (name or ID)")
	fl.BoolVar(&f.long, "long", false, "list additional fields in output (HA State, with --router)")
	return cmd
}

var agentListColumns = []string{"ID", "Agent Type", "Host", "Availability Zone", "Alive", "State", "Binary"}

func agentListRow(ag *agents.Agent) []any {
	return []any{ag.ID, ag.AgentType, ag.Host, ag.AvailabilityZone, aliveString(ag.Alive), adminState(ag.AdminStateUp), ag.Binary}
}

func runAgentList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *agentListFlags, w io.Writer) error {
	switch {
	case f.network != "":
		return runAgentListByNetwork(ctx, client, o, f.network, w)
	case f.router != "":
		return runAgentListByRouter(ctx, client, o, f.router, f.long, w)
	}
	opts := agents.ListOpts{AgentType: agentTypeFilter(f.agentType), Host: f.host}
	all, err := networkapi.ListAgents(ctx, client, opts)
	if err != nil {
		return err
	}
	return writeAgentRows(o, w, all)
}

func writeAgentRows(o *output.Options, w io.Writer, all []agents.Agent) error {
	t := output.Table{Columns: agentListColumns, Rows: make([][]any, 0, len(all))}
	for i := range all {
		t.Rows = append(t.Rows, agentListRow(&all[i]))
	}
	return o.WriteList(w, t)
}

func runAgentListByNetwork(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, w io.Writer) error {
	netID, err := resolveNetworkID(ctx, client, ref)
	if err != nil {
		return err
	}
	all, err := networkapi.ListNetworkDHCPAgents(ctx, client, netID)
	if err != nil {
		return relabel(err, "listing DHCP agents hosting network %q", ref)
	}
	return writeAgentRows(o, w, all)
}

func runAgentListByRouter(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	ref string, long bool, w io.Writer,
) error {
	routerID, err := resolveRouterID(ctx, client, ref)
	if err != nil {
		return err
	}
	all, err := networkapi.ListRouterL3Agents(ctx, client, routerID)
	if err != nil {
		return relabelList(err, "listing L3 agents hosting router %q", ref)
	}
	cols := agentListColumns
	if long {
		cols = append(append([]string{}, cols...), "HA State")
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(all))}
	for _, ag := range all {
		row := []any{ag.ID, ag.AgentType, ag.Host, ag.AvailabilityZone, aliveString(ag.Alive), adminState(ag.AdminStateUp), ag.Binary}
		if long {
			row = append(row, ag.HAState)
		}
		t.Rows = append(t.Rows, row)
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
	ag, err := networkapi.GetAgent(ctx, client, id)
	if err != nil {
		return err
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
	return batchdelete.Each(ids, func(id string) error {
		if err := networkapi.DeleteAgent(ctx, client, id); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "Deleted agent %s\n", id); err != nil {
			return err
		}
		return nil
	})
}

type agentSetFlags struct {
	description string
	enable      bool
	disable     bool
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
	fl.StringVar(&f.description, flagDescription, "", "set the agent description")
	fl.BoolVar(&f.enable, "enable", false, "enable the agent (admin state up)")
	fl.BoolVar(&f.disable, "disable", false, "disable the agent (admin state down)")
	return cmd
}

func runAgentSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, id string, f *agentSetFlags, flags flagSet, w io.Writer) error {
	if err := o.CheckColumns(agentColumns...); err != nil {
		return err
	}
	if err := mutuallyExclusive(flags, "enable", "disable"); err != nil {
		return err
	}
	opts := agents.UpdateOpts{AdminStateUp: enableDisable(flags, f.enable, f.disable)}
	if flags.Changed(flagDescription) {
		desc := f.description
		opts.Description = &desc
	}
	if opts.AdminStateUp == nil && opts.Description == nil {
		return fmt.Errorf("agent set requires --description, --enable or --disable")
	}
	ag, err := networkapi.UpdateAgent(ctx, client, id, opts)
	if err != nil {
		return err
	}
	fields, values := agentShowFields(ag)
	return o.WriteSingle(w, fields, values)
}

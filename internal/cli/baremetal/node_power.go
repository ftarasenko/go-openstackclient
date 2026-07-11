package baremetal

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/baremetal/v1/nodes"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// newNodePowerCommand builds "baremetal node power on|off|reboot".
func newNodePowerCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "power",
		Short: "Change a node's power state",
	}
	cmd.AddCommand(
		newNodePowerActionCommand(a, o, "on", "Power a node on", nodes.PowerOn),
		newNodePowerActionCommand(a, o, "off", "Power a node off", nodes.PowerOff),
		newNodePowerActionCommand(a, o, "reboot", "Reboot a node", nodes.Rebooting),
	)
	return cmd
}

func newNodePowerActionCommand(a *auth.Options, o *output.Options, verb, short string, target nodes.TargetPowerState) *cobra.Command {
	var soft bool
	cmd := &cobra.Command{
		Use:   verb + " <node>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runNodePower(ctx, client, args[0], target, soft, cmd.OutOrStdout())
		},
	}
	// --soft requests a graceful power action. Flag semantics follow upstream
	// OSC; UNVERIFIED against KeyStack docs (docs.keystack.ru returned HTTP 403).
	cmd.Flags().BoolVar(&soft, "soft", false, "request a soft (graceful) power action")
	return cmd
}

func runNodePower(ctx context.Context, client *gophercloud.ServiceClient, id string, target nodes.TargetPowerState, soft bool, w io.Writer) error {
	if soft {
		switch target {
		case nodes.PowerOff:
			target = nodes.SoftPowerOff
		case nodes.Rebooting:
			target = nodes.SoftRebooting
		}
	}
	opts := nodes.PowerStateOpts{Target: target}
	if err := nodes.ChangePowerState(ctx, client, id, opts).ExtractErr(); err != nil {
		return fmt.Errorf("changing power state of node %s: %w", id, err)
	}
	if _, err := fmt.Fprintf(w, "Requested power state %q for node %s\n", target, id); err != nil {
		return err
	}
	return nil
}

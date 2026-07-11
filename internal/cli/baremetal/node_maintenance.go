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

// newNodeMaintenanceCommand builds "baremetal node maintenance set|unset".
func newNodeMaintenanceCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "maintenance",
		Short: "Set or unset node maintenance mode",
	}
	cmd.AddCommand(newNodeMaintenanceSetCommand(a, o))
	cmd.AddCommand(newNodeMaintenanceUnsetCommand(a, o))
	return cmd
}

func newNodeMaintenanceSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:   "set <node>",
		Short: "Put a node into maintenance mode",
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
			return runNodeMaintenanceSet(ctx, client, args[0], reason, cmd.OutOrStdout())
		},
	}
	// Flag semantics follow upstream OSC; UNVERIFIED against KeyStack docs
	// (docs.keystack.ru returned HTTP 403).
	cmd.Flags().StringVar(&reason, "reason", "", "reason for setting maintenance mode")
	return cmd
}

func runNodeMaintenanceSet(ctx context.Context, client *gophercloud.ServiceClient, id, reason string, w io.Writer) error {
	opts := nodes.MaintenanceOpts{Reason: reason}
	if err := nodes.SetMaintenance(ctx, client, id, opts).ExtractErr(); err != nil {
		return fmt.Errorf("setting maintenance on node %s: %w", id, err)
	}
	if _, err := fmt.Fprintf(w, "Set node %s into maintenance mode\n", id); err != nil {
		return err
	}
	return nil
}

func newNodeMaintenanceUnsetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "unset <node>",
		Short: "Take a node out of maintenance mode",
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
			return runNodeMaintenanceUnset(ctx, client, args[0], cmd.OutOrStdout())
		},
	}
	return cmd
}

func runNodeMaintenanceUnset(ctx context.Context, client *gophercloud.ServiceClient, id string, w io.Writer) error {
	if err := nodes.UnsetMaintenance(ctx, client, id).ExtractErr(); err != nil {
		return fmt.Errorf("unsetting maintenance on node %s: %w", id, err)
	}
	if _, err := fmt.Fprintf(w, "Took node %s out of maintenance mode\n", id); err != nil {
		return err
	}
	return nil
}

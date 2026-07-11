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

// newNodeBootDeviceCommand builds "baremetal node boot device set|show".
func newNodeBootDeviceCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "boot",
		Short: "Manage a node's boot device",
	}
	device := &cobra.Command{
		Use:   "device",
		Short: "Manage a node's boot device",
	}
	device.AddCommand(newNodeBootDeviceSetCommand(a, o))
	device.AddCommand(newNodeBootDeviceShowCommand(a, o))
	cmd.AddCommand(device)
	return cmd
}

func newNodeBootDeviceSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var persistent bool
	cmd := &cobra.Command{
		Use:   "set <node> <device>",
		Short: "Set the boot device for a node",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runNodeBootDeviceSet(ctx, client, args[0], args[1], persistent, cmd.OutOrStdout())
		},
	}
	// --persistent makes the boot device stick across reboots. Flag semantics
	// follow upstream OSC; UNVERIFIED against KeyStack docs (docs.keystack.ru
	// returned HTTP 403).
	cmd.Flags().BoolVar(&persistent, "persistent", false, "make the boot device persistent across reboots")
	return cmd
}

func runNodeBootDeviceSet(ctx context.Context, client *gophercloud.ServiceClient, id, device string, persistent bool, w io.Writer) error {
	opts := nodes.BootDeviceOpts{BootDevice: device, Persistent: persistent}
	if err := nodes.SetBootDevice(ctx, client, id, opts).ExtractErr(); err != nil {
		return fmt.Errorf("setting boot device for node %s: %w", id, err)
	}
	if _, err := fmt.Fprintf(w, "Set boot device of node %s to %s\n", id, device); err != nil {
		return err
	}
	return nil
}

func newNodeBootDeviceShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <node>",
		Short: "Show the boot device for a node",
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
			return runNodeBootDeviceShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
	return cmd
}

func runNodeBootDeviceShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, id string, w io.Writer) error {
	bd, err := nodes.GetBootDevice(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("getting boot device for node %s: %w", id, err)
	}
	fields := []string{"boot_device", "persistent"}
	values := []any{bd.BootDevice, bd.Persistent}
	return o.WriteSingle(w, fields, values)
}

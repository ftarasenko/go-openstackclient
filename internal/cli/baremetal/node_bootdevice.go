package baremetal

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/baremetal/v1/nodes"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// newNodeBootDeviceCommand builds "baremetal node boot device set|show" and
// "baremetal node boot mode set".
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
	mode := &cobra.Command{
		Use:   "mode",
		Short: "Manage a node's boot mode",
	}
	mode.AddCommand(newNodeBootModeSetCommand(a))
	cmd.AddCommand(mode)
	return cmd
}

// bootModes are python-ironicclient's choices for "node boot mode set".
var bootModes = []string{"uefi", "bios"}

// newNodeBootModeSetCommand builds "baremetal node boot mode set": a raw PUT of
// {"target": <mode>} to /nodes/<node>/states/boot_mode (API 1.76, inside the
// Zed cap), which gophercloud has no call for. Like secure boot, ironic
// answers 202 and the conductor applies the change on the BMC.
func newNodeBootModeSetCommand(a *auth.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "set <node> [<node> ...] <boot_mode>",
		Short: "Set the boot mode (uefi or bios) for the next deployment of node(s)",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			mode := args[len(args)-1]
			if !slices.Contains(bootModes, mode) {
				return fmt.Errorf("invalid boot mode %q: must be one of %s", mode, strings.Join(bootModes, ", "))
			}
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runNodeBootModeSet(ctx, client, args[:len(args)-1], mode, cmd.OutOrStdout())
		},
	}
}

func runNodeBootModeSet(ctx context.Context, client *gophercloud.ServiceClient, nodes []string, mode string, w io.Writer) error {
	return batchdelete.Each(nodes, func(node string) error {
		if err := ironicPut(ctx, client, joinPath("nodes", node, "states", "boot_mode"), map[string]any{"target": mode}); err != nil {
			return fmt.Errorf("setting boot mode %s for baremetal node %s: %w", mode, node, err)
		}
		_, err := fmt.Fprintf(w, "Requested boot mode %s for node %s\n", mode, node)
		return err
	})
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

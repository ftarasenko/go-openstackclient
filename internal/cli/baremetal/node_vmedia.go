package baremetal

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/koc/internal/auth"
	bmapi "github.com/ftarasenko/koc/pkg/openstack/baremetal"
)

// vmediaDevices are ironic's boot_devices.VMEDIA_DEVICES.
var vmediaDevices = []string{"cdrom", "disk", "floppy"}

// newNodeVmediaCommand builds "baremetal node vmedia attach|detach".
func newNodeVmediaCommand(a *auth.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "vmedia",
		Short: "Attach or detach virtual media (requires ironic API 1.89, OpenStack 2024.1)",
	}
	var deviceType, source string
	attach := &cobra.Command{
		Use:   "attach <node> <image-url>",
		Short: "Attach a virtual media image to a node (requires ironic API 1.89, OpenStack 2024.1)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runNodeVmediaAttach(ctx, client, args[0], args[1], deviceType, source, cmd.OutOrStdout())
		},
	}
	attach.Flags().StringVar(&deviceType, "device-type", "cdrom", "device to attach as: "+strings.Join(vmediaDevices, ", "))
	attach.Flags().StringVar(&source, "image-download-source", "",
		"how the conductor serves the image: http (the BMC fetches the URL), local or swift; ironic defaults to local")

	var detachTypes []string
	detach := &cobra.Command{
		Use:   "detach <node>",
		Short: "Detach virtual media from a node (requires ironic API 1.89, OpenStack 2024.1)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runNodeVmediaDetach(ctx, client, args[0], detachTypes, cmd.OutOrStdout())
		},
	}
	detach.Flags().StringSliceVar(&detachTypes, "device-type", nil,
		"detach only these devices ("+strings.Join(vmediaDevices, ", ")+"); default all")
	cmd.AddCommand(attach, detach)
	return cmd
}

func runNodeVmediaAttach(ctx context.Context, client *gophercloud.ServiceClient, node, image, deviceType, source string, w io.Writer) error {
	if !slices.Contains(vmediaDevices, deviceType) {
		return fmt.Errorf("--device-type %s: must be one of %s", deviceType, strings.Join(vmediaDevices, ", "))
	}
	opts := bmapi.VirtualMediaOpts{DeviceType: deviceType, ImageURL: image, ImageDownloadSource: source}
	if err := bmapi.AttachVirtualMedia(ctx, client, node, opts); err != nil {
		return explained(err)
	}
	_, err := fmt.Fprintf(w, "Attached %s as %s to node %s\n", image, deviceType, node)
	return err
}

func runNodeVmediaDetach(ctx context.Context, client *gophercloud.ServiceClient, node string, types []string, w io.Writer) error {
	for _, t := range types {
		if !slices.Contains(vmediaDevices, t) {
			return fmt.Errorf("--device-type %s: must be one of %s", t, strings.Join(vmediaDevices, ", "))
		}
	}
	if err := bmapi.DetachVirtualMedia(ctx, client, node, types); err != nil {
		return explained(err)
	}
	_, err := fmt.Fprintf(w, "Detached virtual media from node %s\n", node)
	return err
}

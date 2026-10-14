package baremetal

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
)

// Virtual media (API 1.89): attach an ISO or disk image to a node's BMC
// without a deploy. python-ironicclient has no command for it (openstacksdk
// has attach_vmedia_to_node/detach_vmedia_from_node), so these are
// koc-native.

var featureNodeVmedia = ironicFeature{"baremetal node vmedia", "1.89", releaseCaracal}

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
	body := map[string]any{"device_type": deviceType, "image_url": image}
	setIf(body, "image_download_source", source)
	if err := ironicPost(ctx, client, joinPath("nodes", node, "vmedia"), body, nil); err != nil {
		return fmt.Errorf("attaching virtual media to baremetal node %s: %w", node,
			explainMicroversion(ctx, client, featureNodeVmedia, err))
	}
	_, err := fmt.Fprintf(w, "Attached %s as %s to node %s\n", image, deviceType, node)
	return err
}

func runNodeVmediaDetach(ctx context.Context, client *gophercloud.ServiceClient, node string, types []string, w io.Writer) error {
	path := joinPath("nodes", node, "vmedia")
	if len(types) > 0 {
		for _, t := range types {
			if !slices.Contains(vmediaDevices, t) {
				return fmt.Errorf("--device-type %s: must be one of %s", t, strings.Join(vmediaDevices, ", "))
			}
		}
		path += "?" + url.Values{"device_types": {strings.Join(types, ",")}}.Encode()
	}
	if err := ironicDelete(ctx, client, path); err != nil {
		return fmt.Errorf("detaching virtual media from baremetal node %s: %w", node,
			explainMicroversion(ctx, client, featureNodeVmedia, err))
	}
	_, err := fmt.Fprintf(w, "Detached virtual media from node %s\n", node)
	return err
}

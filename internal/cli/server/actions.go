package server

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/remoteconsoles"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/secgroups"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/volumeattach"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// simpleAction is a server-scoped action that only needs the resolved server ID.
type simpleAction func(ctx context.Context, client *gophercloud.ServiceClient, id string) error

// newSimpleActionCommand builds a "server <verb> <server>" command whose only
// behavior is to resolve the server reference to an ID and invoke fn.
func newSimpleActionCommand(a *auth.Options, o *output.Options, use, short, done string, fn simpleAction) *cobra.Command {
	return &cobra.Command{
		Use:   use + " <server>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newComputeClient(ctx, a)
			if err != nil {
				return err
			}
			return runSimpleAction(ctx, client, args[0], done, fn, cmd.OutOrStdout())
		},
	}
}

func runSimpleAction(ctx context.Context, client *gophercloud.ServiceClient, ref, done string, fn simpleAction, w io.Writer) error {
	id, err := resolveServerID(ctx, client, ref)
	if err != nil {
		return err
	}
	if err := fn(ctx, client, id); err != nil {
		return fmt.Errorf("%s server %q: %w", done, ref, err)
	}
	if _, err := fmt.Fprintf(w, "%s server %s\n", done, ref); err != nil {
		return err
	}
	return nil
}

func newServerStartCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return newSimpleActionCommand(a, o, "start", "Start a server", "Started",
		func(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
			return servers.Start(ctx, c, id).ExtractErr()
		})
}

func newServerStopCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return newSimpleActionCommand(a, o, "stop", "Stop a server", "Stopped",
		func(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
			return servers.Stop(ctx, c, id).ExtractErr()
		})
}

func newServerPauseCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return newSimpleActionCommand(a, o, "pause", "Pause a server", "Paused",
		func(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
			return servers.Pause(ctx, c, id).ExtractErr()
		})
}

func newServerUnpauseCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return newSimpleActionCommand(a, o, "unpause", "Unpause a server", "Unpaused",
		func(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
			return servers.Unpause(ctx, c, id).ExtractErr()
		})
}

func newServerSuspendCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return newSimpleActionCommand(a, o, "suspend", "Suspend a server", "Suspended",
		func(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
			return servers.Suspend(ctx, c, id).ExtractErr()
		})
}

func newServerResumeCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return newSimpleActionCommand(a, o, "resume", "Resume a suspended server", "Resumed",
		func(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
			return servers.Resume(ctx, c, id).ExtractErr()
		})
}

func newServerLockCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return newSimpleActionCommand(a, o, "lock", "Lock a server", "Locked",
		func(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
			return servers.Lock(ctx, c, id).ExtractErr()
		})
}

func newServerUnlockCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return newSimpleActionCommand(a, o, "unlock", "Unlock a server", "Unlocked",
		func(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
			return servers.Unlock(ctx, c, id).ExtractErr()
		})
}

func newServerMigrateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return newSimpleActionCommand(a, o, "migrate", "Cold-migrate a server to another host", "Migrated",
		func(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
			return servers.Migrate(ctx, c, id).ExtractErr()
		})
}

// reboot -----------------------------------------------------------------------

func newServerRebootCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var hard, soft bool
	cmd := &cobra.Command{
		Use:   "reboot <server>",
		Short: "Reboot a server",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			if hard && soft {
				return fmt.Errorf("--hard and --soft are mutually exclusive")
			}
			method := servers.SoftReboot
			if hard {
				method = servers.HardReboot
			}
			ctx := cmd.Context()
			client, err := newComputeClient(ctx, a)
			if err != nil {
				return err
			}
			return runServerReboot(ctx, client, args[0], method, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&hard, "hard", false, "perform a hard (power-cycle) reboot")
	fl.BoolVar(&soft, "soft", false, "perform a soft (OS-level) reboot (default)")
	return cmd
}

func runServerReboot(ctx context.Context, client *gophercloud.ServiceClient, ref string, method servers.RebootMethod, w io.Writer) error {
	id, err := resolveServerID(ctx, client, ref)
	if err != nil {
		return err
	}
	if err := servers.Reboot(ctx, client, id, servers.RebootOpts{Type: method}).ExtractErr(); err != nil {
		return fmt.Errorf("rebooting server %q: %w", ref, err)
	}
	if _, err := fmt.Fprintf(w, "Rebooted server %s (%s)\n", ref, method); err != nil {
		return err
	}
	return nil
}

// resize -----------------------------------------------------------------------

func newServerResizeCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var flavor string
	var confirm, revert bool
	cmd := &cobra.Command{
		Use:   "resize <server>",
		Short: "Resize a server to a new flavor, or confirm/revert a pending resize",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newComputeClient(ctx, a)
			if err != nil {
				return err
			}
			return runServerResize(ctx, client, args[0], flavor, confirm, revert, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&flavor, "flavor", "", "flavor ID or name to resize to")
	fl.BoolVar(&confirm, "confirm", false, "confirm a pending resize")
	fl.BoolVar(&revert, "revert", false, "revert a pending resize")
	return cmd
}

func runServerResize(ctx context.Context, client *gophercloud.ServiceClient, ref, flavor string, confirm, revert bool, w io.Writer) error {
	id, err := resolveServerID(ctx, client, ref)
	if err != nil {
		return err
	}
	switch {
	case confirm:
		if err := servers.ConfirmResize(ctx, client, id).ExtractErr(); err != nil {
			return fmt.Errorf("confirming resize of server %q: %w", ref, err)
		}
		if _, err := fmt.Fprintf(w, "Confirmed resize of server %s\n", ref); err != nil {
			return err
		}
	case revert:
		if err := servers.RevertResize(ctx, client, id).ExtractErr(); err != nil {
			return fmt.Errorf("reverting resize of server %q: %w", ref, err)
		}
		if _, err := fmt.Fprintf(w, "Reverted resize of server %s\n", ref); err != nil {
			return err
		}
	default:
		if flavor == "" {
			return fmt.Errorf("--flavor is required to resize (or use --confirm/--revert)")
		}
		flavorRef, err := resolveFlavorRef(ctx, client, flavor)
		if err != nil {
			return err
		}
		if err := servers.Resize(ctx, client, id, servers.ResizeOpts{FlavorRef: flavorRef}).ExtractErr(); err != nil {
			return fmt.Errorf("resizing server %q: %w", ref, err)
		}
		if _, err := fmt.Fprintf(w, "Resized server %s to flavor %s (confirm or revert when ready)\n", ref, flavor); err != nil {
			return err
		}
	}
	return nil
}

// rebuild ----------------------------------------------------------------------

func newServerRebuildCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var image string
	cmd := &cobra.Command{
		Use:   "rebuild <server>",
		Short: "Rebuild a server from an image",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			if image == "" {
				return fmt.Errorf("--image is required")
			}
			ctx := cmd.Context()
			client, err := newComputeClient(ctx, a)
			if err != nil {
				return err
			}
			return runServerRebuild(ctx, client, o, args[0], image, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&image, "image", "", "image ID to rebuild from (required; pass an ID)")
	return cmd
}

func runServerRebuild(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref, image string, w io.Writer) error {
	id, err := resolveServerID(ctx, client, ref)
	if err != nil {
		return err
	}
	s, err := servers.Rebuild(ctx, client, id, servers.RebuildOpts{ImageRef: image}).Extract()
	if err != nil {
		return fmt.Errorf("rebuilding server %q: %w", ref, err)
	}
	return o.WriteSingle(w, []string{"ID", "Name", "Status"}, []any{s.ID, s.Name, s.Status})
}

// volumes ----------------------------------------------------------------------

func newServerAddVolumeCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var device string
	cmd := &cobra.Command{
		Use:   "volume <server> <volume>",
		Short: "Attach a volume to a server",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newComputeClient(ctx, a)
			if err != nil {
				return err
			}
			return runServerAddVolume(ctx, client, args[0], args[1], device, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&device, "device", "", "device name to expose the volume as (default auto)")
	return cmd
}

func runServerAddVolume(ctx context.Context, client *gophercloud.ServiceClient, ref, volumeID, device string, w io.Writer) error {
	id, err := resolveServerID(ctx, client, ref)
	if err != nil {
		return err
	}
	if _, err := volumeattach.Create(ctx, client, id, volumeattach.CreateOpts{VolumeID: volumeID, Device: device}).Extract(); err != nil {
		return fmt.Errorf("attaching volume %q to server %q: %w", volumeID, ref, err)
	}
	if _, err := fmt.Fprintf(w, "Attached volume %s to server %s\n", volumeID, ref); err != nil {
		return err
	}
	return nil
}

func newServerRemoveVolumeCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "volume <server> <volume>",
		Short: "Detach a volume from a server",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newComputeClient(ctx, a)
			if err != nil {
				return err
			}
			return runServerRemoveVolume(ctx, client, args[0], args[1], cmd.OutOrStdout())
		},
	}
	return cmd
}

func runServerRemoveVolume(ctx context.Context, client *gophercloud.ServiceClient, ref, volumeID string, w io.Writer) error {
	id, err := resolveServerID(ctx, client, ref)
	if err != nil {
		return err
	}
	if err := volumeattach.Delete(ctx, client, id, volumeID).ExtractErr(); err != nil {
		return fmt.Errorf("detaching volume %q from server %q: %w", volumeID, ref, err)
	}
	if _, err := fmt.Fprintf(w, "Detached volume %s from server %s\n", volumeID, ref); err != nil {
		return err
	}
	return nil
}

// floating IPs -----------------------------------------------------------------
//
// gophercloud v2 has no compute floating-IP action helper (the historical
// os-floating-ips server actions were removed from the typed API), so these use
// the raw server-action endpoint directly. Documented as a raw fallback.

func serverActionRaw(ctx context.Context, client *gophercloud.ServiceClient, id string, body map[string]any) error {
	// The addFloatingIp/removeFloatingIp server actions were removed from nova
	// at microversion 2.44. The compute client negotiates "latest", so these
	// requests must be pinned to a pre-2.44 microversion or nova 404s. Setting
	// RequestOpts.MoreHeaders is not enough: service_client.go's
	// setMicroversionHeader overwrites X-OpenStack-Nova-API-Version from
	// client.Microversion on every Request. Shallow-copy the service client
	// (sharing the ProviderClient) and override its Microversion for this call.
	legacy := *client
	legacy.Microversion = "2.43"
	url := legacy.ServiceURL("servers", id, "action")
	resp, err := legacy.Post(ctx, url, body, nil, &gophercloud.RequestOpts{OkCodes: []int{200, 202}})
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	_, _, err = gophercloud.ParseResponse(resp, err)
	return err
}

func newServerAddFloatingIPCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var fixed string
	cmd := &cobra.Command{
		Use:   "ip <server> <ip-address>",
		Short: "Associate a floating IP with a server",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newComputeClient(ctx, a)
			if err != nil {
				return err
			}
			return runServerAddFloatingIP(ctx, client, args[0], args[1], fixed, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&fixed, "fixed-ip-address", "", "fixed IP to associate the floating IP with")
	return cmd
}

func runServerAddFloatingIP(ctx context.Context, client *gophercloud.ServiceClient, ref, address, fixed string, w io.Writer) error {
	id, err := resolveServerID(ctx, client, ref)
	if err != nil {
		return err
	}
	action := map[string]any{"address": address}
	if fixed != "" {
		action["fixed_address"] = fixed
	}
	if err := serverActionRaw(ctx, client, id, map[string]any{"addFloatingIp": action}); err != nil {
		return fmt.Errorf("associating floating IP %q with server %q: %w", address, ref, err)
	}
	if _, err := fmt.Fprintf(w, "Associated floating IP %s with server %s\n", address, ref); err != nil {
		return err
	}
	return nil
}

func newServerRemoveFloatingIPCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ip <server> <ip-address>",
		Short: "Disassociate a floating IP from a server",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newComputeClient(ctx, a)
			if err != nil {
				return err
			}
			return runServerRemoveFloatingIP(ctx, client, args[0], args[1], cmd.OutOrStdout())
		},
	}
	return cmd
}

func runServerRemoveFloatingIP(ctx context.Context, client *gophercloud.ServiceClient, ref, address string, w io.Writer) error {
	id, err := resolveServerID(ctx, client, ref)
	if err != nil {
		return err
	}
	body := map[string]any{"removeFloatingIp": map[string]any{"address": address}}
	if err := serverActionRaw(ctx, client, id, body); err != nil {
		return fmt.Errorf("disassociating floating IP %q from server %q: %w", address, ref, err)
	}
	if _, err := fmt.Fprintf(w, "Disassociated floating IP %s from server %s\n", address, ref); err != nil {
		return err
	}
	return nil
}

// security groups --------------------------------------------------------------

func newServerAddSecurityGroupCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "group <server> <group>",
		Short: "Add a security group to a server",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newComputeClient(ctx, a)
			if err != nil {
				return err
			}
			return runServerAddSecurityGroup(ctx, client, args[0], args[1], cmd.OutOrStdout())
		},
	}
	return cmd
}

func runServerAddSecurityGroup(ctx context.Context, client *gophercloud.ServiceClient, ref, group string, w io.Writer) error {
	id, err := resolveServerID(ctx, client, ref)
	if err != nil {
		return err
	}
	if err := secgroups.AddServer(ctx, client, id, group).ExtractErr(); err != nil {
		return fmt.Errorf("adding security group %q to server %q: %w", group, ref, err)
	}
	if _, err := fmt.Fprintf(w, "Added security group %s to server %s\n", group, ref); err != nil {
		return err
	}
	return nil
}

func newServerRemoveSecurityGroupCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "group <server> <group>",
		Short: "Remove a security group from a server",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newComputeClient(ctx, a)
			if err != nil {
				return err
			}
			return runServerRemoveSecurityGroup(ctx, client, args[0], args[1], cmd.OutOrStdout())
		},
	}
	return cmd
}

func runServerRemoveSecurityGroup(ctx context.Context, client *gophercloud.ServiceClient, ref, group string, w io.Writer) error {
	id, err := resolveServerID(ctx, client, ref)
	if err != nil {
		return err
	}
	if err := secgroups.RemoveServer(ctx, client, id, group).ExtractErr(); err != nil {
		return fmt.Errorf("removing security group %q from server %q: %w", group, ref, err)
	}
	if _, err := fmt.Fprintf(w, "Removed security group %s from server %s\n", group, ref); err != nil {
		return err
	}
	return nil
}

// console ----------------------------------------------------------------------

func newServerConsoleCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "console",
		Short: "Server console commands",
	}
	cmd.AddCommand(newConsoleLogShowCommand(a, o), newConsoleURLShowCommand(a, o))
	return cmd
}

func newConsoleLogShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var lines int
	cmd := &cobra.Command{
		Use:   "log show <server>",
		Short: "Show console log output for a server",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newComputeClient(ctx, a)
			if err != nil {
				return err
			}
			return runConsoleLogShow(ctx, client, args[0], lines, cmd.OutOrStdout())
		},
	}
	cmd.Flags().IntVar(&lines, "lines", 0, "number of lines to fetch from the end of the log (default all)")
	return cmd
}

func runConsoleLogShow(ctx context.Context, client *gophercloud.ServiceClient, ref string, lines int, w io.Writer) error {
	id, err := resolveServerID(ctx, client, ref)
	if err != nil {
		return err
	}
	out, err := servers.ShowConsoleOutput(ctx, client, id, servers.ShowConsoleOutputOpts{Length: lines}).Extract()
	if err != nil {
		return fmt.Errorf("fetching console log for server %q: %w", ref, err)
	}
	if _, err := fmt.Fprint(w, out); err != nil {
		return err
	}
	return nil
}

func newConsoleURLShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var novnc bool
	var consoleType string
	cmd := &cobra.Command{
		Use:   "url show <server>",
		Short: "Show a remote console URL for a server",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			t := consoleType
			if novnc {
				t = "novnc"
			}
			if t == "" {
				t = "novnc"
			}
			ctx := cmd.Context()
			client, err := newComputeClient(ctx, a)
			if err != nil {
				return err
			}
			return runConsoleURLShow(ctx, client, o, args[0], t, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&novnc, "novnc", false, "request a noVNC console (default)")
	fl.StringVar(&consoleType, "type", "", "console type: novnc, xvpvnc, spice-html5, serial, webmks")
	return cmd
}

func runConsoleURLShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref, consoleType string, w io.Writer) error {
	id, err := resolveServerID(ctx, client, ref)
	if err != nil {
		return err
	}
	protocol := remoteconsoles.ConsoleProtocolVNC
	switch remoteconsoles.ConsoleType(consoleType) {
	case remoteconsoles.ConsoleTypeSPICEHTML5:
		protocol = remoteconsoles.ConsoleProtocolSPICE
	case remoteconsoles.ConsoleTypeSerial:
		protocol = remoteconsoles.ConsoleProtocolSerial
	case remoteconsoles.ConsoleTypeWebMKS:
		protocol = remoteconsoles.ConsoleProtocolMKS
	}
	rc, err := remoteconsoles.Create(ctx, client, id, remoteconsoles.CreateOpts{
		Protocol: protocol,
		Type:     remoteconsoles.ConsoleType(consoleType),
	}).Extract()
	if err != nil {
		return fmt.Errorf("creating console for server %q: %w", ref, err)
	}
	return o.WriteSingle(w, []string{"Type", "Protocol", "URL"}, []any{rc.Type, rc.Protocol, rc.URL})
}

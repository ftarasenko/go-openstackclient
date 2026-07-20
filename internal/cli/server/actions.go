package server

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

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

// migratePollInterval and migratePollTimeout bound the --wait polling loop.
// Migrations of large instances can run for many minutes, so the default cap is
// generous; --wait-timeout overrides it.
const (
	migratePollInterval = 5 * time.Second
	migratePollTimeout  = 60 * time.Minute
)

// serverMigrateFlags holds the options accepted by "server migrate". Cold
// migration is the default; --live-migration switches to a live migration and
// unlocks its block-migration / disk-overcommit knobs.
type serverMigrateFlags struct {
	live            bool
	host            string
	blockMigration  bool
	sharedMigration bool
	diskOverCommit  bool
	wait            bool
	waitTimeout     time.Duration
}

func newServerMigrateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &serverMigrateFlags{}
	cmd := &cobra.Command{
		Use:   "migrate <server>",
		Short: "Migrate a server to another host (cold by default; --live-migration for live)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			if f.blockMigration && f.sharedMigration {
				return fmt.Errorf("--block-migration and --shared-migration are mutually exclusive")
			}
			if !f.live && (f.blockMigration || f.diskOverCommit) {
				return fmt.Errorf("--block-migration and --disk-overcommit require --live-migration")
			}
			ctx := cmd.Context()
			client, err := newComputeClient(ctx, a)
			if err != nil {
				return err
			}
			return runServerMigrate(ctx, client, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&f.live, "live-migration", false, "perform a live (non-disruptive) migration instead of a cold one")
	fl.StringVar(&f.host, "host", "", "target host (omit to let the scheduler choose)")
	fl.BoolVar(&f.blockMigration, "block-migration", false, "live migration only: force a block migration (copy local disks)")
	fl.BoolVar(&f.sharedMigration, "shared-migration", false, "live migration only: force a shared-storage migration (no disk copy)")
	fl.BoolVar(&f.diskOverCommit, "disk-overcommit", false, "live migration only: allow disk over-commit on the destination (compute API <= 2.24)")
	fl.BoolVar(&f.wait, "wait", false, "wait for the migration to finish (server reaches ACTIVE, or VERIFY_RESIZE for a cold migration)")
	fl.DurationVar(&f.waitTimeout, "wait-timeout", migratePollTimeout, "maximum time to wait for --wait to complete")
	return cmd
}

func runServerMigrate(ctx context.Context, client *gophercloud.ServiceClient, ref string, f *serverMigrateFlags, w io.Writer) error {
	id, err := resolveServerID(ctx, client, ref)
	if err != nil {
		return err
	}
	if f.live {
		// Build the os-migrateLive body directly rather than via gophercloud's
		// servers.LiveMigrateOpts: its BlockMigration is a *bool (omitempty), so it
		// cannot send the "auto" string nova requires at microversion >= 2.25 and
		// drops block_migration entirely when unset — while always emitting
		// host:null — which nova 400s with "'block_migration' is a required
		// property".
		live, err := liveMigrateBody(client, f, w)
		if err != nil {
			return err
		}
		body := map[string]any{"os-migrateLive": live}
		if err := serverActionNegotiated(ctx, client, id, body); err != nil {
			return fmt.Errorf("live-migrating server %q: %w", ref, err)
		}
		if _, err := fmt.Fprintf(w, "Requested live migration of server %s\n", ref); err != nil {
			return err
		}
		return waitForMigration(ctx, client, ref, id, f, w)
	}

	// Cold migration. gophercloud's servers.Migrate posts {"migrate": null} with
	// no host; when a target host is requested (nova 2.56+) build the action body
	// directly so the host reaches nova.
	if f.host != "" {
		body := map[string]any{"migrate": map[string]any{"host": f.host}}
		if err := serverActionNegotiated(ctx, client, id, body); err != nil {
			return fmt.Errorf("migrating server %q: %w", ref, err)
		}
	} else if err := servers.Migrate(ctx, client, id).ExtractErr(); err != nil {
		return fmt.Errorf("migrating server %q: %w", ref, err)
	}
	if _, err := fmt.Fprintf(w, "Migrated server %s\n", ref); err != nil {
		return err
	}
	return waitForMigration(ctx, client, ref, id, f, w)
}

// liveMigrateBody builds the os-migrateLive action body, mirroring OSC's
// "server migrate --live-migration". nova requires block_migration: it defaults
// to "auto" at microversion >= 2.25 (nova picks block vs shared from the
// instance's storage — the right default for shared storage), and false below;
// --block-migration / --shared-migration force it. host is part of the body but
// nullable/optional at >= 2.30, so it is included only when a target is given.
// disk_over_commit exists only at <= 2.24, so it is sent there and dropped (with
// a note) at higher microversions.
func liveMigrateBody(client *gophercloud.ServiceClient, f *serverMigrateFlags, w io.Writer) (map[string]any, error) {
	live := map[string]any{}
	supports225 := computeSupportsMicroversion(client, "2.25")
	switch {
	case f.blockMigration:
		live["block_migration"] = true
	case f.sharedMigration:
		live["block_migration"] = false
	case supports225:
		live["block_migration"] = "auto"
	default:
		live["block_migration"] = false
	}
	if f.host != "" {
		live["host"] = f.host
	}
	if !supports225 {
		live["disk_over_commit"] = f.diskOverCommit
	} else if f.diskOverCommit {
		if _, err := fmt.Fprintln(w, "warning: --disk-overcommit is only honored at compute API microversion <= 2.24; ignoring"); err != nil {
			return nil, err
		}
	}
	return live, nil
}

// computeSupportsMicroversion reports whether the compute client's negotiated
// microversion is at least want. "latest" (koc's default) supports everything;
// an unset microversion is nova's 2.1 baseline and supports nothing newer.
func computeSupportsMicroversion(client *gophercloud.ServiceClient, want string) bool {
	if client.Microversion == "latest" {
		return true
	}
	hMaj, hMin, ok := parseMicroversion(client.Microversion)
	if !ok {
		return false
	}
	wMaj, wMin, _ := parseMicroversion(want)
	if hMaj != wMaj {
		return hMaj > wMaj
	}
	return hMin >= wMin
}

func parseMicroversion(v string) (major, minor int, ok bool) {
	majStr, minStr, found := strings.Cut(v, ".")
	if !found {
		return 0, 0, false
	}
	major, err1 := strconv.Atoi(majStr)
	minor, err2 := strconv.Atoi(minStr)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return major, minor, true
}

// waitForMigration polls the server until its migration settles, when --wait is
// set. Success mirrors OSC's "server migrate --wait": the server reaches ACTIVE
// (live migration) or VERIFY_RESIZE (cold migration, awaiting confirm) with no
// task in flight; an ERROR status is terminal. task_state gates the ACTIVE check
// so a live migration is not reported done before nova starts it (status stays
// ACTIVE while task_state is "migrating").
func waitForMigration(ctx context.Context, client *gophercloud.ServiceClient, ref, id string, f *serverMigrateFlags, w io.Writer) error {
	if !f.wait {
		return nil
	}
	timeout := f.waitTimeout
	if timeout <= 0 {
		timeout = migratePollTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(migratePollInterval)
	defer ticker.Stop()

	var getErrors int
	for {
		var s struct {
			Status    string `json:"status"`
			TaskState string `json:"OS-EXT-STS:task_state"`
		}
		if err := servers.Get(ctx, client, id).ExtractInto(&s); err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("waiting for migration of server %q: %w", ref, ctx.Err())
			}
			// Tolerate a few consecutive transient Get errors before giving up.
			getErrors++
			if getErrors > maxConsecutiveGetErrors {
				return fmt.Errorf("polling server %q during migration: %w", ref, err)
			}
		} else {
			getErrors = 0
			switch {
			case strings.EqualFold(s.Status, "ERROR"):
				return fmt.Errorf("server %q entered ERROR status during migration", ref)
			case s.TaskState == "" && (strings.EqualFold(s.Status, "ACTIVE") || strings.EqualFold(s.Status, "VERIFY_RESIZE")):
				if _, err := fmt.Fprintf(w, "Server %s migration complete (status %s)\n", ref, s.Status); err != nil {
					return err
				}
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for migration of server %q: %w", ref, ctx.Err())
		case <-ticker.C:
		}
	}
}

// maxConsecutiveGetErrors bounds how many consecutive servers.Get failures the
// --wait poll tolerates before giving up; the counter resets on any success.
const maxConsecutiveGetErrors = 5

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
	// Two-word OSC nouns ("console log show", "console url show") are modeled as
	// nested parent commands so cobra resolves them unambiguously — otherwise a
	// flat "log show <server>" Use string names the command "log" and treats
	// "show" as a positional arg, breaking the documented invocation.
	logParent := &cobra.Command{Use: "log", Short: "Server console log"}
	logParent.AddCommand(newConsoleLogShowCommand(a, o))
	urlParent := &cobra.Command{Use: "url", Short: "Server remote console URL"}
	urlParent.AddCommand(newConsoleURLShowCommand(a, o))
	cmd.AddCommand(logParent, urlParent)
	return cmd
}

func newConsoleLogShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var lines int
	cmd := &cobra.Command{
		Use:   "show <server>",
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
	var novnc, xvpvnc, spice, serial, mks bool
	var consoleType string
	cmd := &cobra.Command{
		Use:   "show <server>",
		Short: "Show a remote console URL for a server",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			t, err := consoleTypeFromFlags(novnc, xvpvnc, spice, serial, mks, consoleType)
			if err != nil {
				return err
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
	// Discrete OSC-style flags, one per console type; mutually exclusive.
	fl.BoolVar(&novnc, "novnc", false, "request a noVNC console (default)")
	fl.BoolVar(&xvpvnc, "xvpvnc", false, "request an XVP VNC console")
	fl.BoolVar(&spice, "spice", false, "request a SPICE HTML5 console")
	fl.BoolVar(&serial, "serial", false, "request a serial console")
	fl.BoolVar(&mks, "mks", false, "request a WebMKS console")
	// --type is a hidden back-compat alias for the pre-existing scheme; the
	// discrete flags above are preferred and take precedence.
	fl.StringVar(&consoleType, "type", "", "console type: novnc, xvpvnc, spice-html5, serial, webmks")
	_ = fl.MarkHidden("type")
	return cmd
}

// consoleTypeFromFlags maps the mutually-exclusive discrete console flags (and
// the hidden --type alias) to the remoteconsoles console-type string. When none
// is set it defaults to noVNC. The discrete flags win over --type.
func consoleTypeFromFlags(novnc, xvpvnc, spice, serial, mks bool, consoleType string) (string, error) {
	set := map[string]bool{
		string(remoteconsoles.ConsoleTypeNoVNC):      novnc,
		string(remoteconsoles.ConsoleTypeXVPVNC):     xvpvnc,
		string(remoteconsoles.ConsoleTypeSPICEHTML5): spice,
		string(remoteconsoles.ConsoleTypeSerial):     serial,
		string(remoteconsoles.ConsoleTypeWebMKS):     mks,
	}
	var chosen []string
	for t, on := range set {
		if on {
			chosen = append(chosen, t)
		}
	}
	switch len(chosen) {
	case 0:
		if consoleType != "" {
			return consoleType, nil
		}
		return string(remoteconsoles.ConsoleTypeNoVNC), nil
	case 1:
		return chosen[0], nil
	default:
		return "", fmt.Errorf("--novnc/--xvpvnc/--spice/--serial/--mks are mutually exclusive")
	}
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

// serverActionNegotiated posts a server action at the compute client's
// negotiated microversion (default "latest"), unlike serverActionRaw which
// pins to 2.43 for the floating-IP actions removed at 2.44. Used by the
// KeyStack dynamic server-group actions (addServerGroup / removeServerGroup).
func serverActionNegotiated(ctx context.Context, client *gophercloud.ServiceClient, id string, body map[string]any) error {
	url := client.ServiceURL("servers", id, "action")
	resp, err := client.Post(ctx, url, body, nil, &gophercloud.RequestOpts{OkCodes: []int{200, 202}})
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	_, _, err = gophercloud.ParseResponse(resp, err)
	return err
}

// newServerAddServerGroupCommand implements "server add server-group <server>
// <server-group-id>" — the KeyStack dynamic-server-group extension (KCP-703),
// which adds a running server to a server group via the addServerGroup action.
// Vanilla nova has no such action and rejects it with HTTP 400.
func newServerAddServerGroupCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "server-group <server> <server-group-id>",
		Short: "Add a server to a server group (KeyStack dynamic server groups)",
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
			return runServerAddServerGroup(ctx, client, args[0], args[1], cmd.OutOrStdout())
		},
	}
	return cmd
}

func runServerAddServerGroup(ctx context.Context, client *gophercloud.ServiceClient, ref, groupID string, w io.Writer) error {
	id, err := resolveServerID(ctx, client, ref)
	if err != nil {
		return err
	}
	body := map[string]any{"addServerGroup": map[string]any{"server_group_id": groupID}}
	if err := serverActionNegotiated(ctx, client, id, body); err != nil {
		return keystackExtErr(fmt.Errorf("adding server %q to server group %q: %w", ref, groupID, err), "dynamic server groups (addServerGroup)")
	}
	if _, err := fmt.Fprintf(w, "Added server %s to server group %s\n", ref, groupID); err != nil {
		return err
	}
	return nil
}

// newServerRemoveServerGroupCommand implements "server remove server-group
// <server>" — the KeyStack removeServerGroup action (KCP-703). Nova infers the
// group from the server, so no group ID is required.
func newServerRemoveServerGroupCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "server-group <server>",
		Short: "Remove a server from its server group (KeyStack dynamic server groups)",
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
			return runServerRemoveServerGroup(ctx, client, args[0], cmd.OutOrStdout())
		},
	}
	return cmd
}

func runServerRemoveServerGroup(ctx context.Context, client *gophercloud.ServiceClient, ref string, w io.Writer) error {
	id, err := resolveServerID(ctx, client, ref)
	if err != nil {
		return err
	}
	body := map[string]any{"removeServerGroup": nil}
	if err := serverActionNegotiated(ctx, client, id, body); err != nil {
		return keystackExtErr(fmt.Errorf("removing server %q from its server group: %w", ref, err), "dynamic server groups (removeServerGroup)")
	}
	if _, err := fmt.Fprintf(w, "Removed server %s from its server group\n", ref); err != nil {
		return err
	}
	return nil
}

// newServerEvacuateCommand implements "server evacuate <server>" — rebuild a
// server on a new host after its hypervisor has failed. host/password are
// standard nova; --preserve-ephemeral is a KeyStack extension (added to the
// evacuate action body downstream) that vanilla nova rejects with HTTP 400.
func newServerEvacuateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var host, password string
	var preserveEphemeral bool
	cmd := &cobra.Command{
		Use:   "evacuate <server>",
		Short: "Evacuate a server from a failed host to another host",
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
			return runServerEvacuate(ctx, client, args[0], host, password, preserveEphemeral, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&host, "host", "", "target host to evacuate to (omit to let the scheduler choose)")
	fl.StringVar(&password, "password", "", "set this admin password on the evacuated server")
	fl.BoolVar(&preserveEphemeral, "preserve-ephemeral", false, "KeyStack: preserve the ephemeral partition during evacuation")
	return cmd
}

func runServerEvacuate(ctx context.Context, client *gophercloud.ServiceClient, ref, host, password string, preserveEphemeral bool, w io.Writer) error {
	id, err := resolveServerID(ctx, client, ref)
	if err != nil {
		return err
	}
	// Build the evacuate action body directly rather than via gophercloud's
	// servers.EvacuateOpts, which is frozen at the pre-2.14 shape (it always
	// emits onSharedStorage and lacks preserve_ephemeral); nova negotiates
	// "latest", where that body is rejected.
	action := map[string]any{}
	if host != "" {
		action["host"] = host
	}
	if password != "" {
		action["adminPass"] = password
	}
	if preserveEphemeral {
		action["preserve_ephemeral"] = true
	}
	if err := serverActionNegotiated(ctx, client, id, map[string]any{"evacuate": action}); err != nil {
		if preserveEphemeral {
			return keystackExtErr(fmt.Errorf("evacuating server %q: %w", ref, err), "evacuate preserve_ephemeral")
		}
		return fmt.Errorf("evacuating server %q: %w", ref, err)
	}
	if _, err := fmt.Fprintf(w, "Requested evacuation of server %s\n", ref); err != nil {
		return err
	}
	return nil
}

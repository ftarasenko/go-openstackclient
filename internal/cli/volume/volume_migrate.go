package volume

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
	volumeapi "github.com/ftarasenko/go-openstackclient/pkg/openstack/volume"
)

// volumeMigrateFlags holds the options accepted by "volume migrate".
//
// Flag names follow upstream OSC (`openstack volume migrate`); the KeyStack
// reference (docs.keystack.ru) returned HTTP 403 at implementation time, so the
// surface is UNVERIFIED against KeyStack and falls back to upstream OSC.
type volumeMigrateFlags struct {
	host          string
	forceHostCopy bool
	lockVolume    bool
	wait          bool
	waitTimeout   time.Duration
}

func newVolumeMigrateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &volumeMigrateFlags{}
	cmd := &cobra.Command{
		Use:   "migrate <volume>",
		Short: "Migrate a volume to a new host",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newVolumeClient(ctx, a)
			if err != nil {
				return err
			}
			return runVolumeMigrate(ctx, client, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.host, "host", "", "destination host, as host@backend-name#pool (required)")
	fl.BoolVar(&f.forceHostCopy, "force-host-copy", false, "force generic host-based copy, bypassing driver optimizations")
	fl.BoolVar(&f.lockVolume, "lock-volume", false, "lock the volume so the migration cannot be aborted")
	fl.BoolVar(&f.wait, "wait", false, "wait for the migration to finish (polls the admin-only migration_status)")
	fl.DurationVar(&f.waitTimeout, "wait-timeout", volumePollTimeout, "maximum time to wait for --wait to complete")
	if err := cmd.MarkFlagRequired("host"); err != nil {
		panic(err)
	}
	return cmd
}

// runVolumeMigrate starts an os-migrate_volume and, with --wait, follows it.
func runVolumeMigrate(ctx context.Context, client *gophercloud.ServiceClient, ref string, f *volumeMigrateFlags, w io.Writer) error {
	if f.host == "" {
		return fmt.Errorf("--host is required")
	}
	id, err := resolveVolumeID(ctx, client, ref)
	if err != nil {
		return err
	}
	opts := volumeapi.MigrateOpts{Host: f.host, ForceHostCopy: f.forceHostCopy, LockVolume: f.lockVolume}
	if err := volumeapi.MigrateVolume(ctx, client, id, opts); err != nil {
		return relabel(err, "migrating volume %q", ref)
	}
	if _, err := fmt.Fprintf(w, "Migrating volume %s to host %s\n", id, f.host); err != nil {
		return err
	}
	if f.wait {
		return waitForVolumeMigration(ctx, client, ref, id, f.host, f.waitTimeout, w)
	}
	return nil
}

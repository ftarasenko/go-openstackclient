package volume

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
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
	if err := cmd.MarkFlagRequired("host"); err != nil {
		panic(err)
	}
	return cmd
}

// runVolumeMigrate posts os-migrate_volume. gophercloud v2's blockstorage/v3
// volumes package has no typed migrate call, so this uses the raw volume action
// endpoint (isolated here so it is easy to replace if one lands upstream).
func runVolumeMigrate(ctx context.Context, client *gophercloud.ServiceClient, ref string, f *volumeMigrateFlags, w io.Writer) error {
	if f.host == "" {
		return fmt.Errorf("--host is required")
	}
	id, err := resolveVolumeID(ctx, client, ref)
	if err != nil {
		return err
	}

	body := map[string]any{"os-migrate_volume": map[string]any{
		"host":            f.host,
		"force_host_copy": f.forceHostCopy,
		"lock_volume":     f.lockVolume,
	}}
	url := client.ServiceURL("volumes", id, "action")
	resp, err := client.Post(ctx, url, body, nil, &gophercloud.RequestOpts{OkCodes: []int{202}})
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if _, _, err = gophercloud.ParseResponse(resp, err); err != nil {
		return fmt.Errorf("migrating volume %q: %w", ref, err)
	}
	if _, err := fmt.Fprintf(w, "Migrating volume %s to host %s\n", id, f.host); err != nil {
		return err
	}
	return nil
}

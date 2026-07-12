package volume

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/backups"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// newBackupCommand builds "volume backup ...".
//
// Flag names follow upstream OSC (`openstack volume backup ...`); the KeyStack
// reference (docs.keystack.ru) returned HTTP 403 at implementation time, so the
// surface is UNVERIFIED against KeyStack and falls back to upstream OSC.
func newBackupCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Manage volume backups",
	}
	cmd.AddCommand(newBackupListCommand(a, o))
	cmd.AddCommand(newBackupShowCommand(a, o))
	cmd.AddCommand(newBackupCreateCommand(a, o))
	cmd.AddCommand(newBackupDeleteCommand(a, o))
	cmd.AddCommand(newBackupRestoreCommand(a, o))
	return cmd
}

func backupShowFields(b *backups.Backup) ([]string, []any) {
	fields := []string{
		"id", "name", "description", "status", "size", "volume_id",
		"snapshot_id", "container", "is_incremental", "has_dependent_backups",
		"fail_reason", "created_at", "updated_at",
	}
	values := []any{
		b.ID, b.Name, b.Description, b.Status, b.Size, b.VolumeID,
		b.SnapshotID, b.Container, b.IsIncremental, b.HasDependentBackups,
		b.FailReason, b.CreatedAt, b.UpdatedAt,
	}
	return fields, values
}

type backupListFlags struct {
	allProjects bool
	name        string
	status      string
	volume      string
	limit       int
	marker      string
}

func newBackupListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &backupListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List volume backups",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newVolumeClient(ctx, a)
			if err != nil {
				return err
			}
			return runBackupList(ctx, client, o, f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&f.allProjects, "all-projects", false, "list backups from all projects (admin)")
	fl.StringVar(&f.name, "name", "", "filter by backup name")
	fl.StringVar(&f.status, "status", "", "filter by backup status")
	fl.StringVar(&f.volume, "volume", "", "filter by source volume ID")
	fl.IntVar(&f.limit, "limit", 0, "maximum number of backups to return")
	fl.StringVar(&f.marker, "marker", "", "list backups after this ID (pagination)")
	return cmd
}

func runBackupList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *backupListFlags, w io.Writer) error {
	opts := backups.ListOpts{
		AllTenants: f.allProjects,
		Name:       f.name,
		Status:     f.status,
		VolumeID:   f.volume,
		Limit:      f.limit,
		Marker:     f.marker,
	}
	pages, err := backups.List(client, opts).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing backups: %w", err)
	}
	all, err := backups.ExtractBackups(pages)
	if err != nil {
		return fmt.Errorf("parsing backup list: %w", err)
	}
	// Limit is only the page size to cinder; enforce it as a hard result cap.
	if f.limit > 0 && len(all) > f.limit {
		all = all[:f.limit]
	}
	t := output.Table{Columns: []string{"ID", "Name", "Description", "Status", "Size"}}
	for _, b := range all {
		t.Rows = append(t.Rows, []any{b.ID, b.Name, b.Description, b.Status, b.Size})
	}
	return o.WriteList(w, t)
}

func newBackupShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <backup>",
		Short: "Show backup details",
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
			return runBackupShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runBackupShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, w io.Writer) error {
	id, err := resolveBackupID(ctx, client, ref)
	if err != nil {
		return err
	}
	b, err := backups.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("getting backup %q: %w", ref, err)
	}
	fields, values := backupShowFields(b)
	return o.WriteSingle(w, fields, values)
}

type backupCreateFlags struct {
	name        string
	description string
	incremental bool
	force       bool
	snapshot    string
	container   string
}

func newBackupCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &backupCreateFlags{}
	cmd := &cobra.Command{
		Use:   "create <volume>",
		Short: "Create a backup of a volume",
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
			return runBackupCreate(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "backup name")
	fl.StringVar(&f.description, "description", "", "backup description")
	fl.BoolVar(&f.incremental, "incremental", false, "create an incremental backup")
	fl.BoolVar(&f.force, "force", false, "back up a volume even if attached/in-use")
	fl.StringVar(&f.snapshot, "snapshot", "", "source snapshot (ID or name) to back up")
	fl.StringVar(&f.container, "container", "", "backup container/bucket to store the backup in")
	return cmd
}

func runBackupCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, volumeRef string, f *backupCreateFlags, w io.Writer) error {
	volID, err := resolveVolumeID(ctx, client, volumeRef)
	if err != nil {
		return err
	}
	opts := backups.CreateOpts{
		VolumeID:    volID,
		Name:        f.name,
		Description: f.description,
		Incremental: f.incremental,
		Force:       f.force,
		Container:   f.container,
	}
	// Resolve a --snapshot reference (ID or name) to a snapshot ID.
	if f.snapshot != "" {
		snapID, err := resolveSnapshotID(ctx, client, f.snapshot)
		if err != nil {
			return err
		}
		opts.SnapshotID = snapID
	}
	b, err := backups.Create(ctx, client, opts).Extract()
	if err != nil {
		return fmt.Errorf("creating backup: %w", err)
	}
	fields, values := backupShowFields(b)
	return o.WriteSingle(w, fields, values)
}

func newBackupDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <backup>...",
		Short: "Delete one or more backups",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newVolumeClient(ctx, a)
			if err != nil {
				return err
			}
			return runBackupDelete(ctx, client, args, cmd.OutOrStdout())
		},
	}
}

func runBackupDelete(ctx context.Context, client *gophercloud.ServiceClient, refs []string, w io.Writer) error {
	var errs []error
	for _, ref := range refs {
		id, err := resolveBackupID(ctx, client, ref)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if err := backups.Delete(ctx, client, id).ExtractErr(); err != nil {
			errs = append(errs, fmt.Errorf("deleting backup %q: %w", ref, err))
			continue
		}
		if _, err := fmt.Fprintf(w, "Deleted backup: %s\n", ref); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

type backupRestoreFlags struct {
	volume string
}

func newBackupRestoreCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &backupRestoreFlags{}
	cmd := &cobra.Command{
		Use:   "restore <backup>",
		Short: "Restore a backup to a volume",
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
			return runBackupRestore(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&f.volume, "volume", "", "target volume (ID or name) to restore into; a new volume is created if omitted")
	return cmd
}

func runBackupRestore(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, backupRef string, f *backupRestoreFlags, w io.Writer) error {
	backupID, err := resolveBackupID(ctx, client, backupRef)
	if err != nil {
		return err
	}
	opts := backups.RestoreOpts{}
	if f.volume != "" {
		volID, err := resolveVolumeID(ctx, client, f.volume)
		if err != nil {
			return err
		}
		opts.VolumeID = volID
	}
	r, err := backups.RestoreFromBackup(ctx, client, backupID, opts).Extract()
	if err != nil {
		return fmt.Errorf("restoring backup %q: %w", backupRef, err)
	}
	fields := []string{"backup_id", "volume_id", "volume_name"}
	values := []any{r.BackupID, r.VolumeID, r.VolumeName}
	return o.WriteSingle(w, fields, values)
}

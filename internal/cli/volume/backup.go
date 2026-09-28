package volume

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/backups"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
	"github.com/ftarasenko/go-openstackclient/internal/cli/paging"
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
	cmd.AddCommand(newBackupSetCommand(a, o))
	cmd.AddCommand(newBackupUnsetCommand(a, o))
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
	long        bool
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
	fl.BoolVar(&f.long, "long", false, "list additional fields in output")
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
	// Limit is only the page size to cinder; enforce it as a hard result cap.
	all, err := paging.Collect(ctx, backups.ListDetail(client, backupListDetailOpts{opts}), f.limit, backups.ExtractBackups)
	if err != nil {
		return fmt.Errorf("listing backups: %w", err)
	}
	var names map[string]string
	if f.long {
		names = volumeNamesForTable(ctx, client, o)
	}
	return o.WriteList(w, backupListTable(all, f.long, names))
}

// backupListTable renders the listing in upstream's columns
// (python-openstackclient 10.3.0, volume/v3/volume_backup.py
// ListVolumeBackup): Incremental and Created At by default, and --long adds
// Availability Zone, Volume and Container.
func backupListTable(list []backups.Backup, long bool, volumeNames map[string]string) output.Table {
	cols := []string{"ID", "Name", "Description", "Status", "Size", "Incremental", "Created At"}
	if long {
		cols = append(cols, "Availability Zone", "Volume", "Container")
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(list))}
	for _, b := range list {
		row := []any{b.ID, b.Name, b.Description, b.Status, b.Size, b.IsIncremental, b.CreatedAt}
		if long {
			// availability_zone is absent below cinder 3.51; a nil pointer
			// renders as null rather than as a JSON-quoted string.
			var az any
			if b.AvailabilityZone != nil {
				az = *b.AvailabilityZone
			}
			row = append(row, az, volumeLabel(b.VolumeID, volumeNames), b.Container)
		}
		t.Rows = append(t.Rows, row)
	}
	return t
}

// backupListDetailOpts sends ListOpts's filters to cinder's detail listing.
//
// GET /backups is cinder's summary view — id, name and links, nothing else
// (cinder/api/views/backups.py summary) — so a listing read from it showed an
// empty Description, Status and Size for every backup. /backups/detail takes
// the same name, status and volume_id filters, but gophercloud's
// ListDetailOpts does not model them, hence this adapter over ListOpts.
type backupListDetailOpts struct{ backups.ListOpts }

func (opts backupListDetailOpts) ToBackupListDetailQuery() (string, error) {
	return opts.ToBackupListQuery()
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
	return batchdelete.Each(refs, func(ref string) error {
		id, err := resolveBackupID(ctx, client, ref)
		if err != nil {
			return err
		}
		if err := backups.Delete(ctx, client, id).ExtractErr(); err != nil {
			return fmt.Errorf("deleting backup %q: %w", ref, err)
		}
		if _, err := fmt.Fprintf(w, "Deleted backup: %s\n", ref); err != nil {
			return err
		}
		return nil
	})
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

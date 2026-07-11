package volume

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/snapshots"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// newSnapshotCommand builds "volume snapshot ...".
//
// Flag names follow upstream OSC (`openstack volume snapshot ...`); the KeyStack
// reference (docs.keystack.ru) returned HTTP 403 at implementation time, so the
// surface is UNVERIFIED against KeyStack and falls back to upstream OSC.
func newSnapshotCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "snapshot",
		Short: "Manage volume snapshots",
	}
	cmd.AddCommand(newSnapshotListCommand(a, o))
	cmd.AddCommand(newSnapshotShowCommand(a, o))
	cmd.AddCommand(newSnapshotCreateCommand(a, o))
	cmd.AddCommand(newSnapshotDeleteCommand(a, o))
	return cmd
}

func snapshotShowFields(s *snapshots.Snapshot) ([]string, []any) {
	fields := []string{
		"id", "name", "description", "status", "size", "volume_id",
		"metadata", "created_at", "updated_at",
	}
	values := []any{
		s.ID, s.Name, s.Description, s.Status, s.Size, s.VolumeID,
		s.Metadata, s.CreatedAt, s.UpdatedAt,
	}
	return fields, values
}

type snapshotListFlags struct {
	allProjects bool
	name        string
	status      string
	volume      string
}

func newSnapshotListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &snapshotListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List volume snapshots",
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
			return runSnapshotList(ctx, client, o, f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&f.allProjects, "all-projects", false, "list snapshots from all projects (admin)")
	fl.StringVar(&f.name, "name", "", "filter by snapshot name")
	fl.StringVar(&f.status, "status", "", "filter by snapshot status")
	fl.StringVar(&f.volume, "volume", "", "filter by source volume ID")
	return cmd
}

func runSnapshotList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *snapshotListFlags, w io.Writer) error {
	opts := snapshots.ListOpts{
		AllTenants: f.allProjects,
		Name:       f.name,
		Status:     f.status,
		VolumeID:   f.volume,
	}
	pages, err := snapshots.List(client, opts).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing snapshots: %w", err)
	}
	all, err := snapshots.ExtractSnapshots(pages)
	if err != nil {
		return fmt.Errorf("parsing snapshot list: %w", err)
	}
	t := output.Table{Columns: []string{"ID", "Name", "Description", "Status", "Size"}}
	for _, s := range all {
		t.Rows = append(t.Rows, []any{s.ID, s.Name, s.Description, s.Status, s.Size})
	}
	return o.WriteList(w, t)
}

func newSnapshotShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <snapshot>",
		Short: "Show snapshot details",
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
			return runSnapshotShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runSnapshotShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, w io.Writer) error {
	id, err := resolveSnapshotID(ctx, client, ref)
	if err != nil {
		return err
	}
	s, err := snapshots.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("getting snapshot %q: %w", ref, err)
	}
	fields, values := snapshotShowFields(s)
	return o.WriteSingle(w, fields, values)
}

type snapshotCreateFlags struct {
	name        string
	description string
	force       bool
}

func newSnapshotCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &snapshotCreateFlags{}
	cmd := &cobra.Command{
		Use:   "create <volume>",
		Short: "Create a snapshot of a volume",
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
			return runSnapshotCreate(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "snapshot name")
	fl.StringVar(&f.description, "description", "", "snapshot description")
	fl.BoolVar(&f.force, "force", false, "snapshot a volume even if attached/in-use")
	return cmd
}

func runSnapshotCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, volumeRef string, f *snapshotCreateFlags, w io.Writer) error {
	volID, err := resolveVolumeID(ctx, client, volumeRef)
	if err != nil {
		return err
	}
	opts := snapshots.CreateOpts{
		VolumeID:    volID,
		Name:        f.name,
		Description: f.description,
		Force:       f.force,
	}
	s, err := snapshots.Create(ctx, client, opts).Extract()
	if err != nil {
		return fmt.Errorf("creating snapshot: %w", err)
	}
	fields, values := snapshotShowFields(s)
	return o.WriteSingle(w, fields, values)
}

func newSnapshotDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <snapshot>...",
		Short: "Delete one or more snapshots",
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
			return runSnapshotDelete(ctx, client, args, cmd.OutOrStdout())
		},
	}
}

func runSnapshotDelete(ctx context.Context, client *gophercloud.ServiceClient, refs []string, w io.Writer) error {
	for _, ref := range refs {
		id, err := resolveSnapshotID(ctx, client, ref)
		if err != nil {
			return err
		}
		if err := snapshots.Delete(ctx, client, id).ExtractErr(); err != nil {
			return fmt.Errorf("deleting snapshot %q: %w", ref, err)
		}
		if _, err := fmt.Fprintf(w, "Deleted snapshot: %s\n", ref); err != nil {
			return err
		}
	}
	return nil
}

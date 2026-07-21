// Package volume implements the "koc volume" command tree, mirroring the
// upstream "openstack volume" (cinder block-storage v3) noun-verb surface,
// including the snapshot/backup/type/service subgroups.
package volume

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/volumes"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/resolve"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// NewCommand builds the "volume" command group and returns it as a slice so the
// root command can splice it (and any sibling groups) into its tree. The
// snapshot, backup, type and service subgroups live under "volume" as
// "volume snapshot", "volume backup", "volume type" and "volume service".
func NewCommand(a *auth.Options, o *output.Options) []*cobra.Command {
	cmd := &cobra.Command{
		Use:     "volume",
		Short:   "Block storage (cinder) commands",
		Aliases: []string{"vol"},
	}
	cmd.AddCommand(newVolumeListCommand(a, o))
	cmd.AddCommand(newVolumeShowCommand(a, o))
	cmd.AddCommand(newVolumeCreateCommand(a, o))
	cmd.AddCommand(newVolumeDeleteCommand(a, o))
	cmd.AddCommand(newVolumeSetCommand(a, o))
	cmd.AddCommand(newVolumeUnsetCommand(a, o))

	cmd.AddCommand(newSnapshotCommand(a, o))
	cmd.AddCommand(newBackupCommand(a, o))
	cmd.AddCommand(newTypeCommand(a, o))
	cmd.AddCommand(newServiceCommand(a, o))

	return []*cobra.Command{cmd}
}

// volumeShowFields is the curated Field/Value view for a single volume, matching
// the most operationally useful attributes of `openstack volume show`.
func volumeShowFields(v *volumes.Volume) ([]string, []any) {
	fields := []string{
		"id", "name", "description", "status", "size", "volume_type",
		"bootable", "availability_zone", "attachments", "snapshot_id",
		"source_volid", "encrypted", "multiattach", "metadata",
		"user_id", "created_at", "updated_at",
	}
	values := []any{
		v.ID, v.Name, v.Description, v.Status, v.Size, v.VolumeType,
		v.Bootable, v.AvailabilityZone, v.Attachments, v.SnapshotID,
		v.SourceVolID, v.Encrypted, v.Multiattach, v.Metadata,
		v.UserID, v.CreatedAt, v.UpdatedAt,
	}
	return fields, values
}

// volumeListFlags holds the filters accepted by "volume list".
//
// Flag names follow upstream OSC (`openstack volume list`). The KeyStack command
// reference at https://docs.keystack.ru/ was not reachable at implementation
// time (HTTP 403), so these are UNVERIFIED against KeyStack and fall back to
// upstream OSC semantics — see the PR description.
type volumeListFlags struct {
	allProjects bool
	long        bool
	name        string
	status      string
	volumeType  string
	limit       int
	marker      string
}

func newVolumeListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &volumeListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List volumes",
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
			return runVolumeList(ctx, client, o, f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&f.allProjects, "all-projects", false, "list volumes from all projects (admin)")
	fl.BoolVar(&f.long, "long", false, "list additional fields in output")
	fl.StringVar(&f.name, "name", "", "filter by volume name")
	fl.StringVar(&f.status, "status", "", "filter by volume status")
	fl.StringVar(&f.volumeType, "type", "", "filter by volume type (client-side)")
	fl.IntVar(&f.limit, "limit", 0, "maximum number of volumes to return")
	fl.StringVar(&f.marker, "marker", "", "list volumes after this ID (pagination)")
	return cmd
}

func runVolumeList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *volumeListFlags, w io.Writer) error {
	opts := volumes.ListOpts{
		AllTenants: f.allProjects,
		Name:       f.name,
		Status:     f.status,
		Limit:      f.limit,
		Marker:     f.marker,
	}
	pages, err := volumes.List(client, opts).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing volumes: %w", err)
	}
	all, err := volumes.ExtractVolumes(pages)
	if err != nil {
		return fmt.Errorf("parsing volume list: %w", err)
	}
	// Cinder's volume list has no volume_type query param (and upstream OSC has no
	// --type on list), so filter by type client-side after extraction.
	if f.volumeType != "" {
		filtered := all[:0]
		for _, v := range all {
			if v.VolumeType == f.volumeType {
				filtered = append(filtered, v)
			}
		}
		all = filtered
	}
	// Limit is only the page size to cinder; enforce it as a hard result cap.
	if f.limit > 0 && len(all) > f.limit {
		all = all[:f.limit]
	}
	return o.WriteList(w, volumeListTable(all, f.long))
}

func volumeListTable(list []volumes.Volume, long bool) output.Table {
	cols := []string{"ID", "Name", "Status", "Size", "Attached to"}
	if long {
		cols = append(cols, "Type", "Bootable", "Availability Zone")
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(list))}
	for _, v := range list {
		row := []any{v.ID, v.Name, v.Status, v.Size, attachedTo(v)}
		if long {
			row = append(row, v.VolumeType, v.Bootable, v.AvailabilityZone)
		}
		t.Rows = append(t.Rows, row)
	}
	return t
}

// attachedTo renders the servers a volume is attached to in a compact form.
func attachedTo(v volumes.Volume) string {
	if len(v.Attachments) == 0 {
		return ""
	}
	parts := make([]string, 0, len(v.Attachments))
	for _, a := range v.Attachments {
		if a.Device != "" {
			parts = append(parts, fmt.Sprintf("%s on %s", a.ServerID, a.Device))
		} else {
			parts = append(parts, a.ServerID)
		}
	}
	out := parts[0]
	for _, p := range parts[1:] {
		out += ", " + p
	}
	return out
}

func newVolumeShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <volume>",
		Short: "Show volume details",
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
			return runVolumeShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runVolumeShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, w io.Writer) error {
	id, err := resolveVolumeID(ctx, client, ref)
	if err != nil {
		return err
	}
	v, err := volumes.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("getting volume %q: %w", ref, err)
	}
	fields, values := volumeShowFields(v)
	return o.WriteSingle(w, fields, values)
}

// volumeCreateFlags holds the options accepted by "volume create".
type volumeCreateFlags struct {
	size             int
	description      string
	volumeType       string
	image            string
	snapshot         string
	source           string
	availabilityZone string
	property         []string
	bootable         bool
	nonBootable      bool
}

func newVolumeCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &volumeCreateFlags{}
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new volume",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			if f.bootable && f.nonBootable {
				return fmt.Errorf("--bootable and --non-bootable are mutually exclusive")
			}
			ctx := cmd.Context()
			client, session, err := newVolumeSession(ctx, a)
			if err != nil {
				return err
			}
			// Resolve a --image name to an ID via glance before creating.
			if f.image != "" && !resolve.IsUUID(f.image) {
				img, err := session.Image()
				if err != nil {
					return err
				}
				id, err := resolve.ImageID(ctx, img, f.image)
				if err != nil {
					return err
				}
				f.image = id
			}
			// Resolve a --snapshot name to an ID via cinder before creating.
			if f.snapshot != "" && !resolve.IsUUID(f.snapshot) {
				id, err := resolveSnapshotID(ctx, client, f.snapshot)
				if err != nil {
					return err
				}
				f.snapshot = id
			}
			return runVolumeCreate(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.IntVar(&f.size, "size", 0, "volume size in GiB (required)")
	fl.StringVar(&f.description, "description", "", "volume description")
	fl.StringVar(&f.volumeType, "type", "", "volume type")
	fl.StringVar(&f.image, "image", "", "source image (ID or name) to create a bootable volume from")
	fl.StringVar(&f.snapshot, "snapshot", "", "source snapshot (ID or name)")
	fl.StringVar(&f.source, "source", "", "source volume (ID or name) to clone from")
	fl.StringVar(&f.availabilityZone, "availability-zone", "", "availability zone")
	fl.StringArrayVar(&f.property, "property", nil, "set a property key=value (repeatable)")
	fl.BoolVar(&f.bootable, "bootable", false, "mark the created volume as bootable")
	fl.BoolVar(&f.nonBootable, "non-bootable", false, "mark the created volume as non-bootable")
	return cmd
}

func runVolumeCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, name string, f *volumeCreateFlags, w io.Writer) error {
	// Cinder derives the size from the source snapshot/volume, so --size is only
	// required for image-from and blank creates.
	if f.snapshot == "" && f.source == "" && f.size <= 0 {
		return fmt.Errorf("--size must be a positive number of GiB")
	}
	meta, err := parseKeyValMap(f.property)
	if err != nil {
		return fmt.Errorf("parsing --property: %w", err)
	}
	// Resolve a --source clone reference (ID or name) to a volume ID.
	var sourceVolID string
	if f.source != "" {
		sourceVolID, err = resolveVolumeID(ctx, client, f.source)
		if err != nil {
			return err
		}
	}
	opts := volumes.CreateOpts{
		Name:             name,
		Size:             f.size,
		Description:      f.description,
		VolumeType:       f.volumeType,
		ImageID:          f.image,
		SnapshotID:       f.snapshot,
		SourceVolID:      sourceVolID,
		AvailabilityZone: f.availabilityZone,
		Metadata:         meta,
	}
	v, err := volumes.Create(ctx, client, opts, nil).Extract()
	if err != nil {
		return fmt.Errorf("creating volume: %w", err)
	}
	// CreateOpts has no bootable field, so apply the requested bootable flag as a
	// follow-up cinder volume action once the volume exists.
	if f.bootable || f.nonBootable {
		if err := setVolumeBootable(ctx, client, v.ID, f.bootable); err != nil {
			return fmt.Errorf("setting bootable flag on volume %q: %w", v.ID, err)
		}
		v, err = volumes.Get(ctx, client, v.ID).Extract()
		if err != nil {
			return fmt.Errorf("getting volume %q: %w", v.ID, err)
		}
	}
	fields, values := volumeShowFields(v)
	return o.WriteSingle(w, fields, values)
}

// setVolumeBootable issues the cinder "os-set_bootable" volume action. The
// gophercloud volumes package has no typed verb for it, so we POST the raw
// action body ourselves; replace this with a typed call if one is added.
func setVolumeBootable(ctx context.Context, client *gophercloud.ServiceClient, id string, bootable bool) error {
	body := map[string]any{"os-set_bootable": map[string]any{"bootable": bootable}}
	url := client.ServiceURL("volumes", id, "action")
	resp, err := client.Post(ctx, url, body, nil, &gophercloud.RequestOpts{OkCodes: []int{200, 202}})
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	_, _, err = gophercloud.ParseResponse(resp, err)
	return err
}

func newVolumeDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <volume>...",
		Short: "Delete one or more volumes",
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
			return runVolumeDelete(ctx, client, args, cmd.OutOrStdout())
		},
	}
}

func runVolumeDelete(ctx context.Context, client *gophercloud.ServiceClient, refs []string, w io.Writer) error {
	var errs []error
	for _, ref := range refs {
		id, err := resolveVolumeID(ctx, client, ref)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if err := volumes.Delete(ctx, client, id, nil).ExtractErr(); err != nil {
			errs = append(errs, fmt.Errorf("deleting volume %q: %w", ref, err))
			continue
		}
		if _, err := fmt.Fprintf(w, "Deleted volume: %s\n", ref); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// volumeSetFlags holds the mutations accepted by "volume set".
type volumeSetFlags struct {
	name        string
	description string
	property    []string
	size        int
}

func newVolumeSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &volumeSetFlags{}
	cmd := &cobra.Command{
		Use:   "set <volume>",
		Short: "Update properties of a volume",
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
			return runVolumeSet(ctx, client, args[0], f, cmd)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "new volume name")
	fl.StringVar(&f.description, "description", "", "new volume description")
	fl.StringArrayVar(&f.property, "property", nil, "set a property key=value (repeatable)")
	fl.IntVar(&f.size, "size", 0, "extend the volume to this size in GiB")
	return cmd
}

func runVolumeSet(ctx context.Context, client *gophercloud.ServiceClient, ref string, f *volumeSetFlags, cmd *cobra.Command) error {
	if !cmd.Flags().Changed("name") && !cmd.Flags().Changed("description") &&
		!cmd.Flags().Changed("size") && len(f.property) == 0 {
		return fmt.Errorf("nothing to set: specify at least one of --name, --description, --size, --property")
	}
	id, err := resolveVolumeID(ctx, client, ref)
	if err != nil {
		return err
	}

	// Extend first, if requested: it is a separate action from the metadata/name
	// update below.
	if cmd.Flags().Changed("size") {
		if err := volumes.ExtendSize(ctx, client, id, volumes.ExtendSizeOpts{NewSize: f.size}).ExtractErr(); err != nil {
			return fmt.Errorf("extending volume %q: %w", ref, err)
		}
	}

	update := volumes.UpdateOpts{}
	changed := false
	if cmd.Flags().Changed("name") {
		update.Name = &f.name
		changed = true
	}
	if cmd.Flags().Changed("description") {
		update.Description = &f.description
		changed = true
	}
	if len(f.property) > 0 {
		props, err := parseKeyValMap(f.property)
		if err != nil {
			return fmt.Errorf("parsing --property: %w", err)
		}
		// Cinder's volume update replaces metadata wholesale, so merge the new
		// properties onto the existing set to preserve unrelated keys.
		cur, err := volumes.Get(ctx, client, id).Extract()
		if err != nil {
			return fmt.Errorf("getting volume %q: %w", ref, err)
		}
		merged := map[string]string{}
		for k, v := range cur.Metadata {
			merged[k] = v
		}
		for k, v := range props {
			merged[k] = v
		}
		update.Metadata = merged
		changed = true
	}

	if changed {
		if _, err := volumes.Update(ctx, client, id, update).Extract(); err != nil {
			return fmt.Errorf("updating volume %q: %w", ref, err)
		}
	}
	return nil
}

// volumeUnsetFlags holds the removals accepted by "volume unset".
type volumeUnsetFlags struct {
	property []string
}

func newVolumeUnsetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &volumeUnsetFlags{}
	cmd := &cobra.Command{
		Use:   "unset <volume>",
		Short: "Remove properties from a volume",
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
			return runVolumeUnset(ctx, client, args[0], f)
		},
	}
	cmd.Flags().StringArrayVar(&f.property, "property", nil, "remove a property by key (repeatable)")
	return cmd
}

func runVolumeUnset(ctx context.Context, client *gophercloud.ServiceClient, ref string, f *volumeUnsetFlags) error {
	if len(f.property) == 0 {
		return nil
	}
	id, err := resolveVolumeID(ctx, client, ref)
	if err != nil {
		return err
	}
	cur, err := volumes.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("getting volume %q: %w", ref, err)
	}
	merged := map[string]string{}
	for k, v := range cur.Metadata {
		merged[k] = v
	}
	for _, key := range f.property {
		delete(merged, key)
	}
	if _, err := volumes.Update(ctx, client, id, metadataUpdateOpts{metadata: merged}).Extract(); err != nil {
		return fmt.Errorf("updating volume %q: %w", ref, err)
	}
	return nil
}

// metadataUpdateOpts is a volumes.UpdateOptsBuilder that always emits the
// "metadata" key in the PUT body — even when the map is empty. volumes.UpdateOpts
// tags Metadata with `json:",omitempty"`, so unsetting the last key would drop
// the field entirely and cinder (which replaces metadata wholesale) would keep
// the old values. Sending "metadata":{} clears them as intended.
type metadataUpdateOpts struct {
	metadata map[string]string
}

func (o metadataUpdateOpts) ToVolumeUpdateMap() (map[string]any, error) {
	m := map[string]string{}
	for k, v := range o.metadata {
		m[k] = v
	}
	return map[string]any{"volume": map[string]any{"metadata": m}}, nil
}

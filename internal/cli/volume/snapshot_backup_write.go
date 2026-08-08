package volume

import (
	"context"
	"fmt"
	"maps"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/backups"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/snapshots"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// The set/unset verbs of "volume snapshot" and "volume backup".
//
// Flag names follow upstream OSC (`openstack volume snapshot set|unset`,
// `openstack volume backup set|unset`). UNVERIFIED against KeyStack docs
// (https://docs.keystack.ru/ returned HTTP 403 at implementation time); falls
// back to upstream OSC semantics.
//
// Properties are cinder *metadata*, and its metadata endpoint is a PUT that
// replaces the whole map. So both set and unset read the current metadata,
// apply the change, and write back the merged result — the API offers no way to
// touch a single key.

// --- volume snapshot set/unset ----------------------------------------------

type snapshotSetFlags struct {
	name        string
	description string
	property    []string
	noProperty  bool
	state       string
}

func newSnapshotSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &snapshotSetFlags{}
	cmd := &cobra.Command{
		Use:   "set <snapshot>",
		Short: "Update properties of a volume snapshot",
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
			fl := cmd.Flags()
			return runSnapshotSet(ctx, client, args[0], f, fl.Changed("name"), fl.Changed("description"))
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "new snapshot name")
	fl.StringVar(&f.description, "description", "", "new snapshot description")
	fl.StringArrayVar(&f.property, "property", nil, "set a property key=value (repeatable)")
	fl.BoolVar(&f.noProperty, "no-property", false, "clear all properties before applying --property")
	fl.StringVar(&f.state, "state", "",
		"reset the snapshot status (admin only; changes only cinder's record, not the backend)")
	return cmd
}

func runSnapshotSet(ctx context.Context, client *gophercloud.ServiceClient, ref string,
	f *snapshotSetFlags, nameSet, descSet bool,
) error {
	id, err := resolveSnapshotID(ctx, client, ref)
	if err != nil {
		return err
	}
	if nameSet || descSet {
		opts := snapshots.UpdateOpts{}
		if nameSet {
			opts.Name = &f.name
		}
		if descSet {
			opts.Description = &f.description
		}
		if _, err := snapshots.Update(ctx, client, id, opts).Extract(); err != nil {
			return fmt.Errorf("updating snapshot %q: %w", ref, err)
		}
	}
	if len(f.property) > 0 || f.noProperty {
		add, perr := parseProperties(f.property)
		if perr != nil {
			return perr
		}
		merged, merr := mergedSnapshotMetadata(ctx, client, id, add, nil, f.noProperty)
		if merr != nil {
			return merr
		}
		if _, err := snapshots.UpdateMetadata(ctx, client, id, snapshots.UpdateMetadataOpts{Metadata: merged}).Extract(); err != nil {
			return fmt.Errorf("updating properties of snapshot %q: %w", ref, err)
		}
	}
	if f.state != "" {
		if err := snapshots.ResetStatus(ctx, client, id, snapshots.ResetStatusOpts{Status: f.state}).ExtractErr(); err != nil {
			return fmt.Errorf("resetting the status of snapshot %q: %w", ref, err)
		}
	}
	return nil
}

func newSnapshotUnsetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var property []string
	cmd := &cobra.Command{
		Use:   "unset <snapshot>",
		Short: "Remove properties from a volume snapshot",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			if len(property) == 0 {
				return fmt.Errorf("volume snapshot unset requires at least one --property")
			}
			ctx := cmd.Context()
			client, err := newVolumeClient(ctx, a)
			if err != nil {
				return err
			}
			return runSnapshotUnset(ctx, client, args[0], property)
		},
	}
	cmd.Flags().StringArrayVar(&property, "property", nil, "property key to remove (repeatable)")
	return cmd
}

func runSnapshotUnset(ctx context.Context, client *gophercloud.ServiceClient, ref string, keys []string) error {
	id, err := resolveSnapshotID(ctx, client, ref)
	if err != nil {
		return err
	}
	merged, err := mergedSnapshotMetadata(ctx, client, id, nil, keys, false)
	if err != nil {
		return err
	}
	if _, err := snapshots.UpdateMetadata(ctx, client, id, snapshots.UpdateMetadataOpts{Metadata: merged}).Extract(); err != nil {
		return fmt.Errorf("removing properties from snapshot %q: %w", ref, err)
	}
	return nil
}

// mergedSnapshotMetadata computes the full metadata map to PUT back: the
// current map (unless clearing), plus add, minus remove.
func mergedSnapshotMetadata(ctx context.Context, client *gophercloud.ServiceClient, id string,
	add map[string]string, remove []string, clearAll bool,
) (map[string]any, error) {
	merged := map[string]any{}
	if !clearAll {
		current, err := snapshots.Get(ctx, client, id).Extract()
		if err != nil {
			return nil, fmt.Errorf("reading snapshot %s before updating its properties: %w", id, err)
		}
		for k, v := range current.Metadata {
			merged[k] = v
		}
	}
	for _, k := range remove {
		delete(merged, k)
	}
	for k, v := range add {
		merged[k] = v
	}
	return merged, nil
}

// --- volume backup set/unset ------------------------------------------------

// backupUpdateOpts wraps gophercloud's UpdateOpts in the "backup" object cinder
// requires.
//
// At v2.13.0 backups.UpdateOpts.ToBackupUpdateMap calls BuildRequestBody(opts,
// "") — with an empty parent — so the body goes out as {"name": ...} rather than
// {"backup": {"name": ...}}. cinder/api/v3/backups.py does `body['backup']`
// unconditionally, so a real cloud answers 400 and every `volume backup set`
// would fail. The wrap is skipped if the key is already present, so this becomes
// a no-op the moment gophercloud is fixed upstream.
type backupUpdateOpts struct {
	backups.UpdateOpts
}

func (o backupUpdateOpts) ToBackupUpdateMap() (map[string]any, error) {
	body, err := o.UpdateOpts.ToBackupUpdateMap()
	if err != nil {
		return nil, err
	}
	return o.wrap(body)
}

func (backupUpdateOpts) wrap(body map[string]any) (map[string]any, error) {
	if _, wrapped := body["backup"]; wrapped {
		return body, nil
	}
	return map[string]any{"backup": body}, nil
}

type backupSetFlags struct {
	name        string
	description string
	property    []string
	noProperty  bool
	state       string
}

func newBackupSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &backupSetFlags{}
	cmd := &cobra.Command{
		Use:   "set <backup>",
		Short: "Update properties of a volume backup",
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
			fl := cmd.Flags()
			return runBackupSet(ctx, client, args[0], f, fl.Changed("name"), fl.Changed("description"))
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "new backup name")
	fl.StringVar(&f.description, "description", "", "new backup description")
	fl.StringArrayVar(&f.property, "property", nil, "set a property key=value (repeatable; needs cinder 3.43)")
	fl.BoolVar(&f.noProperty, "no-property", false, "clear all properties before applying --property")
	fl.StringVar(&f.state, "state", "",
		"reset the backup status (admin only; changes only cinder's record, not the backend)")
	return cmd
}

// runBackupSet updates the backup. Unlike snapshots, cinder folds backup
// metadata into the same PATCH as name and description (from microversion
// 3.43), so there is one request rather than two — but the map still replaces
// wholesale, so --property merges against the current value.
func runBackupSet(ctx context.Context, client *gophercloud.ServiceClient, ref string,
	f *backupSetFlags, nameSet, descSet bool,
) error {
	id, err := resolveBackupID(ctx, client, ref)
	if err != nil {
		return err
	}
	opts := backups.UpdateOpts{}
	if nameSet {
		opts.Name = &f.name
	}
	if descSet {
		opts.Description = &f.description
	}
	if len(f.property) > 0 || f.noProperty {
		add, perr := parseProperties(f.property)
		if perr != nil {
			return perr
		}
		merged, merr := mergedBackupMetadata(ctx, client, id, add, nil, f.noProperty)
		if merr != nil {
			return merr
		}
		opts.Metadata = merged
	}
	if nameSet || descSet || len(f.property) > 0 || f.noProperty {
		if _, err := backups.Update(ctx, client, id, backupUpdateOpts{opts}).Extract(); err != nil {
			return fmt.Errorf("updating backup %q: %w", ref, err)
		}
	}
	if f.state != "" {
		if err := backups.ResetStatus(ctx, client, id, backups.ResetStatusOpts{Status: f.state}).ExtractErr(); err != nil {
			return fmt.Errorf("resetting the status of backup %q: %w", ref, err)
		}
	}
	return nil
}

func newBackupUnsetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var property []string
	cmd := &cobra.Command{
		Use:   "unset <backup>",
		Short: "Remove properties from a volume backup",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			if len(property) == 0 {
				return fmt.Errorf("volume backup unset requires at least one --property")
			}
			ctx := cmd.Context()
			client, err := newVolumeClient(ctx, a)
			if err != nil {
				return err
			}
			return runBackupUnset(ctx, client, args[0], property)
		},
	}
	cmd.Flags().StringArrayVar(&property, "property", nil, "property key to remove (repeatable; needs cinder 3.43)")
	return cmd
}

func runBackupUnset(ctx context.Context, client *gophercloud.ServiceClient, ref string, keys []string) error {
	id, err := resolveBackupID(ctx, client, ref)
	if err != nil {
		return err
	}
	merged, err := mergedBackupMetadata(ctx, client, id, nil, keys, false)
	if err != nil {
		return err
	}
	opts := backupUpdateOpts{backups.UpdateOpts{Metadata: merged}}
	if _, err := backups.Update(ctx, client, id, opts).Extract(); err != nil {
		return fmt.Errorf("removing properties from backup %q: %w", ref, err)
	}
	return nil
}

func mergedBackupMetadata(ctx context.Context, client *gophercloud.ServiceClient, id string,
	add map[string]string, remove []string, clearAll bool,
) (map[string]string, error) {
	merged := map[string]string{}
	if !clearAll {
		current, err := backups.Get(ctx, client, id).Extract()
		if err != nil {
			return nil, fmt.Errorf("reading backup %s before updating its properties: %w", id, err)
		}
		// gophercloud models the backup's metadata as *map[string]string, so a
		// backup with none at all comes back as a nil pointer rather than an
		// empty map.
		if current.Metadata != nil {
			maps.Copy(merged, *current.Metadata)
		}
	}
	for _, k := range remove {
		delete(merged, k)
	}
	maps.Copy(merged, add)
	return merged, nil
}

// parseProperties turns repeated key=value flag values into a map. Only the
// first '=' separates, so a value may itself contain one.
func parseProperties(pairs []string) (map[string]string, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	m := make(map[string]string, len(pairs))
	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("expected key=value, got %q", p)
		}
		m[strings.TrimSpace(k)] = v
	}
	return m, nil
}

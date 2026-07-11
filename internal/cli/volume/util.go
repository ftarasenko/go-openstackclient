package volume

import (
	"context"
	"fmt"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/backups"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/snapshots"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/volumes"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/volumetypes"
)

// parseKeyVal splits a "key=value" string into its two halves. The value may
// itself contain '=' signs; only the first is treated as the separator.
func parseKeyVal(s string) (string, string, error) {
	i := strings.Index(s, "=")
	if i < 0 {
		return "", "", fmt.Errorf("expected key=value, got %q", s)
	}
	key := strings.TrimSpace(s[:i])
	if key == "" {
		return "", "", fmt.Errorf("empty key in %q", s)
	}
	return key, s[i+1:], nil
}

// parseKeyValMap turns a slice of "key=value" flag values into a string map,
// suitable for cinder metadata / extra-spec bodies.
func parseKeyValMap(pairs []string) (map[string]string, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	m := make(map[string]string, len(pairs))
	for _, p := range pairs {
		k, v, err := parseKeyVal(p)
		if err != nil {
			return nil, err
		}
		m[k] = v
	}
	return m, nil
}

// oneID enforces exactly-one-result semantics for a name lookup. ids holds the
// IDs of every resource the server-side name filter returned.
func oneID(kind, ref string, ids []string) (string, error) {
	switch len(ids) {
	case 1:
		return ids[0], nil
	case 0:
		return "", fmt.Errorf("no %s found matching %q", kind, ref)
	default:
		return "", fmt.Errorf("multiple %ss match %q; use an ID", kind, ref)
	}
}

// resolveVolumeID turns a user-supplied volume reference (ID or name) into a
// volume ID. It tries a direct GET first (the common case: an ID), then falls
// back to a name lookup, mirroring `openstack volume ...` which accepts either.
func resolveVolumeID(ctx context.Context, client *gophercloud.ServiceClient, ref string) (string, error) {
	if v, err := volumes.Get(ctx, client, ref).Extract(); err == nil {
		return v.ID, nil
	}
	pages, err := volumes.List(client, volumes.ListOpts{Name: ref}).AllPages(ctx)
	if err != nil {
		return "", fmt.Errorf("looking up volume %q: %w", ref, err)
	}
	all, err := volumes.ExtractVolumes(pages)
	if err != nil {
		return "", fmt.Errorf("parsing volume list: %w", err)
	}
	ids := make([]string, 0, len(all))
	for _, v := range all {
		if v.Name == ref {
			ids = append(ids, v.ID)
		}
	}
	return oneID("volume", ref, ids)
}

// resolveSnapshotID resolves a snapshot ID or name to an ID.
func resolveSnapshotID(ctx context.Context, client *gophercloud.ServiceClient, ref string) (string, error) {
	if s, err := snapshots.Get(ctx, client, ref).Extract(); err == nil {
		return s.ID, nil
	}
	pages, err := snapshots.List(client, snapshots.ListOpts{Name: ref}).AllPages(ctx)
	if err != nil {
		return "", fmt.Errorf("looking up snapshot %q: %w", ref, err)
	}
	all, err := snapshots.ExtractSnapshots(pages)
	if err != nil {
		return "", fmt.Errorf("parsing snapshot list: %w", err)
	}
	ids := make([]string, 0, len(all))
	for _, s := range all {
		if s.Name == ref {
			ids = append(ids, s.ID)
		}
	}
	return oneID("snapshot", ref, ids)
}

// resolveBackupID resolves a backup ID or name to an ID.
func resolveBackupID(ctx context.Context, client *gophercloud.ServiceClient, ref string) (string, error) {
	if b, err := backups.Get(ctx, client, ref).Extract(); err == nil {
		return b.ID, nil
	}
	pages, err := backups.List(client, backups.ListOpts{Name: ref}).AllPages(ctx)
	if err != nil {
		return "", fmt.Errorf("looking up backup %q: %w", ref, err)
	}
	all, err := backups.ExtractBackups(pages)
	if err != nil {
		return "", fmt.Errorf("parsing backup list: %w", err)
	}
	ids := make([]string, 0, len(all))
	for _, b := range all {
		if b.Name == ref {
			ids = append(ids, b.ID)
		}
	}
	return oneID("backup", ref, ids)
}

// resolveVolumeTypeID resolves a volume-type ID or name to an ID.
func resolveVolumeTypeID(ctx context.Context, client *gophercloud.ServiceClient, ref string) (string, error) {
	if t, err := volumetypes.Get(ctx, client, ref).Extract(); err == nil {
		return t.ID, nil
	}
	pages, err := volumetypes.List(client, volumetypes.ListOpts{Name: ref}).AllPages(ctx)
	if err != nil {
		return "", fmt.Errorf("looking up volume type %q: %w", ref, err)
	}
	all, err := volumetypes.ExtractVolumeTypes(pages)
	if err != nil {
		return "", fmt.Errorf("parsing volume type list: %w", err)
	}
	ids := make([]string, 0, len(all))
	for _, t := range all {
		if t.Name == ref {
			ids = append(ids, t.ID)
		}
	}
	return oneID("volume type", ref, ids)
}

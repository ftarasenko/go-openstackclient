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
	return resolveByNameOrGet("volume", ref,
		func() (string, bool) {
			if v, err := volumes.Get(ctx, client, ref).Extract(); err == nil {
				return v.ID, true
			}
			return "", false
		},
		func() ([]volumes.Volume, error) {
			pages, err := volumes.List(client, volumes.ListOpts{Name: ref}).AllPages(ctx)
			if err != nil {
				return nil, err
			}
			return volumes.ExtractVolumes(pages)
		},
		func(v volumes.Volume) string { return v.Name },
		func(v volumes.Volume) string { return v.ID },
	)
}

// resolveSnapshotID resolves a snapshot ID or name to an ID.
func resolveSnapshotID(ctx context.Context, client *gophercloud.ServiceClient, ref string) (string, error) {
	return resolveByNameOrGet("snapshot", ref,
		func() (string, bool) {
			if s, err := snapshots.Get(ctx, client, ref).Extract(); err == nil {
				return s.ID, true
			}
			return "", false
		},
		func() ([]snapshots.Snapshot, error) {
			pages, err := snapshots.List(client, snapshots.ListOpts{Name: ref}).AllPages(ctx)
			if err != nil {
				return nil, err
			}
			return snapshots.ExtractSnapshots(pages)
		},
		func(s snapshots.Snapshot) string { return s.Name },
		func(s snapshots.Snapshot) string { return s.ID },
	)
}

// resolveBackupID resolves a backup ID or name to an ID.
func resolveBackupID(ctx context.Context, client *gophercloud.ServiceClient, ref string) (string, error) {
	return resolveByNameOrGet("backup", ref,
		func() (string, bool) {
			if b, err := backups.Get(ctx, client, ref).Extract(); err == nil {
				return b.ID, true
			}
			return "", false
		},
		func() ([]backups.Backup, error) {
			pages, err := backups.List(client, backups.ListOpts{Name: ref}).AllPages(ctx)
			if err != nil {
				return nil, err
			}
			return backups.ExtractBackups(pages)
		},
		func(b backups.Backup) string { return b.Name },
		func(b backups.Backup) string { return b.ID },
	)
}

// resolveVolumeTypeID resolves a volume-type ID or name to an ID.
func resolveVolumeTypeID(ctx context.Context, client *gophercloud.ServiceClient, ref string) (string, error) {
	return resolveByNameOrGet("volume type", ref,
		func() (string, bool) {
			if t, err := volumetypes.Get(ctx, client, ref).Extract(); err == nil {
				return t.ID, true
			}
			return "", false
		},
		func() ([]volumetypes.VolumeType, error) {
			pages, err := volumetypes.List(client, volumetypes.ListOpts{Name: ref}).AllPages(ctx)
			if err != nil {
				return nil, err
			}
			return volumetypes.ExtractVolumeTypes(pages)
		},
		func(t volumetypes.VolumeType) string { return t.Name },
		func(t volumetypes.VolumeType) string { return t.ID },
	)
}

// resolveByNameOrGet backs the cinder name-or-ID resolvers: a direct GET (the
// common case where ref is already an ID) wins; otherwise a name-filtered list
// is matched exactly and oneID enforces exactly-one-result.
func resolveByNameOrGet[T any](kind, ref string,
	get func() (string, bool), list func() ([]T, error),
	nameOf func(T) string, idOf func(T) string,
) (string, error) {
	if id, ok := get(); ok {
		return id, nil
	}
	all, err := list()
	if err != nil {
		return "", fmt.Errorf("looking up %s %q: %w", kind, ref, err)
	}
	ids := make([]string, 0, len(all))
	for _, t := range all {
		if nameOf(t) == ref {
			ids = append(ids, idOf(t))
		}
	}
	return oneID(kind, ref, ids)
}

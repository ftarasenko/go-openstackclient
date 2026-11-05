package volume

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/snapshots"

	"github.com/ftarasenko/go-openstackclient/pkg/openstack/paging"
)

// ListSnapshots lists the snapshots opts selects, up to limit (0: all).
func ListSnapshots(ctx context.Context, c *gophercloud.ServiceClient, opts snapshots.ListOptsBuilder, limit int) ([]snapshots.Snapshot, error) {
	all, err := paging.Collect(ctx, snapshots.List(c, opts), limit, snapshots.ExtractSnapshots)
	if err != nil {
		return nil, wrap(err, "listing snapshots")
	}
	return all, nil
}

// GetSnapshot returns the snapshot with the given ID.
func GetSnapshot(ctx context.Context, c *gophercloud.ServiceClient, id string) (*snapshots.Snapshot, error) {
	s, err := snapshots.Get(ctx, c, id).Extract()
	return s, wrap(err, "getting snapshot %s", id)
}

// CreateSnapshot snapshots a volume. Cinder answers with the snapshot still
// creating.
func CreateSnapshot(ctx context.Context, c *gophercloud.ServiceClient, opts snapshots.CreateOptsBuilder) (*snapshots.Snapshot, error) {
	s, err := snapshots.Create(ctx, c, opts).Extract()
	return s, wrap(err, "creating snapshot")
}

// UpdateSnapshot applies opts (name, description) to the snapshot.
func UpdateSnapshot(ctx context.Context, c *gophercloud.ServiceClient, id string, opts snapshots.UpdateOptsBuilder) (*snapshots.Snapshot, error) {
	s, err := snapshots.Update(ctx, c, id, opts).Extract()
	return s, wrap(err, "updating snapshot %s", id)
}

// ReplaceSnapshotMetadata replaces the snapshot's metadata with metadata and
// returns what cinder stored. Cinder keeps no key that is left out.
// gophercloud omits an empty map from the body, which cinder rejects.
func ReplaceSnapshotMetadata(ctx context.Context, c *gophercloud.ServiceClient, id string, metadata map[string]any) (map[string]any, error) {
	var out struct {
		Metadata map[string]any `json:"metadata"`
	}
	err := snapshots.UpdateMetadata(ctx, c, id, snapshots.UpdateMetadataOpts{Metadata: metadata}).ExtractInto(&out)
	return out.Metadata, wrap(err, "updating properties of snapshot %s", id)
}

// ResetSnapshotStatus overwrites the status cinder records for the snapshot
// (admin), without touching the backend.
func ResetSnapshotStatus(ctx context.Context, c *gophercloud.ServiceClient, id, status string) error {
	return wrap(snapshots.ResetStatus(ctx, c, id, snapshots.ResetStatusOpts{Status: status}).ExtractErr(),
		"resetting the status of snapshot %s", id)
}

// DeleteSnapshot deletes the snapshot with the given ID.
func DeleteSnapshot(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(snapshots.Delete(ctx, c, id).ExtractErr(), "deleting snapshot %s", id)
}

package volume

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/backups"

	"github.com/ftarasenko/go-openstackclient/pkg/openstack/paging"
)

// ListBackups lists the backups opts selects, up to limit (0: all), from
// cinder's detail view. The summary view (backups.List) carries only IDs,
// names and links.
func ListBackups(ctx context.Context, c *gophercloud.ServiceClient, opts backups.ListOpts, limit int) ([]backups.Backup, error) {
	all, err := paging.Collect(ctx, backups.ListDetail(c, detailOpts{opts}), limit, backups.ExtractBackups)
	if err != nil {
		return nil, wrap(err, "listing backups")
	}
	return all, nil
}

// detailOpts sends ListOpts's filters to the detail listing, whose own opts
// type has no name, status or volume_id.
type detailOpts struct{ backups.ListOpts }

func (o detailOpts) ToBackupListDetailQuery() (string, error) { return o.ToBackupListQuery() }

// GetBackup returns the backup with the given ID.
func GetBackup(ctx context.Context, c *gophercloud.ServiceClient, id string) (*backups.Backup, error) {
	b, err := backups.Get(ctx, c, id).Extract()
	return b, wrap(err, "getting backup %s", id)
}

// CreateBackup backs up a volume. Cinder answers with the backup still
// creating.
func CreateBackup(ctx context.Context, c *gophercloud.ServiceClient, opts backups.CreateOptsBuilder) (*backups.Backup, error) {
	b, err := backups.Create(ctx, c, opts).Extract()
	return b, wrap(err, "creating backup")
}

// UpdateBackup applies opts to the backup. Metadata (microversion 3.43)
// replaces the backup's metadata wholesale.
//
// gophercloud sends UpdateOpts without the "backup" wrapper cinder requires;
// this adds it.
func UpdateBackup(ctx context.Context, c *gophercloud.ServiceClient, id string, opts backups.UpdateOpts) (*backups.Backup, error) {
	b, err := backups.Update(ctx, c, id, backupUpdateOpts{opts}).Extract()
	return b, wrap(err, "updating backup %s", id)
}

// backupUpdateOpts wraps the body in "backup" unless gophercloud already did.
type backupUpdateOpts struct{ backups.UpdateOpts }

func (o backupUpdateOpts) ToBackupUpdateMap() (map[string]any, error) {
	body, err := o.UpdateOpts.ToBackupUpdateMap()
	if err != nil {
		return nil, err
	}
	return wrapBackupBody(body), nil
}

func wrapBackupBody(body map[string]any) map[string]any {
	if _, wrapped := body["backup"]; wrapped {
		return body
	}
	return map[string]any{"backup": body}
}

// ResetBackupStatus overwrites the status cinder records for the backup
// (admin), without touching the backend.
func ResetBackupStatus(ctx context.Context, c *gophercloud.ServiceClient, id, status string) error {
	return wrap(backups.ResetStatus(ctx, c, id, backups.ResetStatusOpts{Status: status}).ExtractErr(),
		"resetting the status of backup %s", id)
}

// DeleteBackup deletes the backup with the given ID.
func DeleteBackup(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(backups.Delete(ctx, c, id).ExtractErr(), "deleting backup %s", id)
}

// RestoreBackup restores the backup into opts.VolumeID, or into a new volume
// when it is empty.
func RestoreBackup(ctx context.Context, c *gophercloud.ServiceClient, id string, opts backups.RestoreOpts) (*backups.Restore, error) {
	r, err := backups.RestoreFromBackup(ctx, c, id, opts).Extract()
	return r, wrap(err, "restoring backup %s", id)
}

package volume

import (
	"context"
	"errors"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/volumes"
)

// ExtendVolume grows the volume to size GiB. Cinder resizes asynchronously.
func ExtendVolume(ctx context.Context, c *gophercloud.ServiceClient, id string, size int) error {
	return wrap(volumes.ExtendSize(ctx, c, id, volumes.ExtendSizeOpts{NewSize: size}).ExtractErr(),
		"extending volume %s to %d GiB", id, size)
}

// ErrNothingToReset is what [ResetStatusOpts] returns when every field is empty.
var ErrNothingToReset = errors.New("nothing to reset: set Status or AttachStatus")

// ResetStatusOpts is the body of os-reset_status, sending only the fields that
// are set. volumes.ResetStatusOpts always sends "status", which cinder rejects
// when empty, so it cannot reset the attach status alone.
type ResetStatusOpts struct {
	Status       string
	AttachStatus string
}

// ToResetStatusMap implements volumes.ResetStatusOptsBuilder.
func (o ResetStatusOpts) ToResetStatusMap() (map[string]any, error) {
	action := map[string]any{}
	if o.Status != "" {
		action["status"] = o.Status
	}
	if o.AttachStatus != "" {
		action["attach_status"] = o.AttachStatus
	}
	if len(action) == 0 {
		return nil, ErrNothingToReset
	}
	return map[string]any{"os-reset_status": action}, nil
}

// ResetVolumeStatus overwrites the status cinder records for the volume
// (admin), without touching the backend.
func ResetVolumeStatus(ctx context.Context, c *gophercloud.ServiceClient, id string, opts volumes.ResetStatusOptsBuilder) error {
	return wrap(volumes.ResetStatus(ctx, c, id, opts).ExtractErr(), "resetting status of volume %s", id)
}

// RetypeVolume changes the volume's type. Cinder answers 202 and may still
// roll the change back; [WaitForRetype] tells which.
func RetypeVolume(ctx context.Context, c *gophercloud.ServiceClient, id string, opts volumes.ChangeTypeOptsBuilder) error {
	return wrap(volumes.ChangeType(ctx, c, id, opts).ExtractErr(), "retyping volume %s", id)
}

// SetVolumeBootable sets or clears the volume's bootable flag
// (os-set_bootable, a raw action: gophercloud has no typed call).
func SetVolumeBootable(ctx context.Context, c *gophercloud.ServiceClient, id string, bootable bool) error {
	body := map[string]any{"os-set_bootable": map[string]any{"bootable": bootable}}
	return wrap(volumeAction(ctx, c, id, body, 200, 202), "setting bootable flag on volume %s", id)
}

// MigrateOpts is the body of os-migrate_volume.
type MigrateOpts struct {
	// Host is the destination, as host@backend#pool.
	Host string
	// ForceHostCopy bypasses the driver's optimised copy.
	ForceHostCopy bool
	// LockVolume stops the migration from being aborted.
	LockVolume bool
}

// MigrateVolume moves the volume to another backend (admin; os-migrate_volume,
// a raw action: gophercloud has no typed call). [WaitForVolumeMigration]
// waits for it.
func MigrateVolume(ctx context.Context, c *gophercloud.ServiceClient, id string, opts MigrateOpts) error {
	body := map[string]any{"os-migrate_volume": map[string]any{
		"host":            opts.Host,
		"force_host_copy": opts.ForceHostCopy,
		"lock_volume":     opts.LockVolume,
	}}
	return wrap(volumeAction(ctx, c, id, body, 202), "migrating volume %s", id)
}

// volumeAction posts body to the volume's action endpoint.
func volumeAction(ctx context.Context, c *gophercloud.ServiceClient, id string, body map[string]any, ok ...int) error {
	resp, err := c.Post(ctx, c.ServiceURL("volumes", id, "action"), body, nil, &gophercloud.RequestOpts{OkCodes: ok})
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	_, _, err = gophercloud.ParseResponse(resp, err)
	return err
}

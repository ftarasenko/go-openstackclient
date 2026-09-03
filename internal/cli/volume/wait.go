package volume

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/volumes"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/volumetypes"
)

// Bounds for the --wait polling loops. A retype that has to migrate copies the
// whole volume, so the default cap is generous; --wait-timeout overrides it.
const (
	volumePollTimeout = 60 * time.Minute
	// maxVolumeGetErrors bounds how many consecutive Get failures a poll tolerates
	// before giving up; the counter resets on any success.
	maxVolumeGetErrors = 5
)

// volumePollInterval is a var so tests can shorten it; production never reassigns it.
var volumePollInterval = 5 * time.Second

// volumeWaitState is the subset of the volume body the --wait loops read.
//
// migration_status is only rendered for admins (cinder/api/v2/views/volumes.py
// gates it on ctxt.is_admin), so it is empty for an ordinary user and must never
// be the sole success signal. volume_type_id arrives from microversion 3.63 on.
type volumeWaitState struct {
	Status          string `json:"status"`
	VolumeType      string `json:"volume_type"`
	VolumeTypeID    string `json:"volume_type_id"`
	MigrationStatus string `json:"migration_status"`
}

// migrationInFlight reports whether migration_status is one of the transient
// values cinder sets while a migration runs (see cinder/volume/manager.py).
func migrationInFlight(s string) bool {
	switch strings.ToLower(s) {
	case "starting", "migrating", "completing":
		return true
	}
	return false
}

// pollVolume polls the volume until done reports it settled. done returns
// (finished, err): a non-nil error ends the wait as a failure. Transient Get
// failures are tolerated up to maxVolumeGetErrors.
func pollVolume(ctx context.Context, client *gophercloud.ServiceClient, ref, id string,
	timeout time.Duration, done func(volumeWaitState) (bool, error),
) error {
	if timeout <= 0 {
		timeout = volumePollTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(volumePollInterval)
	defer ticker.Stop()

	var getErrors int
	for {
		var st volumeWaitState
		if err := volumes.Get(ctx, client, id).ExtractInto(&st); err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("waiting for volume %q: %w", ref, ctx.Err())
			}
			getErrors++
			if getErrors > maxVolumeGetErrors {
				return fmt.Errorf("polling volume %q: %w", ref, err)
			}
		} else {
			getErrors = 0
			finished, err := done(st)
			if err != nil {
				return err
			}
			if finished {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for volume %q: %w", ref, ctx.Err())
		case <-ticker.C:
		}
	}
}

// newRetypeTarget looks up the resolved type's name so the wait can match cinder's
// name-based volume_type field and print something readable. A failed lookup is
// not fatal: matching falls back to the ID, which is always known.
func newRetypeTarget(ctx context.Context, client *gophercloud.ServiceClient, typeID string) retypeTarget {
	t := retypeTarget{id: typeID}
	if vt, err := volumetypes.Get(ctx, client, typeID).Extract(); err == nil {
		t.name = vt.Name
	}
	return t
}

// retypeTarget identifies the volume type a retype is expected to land on.
// Cinder renders "volume_type" as the type *name* (falling back to the ID when
// the type object is not loaded), so matching either response field against
// either identifier keeps the check correct across microversions.
type retypeTarget struct {
	id   string
	name string
}

func (t retypeTarget) matches(st volumeWaitState) bool {
	for _, got := range []string{st.VolumeType, st.VolumeTypeID} {
		if got == "" {
			continue
		}
		if got == t.id || (t.name != "" && got == t.name) {
			return true
		}
	}
	return false
}

// waitForRetype polls until a retype settles.
//
// Cinder restores the volume's previous status on both success and failure (it
// stashes previous_status before setting status=retyping), so status alone cannot
// distinguish the two. The authoritative signal is whether volume_type ended up
// as the requested type — which, unlike migration_status, every caller can see.
// Status stays "retyping" for the whole migration, including the nova
// swap_volume leg, so a settled status means cinder is done either way.
func waitForRetype(ctx context.Context, client *gophercloud.ServiceClient,
	ref, id string, target retypeTarget, timeout time.Duration, w io.Writer,
) error {
	err := pollVolume(ctx, client, ref, id, timeout, func(st volumeWaitState) (bool, error) {
		switch {
		case strings.EqualFold(st.Status, "error"):
			return false, fmt.Errorf("volume %q entered error status during retype", ref)
		case strings.EqualFold(st.Status, "retyping") || migrationInFlight(st.MigrationStatus):
			return false, nil
		case strings.EqualFold(st.MigrationStatus, "error"):
			return false, fmt.Errorf("retype of volume %q failed: cinder reported migration_status=error "+
				"and left it as type %q", ref, st.VolumeType)
		case !target.matches(st):
			// Cinder rolled the retype back; the API already returned 202, so this
			// is the only place the failure surfaces.
			return false, fmt.Errorf("retype of volume %q did not take effect: type is %q, wanted %q",
				ref, st.VolumeType, target.display())
		}
		return true, nil
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "Volume %s retyped to %s\n", ref, target.display())
	return err
}

// display names the target type for messages, preferring the human-readable name.
func (t retypeTarget) display() string {
	if t.name != "" {
		return t.name
	}
	return t.id
}

// waitForVolumeMigration polls until an os-migrate_volume settles.
//
// Unlike retype there is no user-visible attribute that flips on success (the
// host is admin-only too), so this keys off migration_status, which cinder drives
// to "success" or "error". os-migrate_volume is itself admin-only by default
// policy, so that field is visible to any caller allowed to start the migration.
func waitForVolumeMigration(ctx context.Context, client *gophercloud.ServiceClient,
	ref, id, host string, timeout time.Duration, w io.Writer,
) error {
	err := pollVolume(ctx, client, ref, id, timeout, func(st volumeWaitState) (bool, error) {
		switch {
		case strings.EqualFold(st.MigrationStatus, "success"):
			return true, nil
		case strings.EqualFold(st.MigrationStatus, "error"):
			return false, fmt.Errorf("migration of volume %q to host %q failed", ref, host)
		case strings.EqualFold(st.Status, "error"):
			return false, fmt.Errorf("volume %q entered error status during migration", ref)
		}
		// An empty migration_status means cinder has not registered the migration
		// yet (or the caller cannot see the field); keep waiting either way.
		return false, nil
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "Volume %s migrated to host %s\n", ref, host)
	return err
}

// waitForVolumeAvailable polls until a freshly created volume settles.
//
// Cinder answers a create with 202 and status "creating"; a volume built from an
// image passes through "downloading" on the way. Both are transient, and the
// only terminal states are "available" and "error" — so unlike the retype and
// migration waits above there is no attribute to cross-check, just the status.
func waitForVolumeAvailable(ctx context.Context, client *gophercloud.ServiceClient,
	ref, id string, timeout time.Duration,
) error {
	return pollVolume(ctx, client, ref, id, timeout, func(st volumeWaitState) (bool, error) {
		switch strings.ToLower(st.Status) {
		case "available":
			return true, nil
		case "error":
			return false, fmt.Errorf("volume %q entered error status while being created", ref)
		}
		return false, nil
	})
}

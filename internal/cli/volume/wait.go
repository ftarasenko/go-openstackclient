package volume

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/gophercloud/gophercloud/v2"

	volumeapi "github.com/ftarasenko/koc/pkg/openstack/volume"
)

// volumePollTimeout bounds a --wait. A retype that has to migrate copies the
// whole volume, so the default cap is generous; --wait-timeout overrides it.
const volumePollTimeout = 60 * time.Minute

// volumePollInterval is a var so tests can shorten it; production never reassigns it.
var volumePollInterval = 5 * time.Second

// waitContext bounds a --wait: timeout, or volumePollTimeout when it is not
// positive.
func waitContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		timeout = volumePollTimeout
	}
	return context.WithTimeout(ctx, timeout)
}

// newRetypeTarget looks up the resolved type's name so the wait can match cinder's
// name-based volume_type field and print something readable. A failed lookup is
// not fatal: matching falls back to the ID, which is always known.
func newRetypeTarget(ctx context.Context, client *gophercloud.ServiceClient, typeID string) retypeTarget {
	t := retypeTarget{id: typeID}
	if vt, err := volumeapi.GetVolumeType(ctx, client, typeID); err == nil {
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
	ctx, cancel := waitContext(ctx, timeout)
	defer cancel()
	err := volumeapi.WaitForRetype(ctx, client, id, target.id, target.name, volumePollInterval)
	if err = relabelWait(err, ref, func(f *volumeapi.WaitFailedError) error {
		switch f.Reason {
		case volumeapi.MigrationError:
			return fmt.Errorf("retype of volume %q failed: cinder reported migration_status=error "+
				"and left it as type %q", ref, f.State.VolumeType)
		case volumeapi.RetypeRolledBack:
			// Cinder rolled the retype back; the API already returned 202, so this
			// is the only place the failure surfaces.
			return fmt.Errorf("retype of volume %q did not take effect: type is %q, wanted %q",
				ref, f.State.VolumeType, target.display())
		default:
			return fmt.Errorf("volume %q entered error status during retype", ref)
		}
	}); err != nil {
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
	ctx, cancel := waitContext(ctx, timeout)
	defer cancel()
	err := volumeapi.WaitForVolumeMigration(ctx, client, id, volumePollInterval)
	if err = relabelWait(err, ref, func(f *volumeapi.WaitFailedError) error {
		if f.Reason == volumeapi.MigrationError {
			return fmt.Errorf("migration of volume %q to host %q failed", ref, host)
		}
		return fmt.Errorf("volume %q entered error status during migration", ref)
	}); err != nil {
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
	ctx, cancel := waitContext(ctx, timeout)
	defer cancel()
	return relabelWait(volumeapi.WaitForVolumeAvailable(ctx, client, id, volumePollInterval), ref,
		func(*volumeapi.WaitFailedError) error {
			return fmt.Errorf("volume %q entered error status while being created", ref)
		})
}

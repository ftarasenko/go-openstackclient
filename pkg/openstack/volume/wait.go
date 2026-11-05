package volume

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/volumes"
)

// State is the part of a volume the waits read.
//
// MigrationStatus is shown to admins only, so it is empty for anyone else.
// VolumeTypeID arrives from microversion 3.63; VolumeType is the type's name.
type State struct {
	Status          string `json:"status"`
	VolumeType      string `json:"volume_type"`
	VolumeTypeID    string `json:"volume_type_id"`
	MigrationStatus string `json:"migration_status"`
}

// WaitFailure says how a wait failed.
type WaitFailure int

const (
	// ErrorStatus means the volume's status became error.
	ErrorStatus WaitFailure = iota + 1
	// MigrationError means cinder set migration_status to error.
	MigrationError
	// RetypeRolledBack means the retype settled with the volume on another type.
	RetypeRolledBack
)

// WaitFailedError is what a wait returns when the volume reaches a state it
// will not leave for the one waited on.
type WaitFailedError struct {
	VolumeID string
	// Op is what was awaited: "create", "retype" or "migration".
	Op     string
	Reason WaitFailure
	// State is the volume as last read.
	State State
}

func (e *WaitFailedError) Error() string {
	switch e.Reason {
	case MigrationError:
		return fmt.Sprintf("%s of volume %s failed: migration_status is error, type %q", e.Op, e.VolumeID, e.State.VolumeType)
	case RetypeRolledBack:
		return fmt.Sprintf("retype of volume %s did not take effect: type is %q", e.VolumeID, e.State.VolumeType)
	default:
		return fmt.Sprintf("volume %s entered error status during %s", e.VolumeID, e.Op)
	}
}

// maxGetErrors is how many consecutive failed reads a wait rides out.
const maxGetErrors = 5

// WaitForVolumeAvailable polls a new volume every interval until it is
// available. It gives up when ctx is done, after more than five failed reads
// in a row, and with a [*WaitFailedError] on error.
func WaitForVolumeAvailable(ctx context.Context, c *gophercloud.ServiceClient, id string, interval time.Duration) error {
	return poll(ctx, c, id, interval, func(st State) (bool, error) {
		switch strings.ToLower(st.Status) {
		case "available":
			return true, nil
		case "error":
			return false, &WaitFailedError{VolumeID: id, Op: "create", Reason: ErrorStatus, State: st}
		}
		return false, nil
	})
}

// WaitForRetype polls a retyping volume every interval until the retype
// settles, and fails with [RetypeRolledBack] unless the volume ended on the
// type with ID typeID or name typeName (either may be empty).
//
// Cinder restores the previous status either way, so status alone cannot tell
// success from failure; the volume's type can, and every caller can read it.
// It gives up as [WaitForVolumeAvailable] does.
func WaitForRetype(ctx context.Context, c *gophercloud.ServiceClient, id, typeID, typeName string, interval time.Duration) error {
	return poll(ctx, c, id, interval, func(st State) (bool, error) {
		fail := func(r WaitFailure) error { return &WaitFailedError{VolumeID: id, Op: "retype", Reason: r, State: st} }
		switch {
		case strings.EqualFold(st.Status, "error"):
			return false, fail(ErrorStatus)
		case strings.EqualFold(st.Status, "retyping") || migrationInFlight(st.MigrationStatus):
			return false, nil
		case strings.EqualFold(st.MigrationStatus, "error"):
			return false, fail(MigrationError)
		case !typeMatches(st, typeID, typeName):
			return false, fail(RetypeRolledBack)
		}
		return true, nil
	})
}

// WaitForVolumeMigration polls a migrating volume every interval until
// migration_status is success. That field is admin-only, as is starting a
// migration. It gives up as [WaitForVolumeAvailable] does; a migration_status
// of error wins over an error status.
func WaitForVolumeMigration(ctx context.Context, c *gophercloud.ServiceClient, id string, interval time.Duration) error {
	return poll(ctx, c, id, interval, func(st State) (bool, error) {
		switch {
		case strings.EqualFold(st.MigrationStatus, "success"):
			return true, nil
		case strings.EqualFold(st.MigrationStatus, "error"):
			return false, &WaitFailedError{VolumeID: id, Op: "migration", Reason: MigrationError, State: st}
		case strings.EqualFold(st.Status, "error"):
			return false, &WaitFailedError{VolumeID: id, Op: "migration", Reason: ErrorStatus, State: st}
		}
		return false, nil
	})
}

// migrationInFlight reports a transient migration_status.
func migrationInFlight(s string) bool {
	switch strings.ToLower(s) {
	case "starting", "migrating", "completing":
		return true
	}
	return false
}

func typeMatches(st State, typeID, typeName string) bool {
	for _, got := range []string{st.VolumeType, st.VolumeTypeID} {
		if got == "" {
			continue
		}
		if got == typeID || (typeName != "" && got == typeName) {
			return true
		}
	}
	return false
}

// poll reads the volume every interval until settle reports done or an error.
func poll(ctx context.Context, c *gophercloud.ServiceClient, id string, interval time.Duration,
	settle func(State) (bool, error),
) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var getErrors int
	for {
		var st State
		if err := volumes.Get(ctx, c, id).ExtractInto(&st); err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("waiting for volume %s: %w", id, ctx.Err())
			}
			getErrors++
			if getErrors > maxGetErrors {
				return fmt.Errorf("polling volume %s: %w", id, err)
			}
		} else {
			getErrors = 0
			done, err := settle(st)
			if err != nil || done {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for volume %s: %w", id, ctx.Err())
		case <-ticker.C:
		}
	}
}

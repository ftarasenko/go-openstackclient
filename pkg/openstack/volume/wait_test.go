package volume_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ftarasenko/go-openstackclient/pkg/openstack/volume"
)

const typeID = "5f4e3d2c-1b0a-4f9e-8d7c-6b5a4f3e2d1c"

// The decision each wait makes on the reads it sees; "" is a failed read.
func TestWaits(t *testing.T) {
	type waitFn func(context.Context, *testing.T, []string) error
	avail := func(ctx context.Context, t *testing.T, reads []string) error {
		_, sc := newCinder(t, "3.70", map[string]route{"GET /volumes/" + volID: sequence(reads...)})
		return volume.WaitForVolumeAvailable(ctx, sc, volID, time.Millisecond)
	}
	retype := func(ctx context.Context, t *testing.T, reads []string) error {
		_, sc := newCinder(t, "3.70", map[string]route{"GET /volumes/" + volID: sequence(reads...)})
		return volume.WaitForRetype(ctx, sc, volID, typeID, "ssd", time.Millisecond)
	}
	migrate := func(ctx context.Context, t *testing.T, reads []string) error {
		_, sc := newCinder(t, "3.70", map[string]route{"GET /volumes/" + volID: sequence(reads...)})
		return volume.WaitForVolumeMigration(ctx, sc, volID, time.Millisecond)
	}
	st := func(fields string) string { return volBody(fields) }
	for _, tc := range []struct {
		name   string
		wait   waitFn
		reads  []string
		reason volume.WaitFailure
		errMsg string
	}{
		{"created", avail, []string{st(`,"status":"creating"`), "", st(`,"status":"downloading"`), st(`,"status":"available"`)}, 0, ""},
		{"create failed", avail, []string{st(`,"status":"creating"`), st(`,"status":"error"`)}, volume.ErrorStatus,
			"volume " + volID + " entered error status during create"},

		{"retyped by name", retype, []string{st(`,"status":"retyping"`), st(`,"status":"in-use","volume_type":"ssd"`)}, 0, ""},
		{"retyped by id", retype, []string{st(`,"status":"available","volume_type":"x","volume_type_id":"` + typeID + `"`)}, 0, ""},
		{"retype migrating", retype, []string{
			st(`,"status":"available","migration_status":"migrating","volume_type":"old"`),
			st(`,"status":"available","migration_status":"success","volume_type":"ssd"`),
		}, 0, ""},
		{"retype error status", retype, []string{st(`,"status":"error","volume_type":"ssd"`)}, volume.ErrorStatus,
			"volume " + volID + " entered error status during retype"},
		{"retype migration error", retype, []string{st(`,"status":"available","migration_status":"error","volume_type":"old"`)},
			volume.MigrationError, "retype of volume " + volID + ` failed: migration_status is error, type "old"`},
		{"retype rolled back", retype, []string{st(`,"status":"available","volume_type":"old"`)}, volume.RetypeRolledBack,
			"retype of volume " + volID + ` did not take effect: type is "old"`},

		{"migrated", migrate, []string{st(`,"status":"available"`), st(`,"status":"available","migration_status":"success"`)}, 0, ""},
		{"migration error wins", migrate, []string{st(`,"status":"error","migration_status":"error"`)}, volume.MigrationError, ""},
		{"migration error status", migrate, []string{st(`,"status":"error","migration_status":"migrating"`)}, volume.ErrorStatus,
			"volume " + volID + " entered error status during migration"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err := tc.wait(ctx, t, tc.reads)
			if tc.reason == 0 {
				if err != nil {
					t.Fatalf("err = %v; want success", err)
				}
				return
			}
			var wf *volume.WaitFailedError
			if !errors.As(err, &wf) || wf.Reason != tc.reason || wf.VolumeID != volID {
				t.Fatalf("err = %#v; want WaitFailedError reason %d", err, tc.reason)
			}
			if tc.errMsg != "" && err.Error() != tc.errMsg {
				t.Errorf("message = %q; want %q", err.Error(), tc.errMsg)
			}
		})
	}
}

func TestWait_RidesOutFiveFailedReads(t *testing.T) {
	ctx := context.Background()
	reads := []string{"", "", "", "", "", volBody(`,"status":"available"`)}
	_, sc := newCinder(t, "3.70", map[string]route{"GET /volumes/" + volID: sequence(reads...)})
	if err := volume.WaitForVolumeAvailable(ctx, sc, volID, time.Millisecond); err != nil {
		t.Fatalf("five failed reads: %v", err)
	}

	_, sc = newCinder(t, "3.70", map[string]route{"GET /volumes/" + volID: sequence("")})
	err := volume.WaitForVolumeAvailable(ctx, sc, volID, time.Millisecond)
	if err == nil || !strings.HasPrefix(err.Error(), "polling volume "+volID+": ") {
		t.Fatalf("err = %v; want a polling error", err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Error("a failed read reads as a timeout")
	}
}

func TestWait_Deadline(t *testing.T) {
	_, sc := newCinder(t, "3.70", map[string]route{"GET /volumes/" + volID: sequence(volBody(`,"status":"creating"`))})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := volume.WaitForVolumeAvailable(ctx, sc, volID, time.Hour)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.HasPrefix(err.Error(), "waiting for volume "+volID+": ") {
		t.Fatalf("err = %v; want a deadline", err)
	}
}

func TestWait_DeadlineDuringARead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	_, sc := newCinder(t, "3.70", map[string]route{"GET /volumes/" + volID: func(_ *http.Request) (int, string) {
		cancel()
		return 500, `{}`
	}})
	err := volume.WaitForVolumeAvailable(ctx, sc, volID, time.Hour)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v; want canceled", err)
	}
}

package volume

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	th "github.com/gophercloud/gophercloud/v2/testhelper"
)

// fastPoll shortens the --wait poll interval so tests that need more than one
// GET do not sleep for the production interval.
func fastPoll(t *testing.T) {
	t.Helper()
	prev := volumePollInterval
	volumePollInterval = time.Millisecond
	t.Cleanup(func() { volumePollInterval = prev })
}

// volumeBody renders a volume with the given status and type name, so a mock can
// walk a --wait poll through a status transition.
func volumeBody(id, status, volumeType, migrationStatus string) string {
	return fmt.Sprintf(`{"volume":{"id":%q,"name":"vol-a","status":%q,"size":10,
	  "volume_type":%q,"migration_status":%q,"bootable":"false","attachments":[],"metadata":{}}}`,
		id, status, volumeType, migrationStatus)
}

// retypeWaitServer wires the mock endpoints a "volume set --type --wait" run
// touches and serves the supplied volume bodies to successive GETs, so a test can
// script the status transition. The first GET is the name/ID resolver.
func retypeWaitServer(t *testing.T, fakeServer th.FakeServer, id, typeID string, bodies []string) *int {
	t.Helper()
	var gets int
	fakeServer.Mux.HandleFunc("/volumes/"+id+"/action", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	fakeServer.Mux.HandleFunc("/volumes/"+id, func(w http.ResponseWriter, _ *http.Request) {
		body := bodies[len(bodies)-1]
		if gets < len(bodies) {
			body = bodies[gets]
		}
		gets++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	})
	fakeServer.Mux.HandleFunc("/types/"+typeID, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(typeGetBody))
	})
	return &gets
}

const waitVolID = "11111111-1111-1111-1111-111111111111"
const waitTypeID = "t1111111-1111-1111-1111-111111111111"

// TestRunVolumeSet_RetypeWaitPollsUntilSettled covers the success path: the volume
// sits in "retyping" for a poll, then settles back to in-use carrying the new
// type. typeGetBody names the type "ssd", which is what cinder renders in
// volume_type.
func TestRunVolumeSet_RetypeWaitPollsUntilSettled(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fastPoll(t)

	gets := retypeWaitServer(t, fakeServer, waitVolID, waitTypeID, []string{
		volumeBody(waitVolID, "in-use", "hdd", ""),   // resolver
		volumeBody(waitVolID, "retyping", "hdd", ""), // retype in flight
		volumeBody(waitVolID, "in-use", "ssd", ""),   // settled on the new type
	})

	client := volumeClient(fakeServer, "3.59")
	f := &volumeSetFlags{}
	cmd := volumeSetCmd(t, f, map[string]string{"type": waitTypeID, "wait": "true"})
	var buf bytes.Buffer
	if err := runVolumeSet(context.Background(), client, waitVolID, f, cmd, &buf); err != nil {
		t.Fatalf("runVolumeSet with --wait: %v", err)
	}
	if *gets < 3 {
		t.Errorf("expected at least 3 GETs (resolve + 2 polls), got %d", *gets)
	}
	if !strings.Contains(buf.String(), "Volume "+waitVolID+" retyped to ssd") {
		t.Errorf("output missing completion line:\n%s", buf.String())
	}
}

// TestRunVolumeSet_RetypeWaitDetectsRollback is the case --wait exists for: the
// action returned 202 but cinder rolled the retype back, restoring the previous
// status with the old type still attached. Without the wait this looks like success.
func TestRunVolumeSet_RetypeWaitDetectsRollback(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fastPoll(t)

	retypeWaitServer(t, fakeServer, waitVolID, waitTypeID, []string{
		volumeBody(waitVolID, "in-use", "hdd", ""),
		volumeBody(waitVolID, "retyping", "hdd", ""),
		volumeBody(waitVolID, "in-use", "hdd", ""), // settled, type unchanged
	})

	client := volumeClient(fakeServer, "3.59")
	f := &volumeSetFlags{}
	cmd := volumeSetCmd(t, f, map[string]string{"type": waitTypeID, "wait": "true"})
	var buf bytes.Buffer
	err := runVolumeSet(context.Background(), client, waitVolID, f, cmd, &buf)
	if err == nil {
		t.Fatal("expected an error when the retype is rolled back, got nil")
	}
	if !strings.Contains(err.Error(), "did not take effect") {
		t.Errorf("error = %v, want it to report the retype did not take effect", err)
	}
}

func TestRunVolumeSet_RetypeWaitDetectsFailure(t *testing.T) {
	tests := []struct {
		name   string
		final  string
		expect string
	}{
		{"error status", volumeBody(waitVolID, "error", "hdd", ""), "error status"},
		{"migration error", volumeBody(waitVolID, "in-use", "hdd", "error"), "migration_status=error"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()
			fastPoll(t)

			retypeWaitServer(t, fakeServer, waitVolID, waitTypeID, []string{
				volumeBody(waitVolID, "in-use", "hdd", ""),
				tc.final,
			})

			client := volumeClient(fakeServer, "3.59")
			f := &volumeSetFlags{}
			cmd := volumeSetCmd(t, f, map[string]string{"type": waitTypeID, "wait": "true"})
			err := runVolumeSet(context.Background(), client, waitVolID, f, cmd, io.Discard)
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			if !strings.Contains(err.Error(), tc.expect) {
				t.Errorf("error = %v, want it to mention %q", err, tc.expect)
			}
		})
	}
}

// TestRunVolumeSet_RetypeWaitMatchesTypeByID covers the microversion >= 3.63 shape,
// where cinder also returns volume_type_id; matching on it must work even when
// volume_type still renders something else.
func TestRunVolumeSet_RetypeWaitMatchesTypeByID(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fastPoll(t)

	settled := fmt.Sprintf(`{"volume":{"id":%q,"status":"in-use","size":10,
	  "volume_type":"","volume_type_id":%q,"attachments":[],"metadata":{}}}`, waitVolID, waitTypeID)
	retypeWaitServer(t, fakeServer, waitVolID, waitTypeID, []string{
		volumeBody(waitVolID, "in-use", "hdd", ""),
		settled,
	})

	client := volumeClient(fakeServer, "3.59")
	f := &volumeSetFlags{}
	cmd := volumeSetCmd(t, f, map[string]string{"type": waitTypeID, "wait": "true"})
	var buf bytes.Buffer
	if err := runVolumeSet(context.Background(), client, waitVolID, f, cmd, &buf); err != nil {
		t.Fatalf("runVolumeSet with --wait matching by volume_type_id: %v", err)
	}
}

func TestRunVolumeMigrate_Wait(t *testing.T) {
	tests := []struct {
		name    string
		final   string
		wantErr string
	}{
		{"success", volumeBody(waitVolID, "in-use", "ssd", "success"), ""},
		{"failure", volumeBody(waitVolID, "in-use", "ssd", "error"), "failed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()
			fastPoll(t)

			retypeWaitServer(t, fakeServer, waitVolID, waitTypeID, []string{
				volumeBody(waitVolID, "in-use", "ssd", ""),          // resolver
				volumeBody(waitVolID, "in-use", "ssd", "migrating"), // in flight
				tc.final,
			})

			client := volumeClient(fakeServer, "3.59")
			f := &volumeMigrateFlags{host: "ctl2@lvm#pool0", wait: true}
			var buf bytes.Buffer
			err := runVolumeMigrate(context.Background(), client, waitVolID, f, &buf)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("runVolumeMigrate with --wait: %v", err)
				}
				if !strings.Contains(buf.String(), "migrated to host ctl2@lvm#pool0") {
					t.Errorf("output missing completion line:\n%s", buf.String())
				}
				return
			}
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

// TestWaitTimeout checks the poll gives up rather than hanging when the volume
// never leaves its transitional status.
func TestWaitForRetype_Timeout(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fastPoll(t)

	fakeServer.Mux.HandleFunc("/volumes/"+waitVolID, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(volumeBody(waitVolID, "retyping", "hdd", "")))
	})

	client := volumeClient(fakeServer, "3.59")
	target := retypeTarget{id: waitTypeID, name: "ssd"}
	err := waitForRetype(context.Background(), client, waitVolID, waitVolID, target, 50*time.Millisecond, io.Discard)
	if err == nil {
		t.Fatal("expected a timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "waiting for volume") {
		t.Errorf("error = %v, want a wait/timeout error", err)
	}
}

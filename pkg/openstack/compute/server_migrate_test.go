package compute_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gophercloud/gophercloud/v2"

	"github.com/ftarasenko/koc/pkg/openstack/compute"
)

type serverCall func(ctx context.Context, sc *gophercloud.ServiceClient) error

func TestMigrateCalls(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name, mv string
		call     serverCall
		body     string
	}{
		{"cold", "2.93", func(ctx context.Context, sc *gophercloud.ServiceClient) error {
			return compute.MigrateServer(ctx, sc, serverID, "")
		}, `{"migrate":null}`},
		{"cold to host", "2.93", func(ctx context.Context, sc *gophercloud.ServiceClient) error {
			return compute.MigrateServer(ctx, sc, serverID, "cmp-02")
		}, `{"migrate":{"host":"cmp-02"}}`},
		{"live auto", "2.93", func(ctx context.Context, sc *gophercloud.ServiceClient) error {
			return compute.LiveMigrateServer(ctx, sc, serverID, compute.LiveMigrateOpts{})
		}, `{"os-migrateLive":{"block_migration":"auto","host":null}}`},
		{"live block to host", "2.93", func(ctx context.Context, sc *gophercloud.ServiceClient) error {
			return compute.LiveMigrateServer(ctx, sc, serverID, compute.LiveMigrateOpts{Host: "cmp-02", BlockMigration: &yes})
		}, `{"os-migrateLive":{"block_migration":true,"host":"cmp-02"}}`},
		{"live shared", "2.93", func(ctx context.Context, sc *gophercloud.ServiceClient) error {
			return compute.LiveMigrateServer(ctx, sc, serverID, compute.LiveMigrateOpts{BlockMigration: &no})
		}, `{"os-migrateLive":{"block_migration":false,"host":null}}`},
		{"live below 2.25", "2.24", func(ctx context.Context, sc *gophercloud.ServiceClient) error {
			return compute.LiveMigrateServer(ctx, sc, serverID, compute.LiveMigrateOpts{DiskOverCommit: true})
		}, `{"os-migrateLive":{"block_migration":false,"disk_over_commit":true,"host":null}}`},
		{"live overcommit dropped", "2.25", func(ctx context.Context, sc *gophercloud.ServiceClient) error {
			return compute.LiveMigrateServer(ctx, sc, serverID, compute.LiveMigrateOpts{DiskOverCommit: true})
		}, `{"os-migrateLive":{"block_migration":"auto","host":null}}`},
		{"evacuate", "2.93", func(ctx context.Context, sc *gophercloud.ServiceClient) error {
			return compute.EvacuateServer(ctx, sc, serverID,
				compute.EvacuateOpts{Host: "cmp-02", AdminPass: "example", PreserveEphemeral: true})
		}, `{"evacuate":{"adminPass":"example","host":"cmp-02","preserve_ephemeral":true}}`},
		{"evacuate force", "2.67", func(ctx context.Context, sc *gophercloud.ServiceClient) error {
			return compute.EvacuateServer(ctx, sc, serverID, compute.EvacuateOpts{Host: "cmp-02", Force: true})
		}, `{"evacuate":{"force":true,"host":"cmp-02"}}`},
		{"evacuate force dropped", "2.68", func(ctx context.Context, sc *gophercloud.ServiceClient) error {
			return compute.EvacuateServer(ctx, sc, serverID, compute.EvacuateOpts{Host: "cmp-02", Force: true})
		}, `{"evacuate":{"host":"cmp-02"}}`},
		{"raw action", "2.93", func(ctx context.Context, sc *gophercloud.ServiceClient) error {
			return compute.ServerAction(ctx, sc, serverID, map[string]any{"addServerGroup": map[string]any{"group_uuid": groupID}})
		}, `{"addServerGroup":{"group_uuid":"` + groupID + `"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs, sc := nova(t, tc.mv)
			var rec calls
			fs.Mux.HandleFunc("/servers/"+serverID+"/action", rec.handler(func(_ *http.Request, _ string) (int, string) {
				return http.StatusAccepted, ""
			}))
			if err := tc.call(context.Background(), sc); err != nil {
				t.Fatal(err)
			}
			got := rec.only(t)
			if got.Method != http.MethodPost || got.Version != "compute "+tc.mv || got.Body != tc.body {
				t.Errorf("sent %+v, want POST at %s with %s", got, tc.mv, tc.body)
			}
		})
	}
}

func TestMigrateCalls_ErrorsNameTheServer(t *testing.T) {
	fs, sc := nova(t, "2.93")
	fs.Mux.HandleFunc("/servers/"+serverID+"/action", func(w http.ResponseWriter, _ *http.Request) {
		answer(w, http.StatusConflict, `{"conflictingRequest":{"code":409,"message":"locked"}}`)
	})
	ctx := context.Background()
	for _, tc := range []struct {
		prefix string
		err    error
	}{
		{"migrating server ", compute.MigrateServer(ctx, sc, serverID, "")},
		{"migrating server ", compute.MigrateServer(ctx, sc, serverID, "cmp-02")},
		{"live-migrating server ", compute.LiveMigrateServer(ctx, sc, serverID, compute.LiveMigrateOpts{})},
		{"evacuating server ", compute.EvacuateServer(ctx, sc, serverID, compute.EvacuateOpts{})},
		{"posting an action to server ", compute.ServerAction(ctx, sc, serverID, map[string]any{"lock": nil})},
	} {
		if tc.err == nil || !strings.HasPrefix(tc.err.Error(), tc.prefix+serverID+": ") {
			t.Errorf("err = %v, want it to start %q", tc.err, tc.prefix+serverID)
		}
		if !gophercloud.ResponseCodeIs(tc.err, http.StatusConflict) {
			t.Errorf("%v does not wrap gophercloud's 409", tc.err)
		}
	}
}

func TestConfirmMigration(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name      string
		confirm   int
		status    string
		wantAuto  bool
		wantError bool
	}{
		{name: "confirmed", confirm: http.StatusNoContent, status: "VERIFY_RESIZE"},
		{name: "nova got there first", confirm: http.StatusConflict, status: "ACTIVE", wantAuto: true},
		{name: "refused while still waiting", confirm: http.StatusConflict, status: "VERIFY_RESIZE", wantError: true},
		{name: "refused and unreadable", confirm: http.StatusConflict, status: "500", wantError: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs, sc := nova(t, "2.93")
			fs.Mux.HandleFunc("/servers/"+serverID+"/action", func(w http.ResponseWriter, _ *http.Request) {
				answer(w, tc.confirm, `{"conflictingRequest":{"code":409,"message":"not in VERIFY_RESIZE"}}`)
			})
			fs.Mux.HandleFunc("/servers/"+serverID, func(w http.ResponseWriter, _ *http.Request) {
				if tc.status == "500" {
					answer(w, http.StatusInternalServerError, `{"computeFault":{"code":500,"message":"boom"}}`)
					return
				}
				answer(w, http.StatusOK, `{"server":{"id":"`+serverID+`","status":"`+tc.status+`"}}`)
			})
			auto, err := compute.ConfirmMigration(ctx, sc, serverID)
			if auto != tc.wantAuto || (err != nil) != tc.wantError {
				t.Fatalf("ConfirmMigration = %v, %v", auto, err)
			}
			if err != nil && !strings.HasPrefix(err.Error(), "confirming resize of server "+serverID+": ") {
				t.Errorf("err = %v", err)
			}
		})
	}
}

// The decision WaitForMigration makes on each read.
func TestWaitForMigration(t *testing.T) {
	cases := []struct {
		name, status, task string
		settled            bool
		failed             bool
	}{
		{name: "active with no task is done", status: "ACTIVE", settled: true},
		// A cold migration parks the server awaiting a confirm.
		{name: "verify_resize with no task is done", status: "VERIFY_RESIZE", settled: true},
		// Nova keeps ACTIVE while it sets up a live migration.
		{name: "active while migrating keeps polling", status: "ACTIVE", task: "migrating"},
		// A live migration keeps the power state, so PAUSED stays PAUSED.
		{name: "paused with no task is done", status: "PAUSED", settled: true},
		{name: "paused while migrating keeps polling", status: "PAUSED", task: "migrating"},
		{name: "resize keeps polling", status: "RESIZE", task: "resize_migrating"},
		{name: "error is terminal", status: "ERROR", failed: true},
		// Nova's status casing has varied across releases.
		{name: "status matching ignores case", status: "active", settled: true},
		{name: "lower-case error is terminal", status: "error", failed: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sc, _ := serverStates(t, state(tc.status, tc.task))
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			status, err := compute.WaitForMigration(ctx, sc, serverID, time.Millisecond)
			var failed *compute.MigrationFailedError
			switch {
			case tc.settled:
				if err != nil || status != tc.status {
					t.Fatalf("WaitForMigration = %q, %v; want %q", status, err, tc.status)
				}
			case tc.failed:
				if !errors.As(err, &failed) || failed.ServerID != serverID || failed.Host != "" || failed.Arrived {
					t.Fatalf("err = %#v, want a MigrationFailedError for the server", err)
				}
				if err.Error() != "server "+serverID+" entered ERROR status during migration" {
					t.Errorf("err = %q", err)
				}
			default:
				if !errors.Is(err, context.DeadlineExceeded) ||
					err.Error() != "waiting for migration of server "+serverID+": context deadline exceeded" {
					t.Fatalf("err = %v, want the wait to run out", err)
				}
			}
		})
	}
}

func TestWaitForMigration_RidesOutFailedReads(t *testing.T) {
	ctx := context.Background()
	t.Run("five in a row", func(t *testing.T) {
		sc, reads := serverStates(t, "500", "500", "500", "500", "500", state("ACTIVE", ""))
		if _, err := compute.WaitForMigration(ctx, sc, serverID, time.Millisecond); err != nil {
			t.Fatal(err)
		}
		if *reads != 6 {
			t.Errorf("reads = %d, want 6", *reads)
		}
	})
	t.Run("six in a row", func(t *testing.T) {
		sc, reads := serverStates(t, "500")
		_, err := compute.WaitForMigration(ctx, sc, serverID, time.Millisecond)
		if err == nil || !strings.HasPrefix(err.Error(), "polling server "+serverID+" during migration: ") {
			t.Fatalf("err = %v", err)
		}
		if *reads != 6 {
			t.Errorf("reads = %d, want 6", *reads)
		}
	})
}

func onHost(status, task, host string) string {
	return state(status, task) + `,"OS-EXT-SRV-ATTR:host":"` + host + `"`
}

func TestWaitForServerToLeaveHost(t *testing.T) {
	ctx := context.Background()
	t.Run("waits for the host, not the status", func(t *testing.T) {
		// Still here migrating; still here with the task cleared (a status
		// wait would stop here); moved.
		sc, reads := serverStates(t, onHost("ACTIVE", "migrating", "cmp-1"), onHost("ACTIVE", "", "cmp-1"),
			onHost("ACTIVE", "", "cmp-2"))
		status, err := compute.WaitForServerToLeaveHost(ctx, sc, serverID, "cmp-1", "ACTIVE", time.Millisecond)
		if err != nil || status != "ACTIVE" || *reads != 3 {
			t.Fatalf("= %q, %v after %d reads", status, err, *reads)
		}
	})

	// What ERROR means depends on where the server started: an evacuation
	// moves servers that are already in ERROR.
	cases := []struct {
		name, status, startStatus, host string
		want                            string
	}{
		{name: "healthy server still moving", status: "ACTIVE", startStatus: "ACTIVE", host: "cmp-1"},
		{name: "errored server still on the failed host", status: "ERROR", startStatus: "ERROR", host: "cmp-1"},
		{name: "healthy server that broke on the way", status: "ERROR", startStatus: "ACTIVE", host: "cmp-1",
			want: "server " + serverID + ` entered ERROR status while leaving host "cmp-1"`},
		{name: "errored server that arrived still broken", status: "ERROR", startStatus: "ERROR", host: "cmp-2",
			want: "server " + serverID + " reached its new host in ERROR status"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sc, _ := serverStates(t, onHost(tc.status, "", tc.host))
			ctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
			defer cancel()
			_, err := compute.WaitForServerToLeaveHost(ctx, sc, serverID, "cmp-1", tc.startStatus, time.Millisecond)
			if tc.want == "" {
				if err == nil || err.Error() != "waiting for server "+serverID+` to leave host "cmp-1": context deadline exceeded` {
					t.Fatalf("err = %v, want the wait to run out", err)
				}
				return
			}
			var failed *compute.MigrationFailedError
			if !errors.As(err, &failed) || err.Error() != tc.want {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}

	t.Run("failed reads", func(t *testing.T) {
		sc, _ := serverStates(t, "500")
		_, err := compute.WaitForServerToLeaveHost(ctx, sc, serverID, "cmp-1", "ACTIVE", time.Millisecond)
		if err == nil || !strings.HasPrefix(err.Error(), "polling server "+serverID+" during the move: ") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestListServersOnHost(t *testing.T) {
	fs, sc := nova(t, "2.1")
	var query string
	fs.Mux.HandleFunc("/servers/detail", func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		answer(w, http.StatusOK, `{"servers":[{"id":"`+serverID+`","name":"web","status":"ACTIVE"}]}`)
	})
	list, err := compute.ListServersOnHost(context.Background(), sc, "cmp-1")
	if err != nil || len(list) != 1 || list[0].Name != "web" {
		t.Fatalf("= %v, %v", list, err)
	}
	if query != "all_tenants=true&host=cmp-1" {
		t.Errorf("query = %q", query)
	}

	fs2, sc2 := nova(t, "2.1")
	fs2.Mux.HandleFunc("/servers/detail", func(w http.ResponseWriter, _ *http.Request) {
		answer(w, http.StatusForbidden, `{"forbidden":{"code":403,"message":"policy"}}`)
	})
	if _, err := compute.ListServersOnHost(context.Background(), sc2, "cmp-1"); err == nil ||
		!strings.HasPrefix(err.Error(), `listing servers on host "cmp-1": `) {
		t.Errorf("err = %v", err)
	}
}

// LiveMigrateBody and EvacuateBody are what a caller posting the same action to
// many servers builds once.
func TestActionBodies(t *testing.T) {
	_, low := nova(t, "2.24")
	_, latest := nova(t, "latest")
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return string(b)
	}
	if got := enc(compute.LiveMigrateBody(low, compute.LiveMigrateOpts{})); got != `{"block_migration":false,"disk_over_commit":false,"host":null}` {
		t.Errorf("2.24 live body = %s", got)
	}
	if got := enc(compute.LiveMigrateBody(latest, compute.LiveMigrateOpts{DiskOverCommit: true})); got != `{"block_migration":"auto","host":null}` {
		t.Errorf("latest live body = %s", got)
	}
	if got := enc(compute.EvacuateBody(latest, compute.EvacuateOpts{Force: true})); got != `{}` {
		t.Errorf("latest evacuate body = %s", got)
	}
}

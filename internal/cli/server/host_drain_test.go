package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// migrateOnlyBody is the host's servers reduced to the one every mode can
// move, so a test about the migrate/confirm sequence is not also asserting
// eligibility.
const migrateOnlyBody = `{
  "servers": [
    {"id": "` + idWeb1 + `", "name": "web-1", "status": "ACTIVE", "OS-EXT-SRV-ATTR:host": "cmp-1"}
  ]
}`

// drainVerbFlags is the verb's default flag set: live, serial, no waiting.
// --confirm defaults true on the command but only bites under --cold.
func drainVerbFlags() *hostDrainVerbFlags {
	f := &hostDrainVerbFlags{confirm: true}
	f.hostDrainFlags = *drainFlags()
	return f
}

// coldFlags is the cold drain's default: confirm on, which implies waiting
// (the RunE does that; the tests set both explicitly).
func coldFlags() *hostDrainVerbFlags {
	f := drainVerbFlags()
	f.cold = true
	f.wait = true
	return f
}

// changedFlags is a flagChecker for the validation rules, standing in for the
// command's *pflag.FlagSet.
type changedFlags map[string]bool

func (c changedFlags) Changed(name string) bool { return c[name] }

// TestHostDrainFlagModes pins the per-mode flag rules. A live-only knob under
// --cold, or a cold-only one without it, is rejected rather than silently
// ignored — the silent version is how an operator ends up believing they forced
// a block migration that never happened.
func TestHostDrainFlagModes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cold    bool
		changed changedFlags
		wantErr string
	}{
		{name: "live defaults", changed: changedFlags{}},
		{name: "cold defaults", cold: true, changed: changedFlags{}},
		{
			name: "block-migration under --cold", cold: true,
			changed: changedFlags{"block-migration": true},
			wantErr: "--block-migration are only valid without --cold",
		},
		{
			name: "disk-overcommit under --cold", cold: true,
			changed: changedFlags{"disk-overcommit": true},
			wantErr: "--disk-overcommit are only valid without --cold",
		},
		{
			name:    "confirm without --cold",
			changed: changedFlags{"confirm": true},
			wantErr: "--confirm requires --cold",
		},
		{
			name: "block and shared together", changed: changedFlags{"block-migration": true},
			wantErr: "mutually exclusive",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := drainVerbFlags()
			f.cold = tc.cold
			for name := range tc.changed {
				switch name {
				case "block-migration":
					f.blockMigration = true
				case "disk-overcommit":
					f.diskOverCommit = true
				}
			}
			if tc.wantErr == "mutually exclusive" {
				f.sharedMigration = true
			}
			err := f.validate(tc.changed)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.wantErr != "" && err == nil:
				t.Fatalf("error = nil, want one containing %q", tc.wantErr)
			case tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr):
				t.Fatalf("error = %q, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

// TestDrainRefusesDeadHost is the mirror of TestHostEvacuateRefusesLiveHost:
// the pair is what makes the drain/evacuate split safe to get wrong, since
// each verb refuses the other's case and names it rather than letting the
// operator find out one refusal per server.
func TestDrainRefusesDeadHost(t *testing.T) {
	for _, cold := range []bool{false, true} {
		name := "live"
		if cold {
			name = "cold"
		}
		t.Run(name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()

			serveComputeService(fakeServer, "down")
			fakeServer.Mux.HandleFunc("/servers/detail", func(_ http.ResponseWriter, _ *http.Request) {
				t.Error("the drain must refuse before it lists the host's servers")
			})

			f := drainVerbFlags()
			f.cold = cold
			client := computeClient(fakeServer, "latest")
			mode, err := drainModeFor(client, f, &bytes.Buffer{})
			if err != nil {
				t.Fatalf("drainModeFor: %v", err)
			}
			o := &output.Options{Format: output.FormatTable}
			var out, progress bytes.Buffer
			err = runHostDrain(context.Background(), client, o, "cmp-1", &f.hostDrainFlags,
				mode, &out, &progress)
			if err == nil {
				t.Fatal("draining a host nova reports down must be refused")
			}
			for _, want := range []string{"is down", "compute host evacuate cmp-1"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error does not mention %q: %v", want, err)
				}
			}
		})
	}
}

// TestLiveDrainBody pins the os-migrateLive body the drain posts. It is built
// by liveMigrateBody, the same function "server migrate --live-migration"
// uses, so this is the assertion that the two verbs cannot drift apart.
func TestLiveDrainBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	serveServers(fakeServer, migrateOnlyBody, nil)
	rec := newActionRecorder(t, fakeServer, http.StatusAccepted, idWeb1)

	client := computeClient(fakeServer, "latest")
	f := drainVerbFlags()
	var warn bytes.Buffer
	mode, err := drainModeFor(client, f, &warn)
	if err != nil {
		t.Fatalf("drainModeFor: %v", err)
	}

	o := &output.Options{Format: output.FormatTable}
	var out, progress bytes.Buffer
	if err := runHostDrain(context.Background(), client, o, "cmp-1", &f.hostDrainFlags,
		mode, &out, &progress); err != nil {
		t.Fatalf("runHostDrain: %v", err)
	}

	body, ok := rec.body(idWeb1)["os-migrateLive"].(map[string]any)
	if !ok {
		t.Fatalf("action body is not an os-migrateLive object: %v", rec.body(idWeb1))
	}
	// "latest" negotiates above 2.25, so block_migration is "auto" and host is
	// sent as an explicit null rather than omitted — nova 400s without it.
	if body["block_migration"] != "auto" {
		t.Errorf("block_migration = %v, want auto", body["block_migration"])
	}
	if host, present := body["host"]; !present || host != nil {
		t.Errorf("host = %v (present %v), want an explicit null", host, present)
	}
}

// TestDrainTargetHost sends the destination through to nova in both modes, in
// each mode's own body shape.
func TestDrainTargetHost(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cold   bool
		action string
	}{
		{name: "live", action: "os-migrateLive"},
		{name: "cold", cold: true, action: "migrate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()

			serveServers(fakeServer, migrateOnlyBody, nil)
			rec := newActionRecorder(t, fakeServer, http.StatusAccepted, idWeb1)

			f := drainVerbFlags()
			f.cold = tc.cold
			f.confirm = false
			f.targetHost = "cmp-9"
			client := computeClient(fakeServer, "latest")
			mode, err := drainModeFor(client, f, &bytes.Buffer{})
			if err != nil {
				t.Fatalf("drainModeFor: %v", err)
			}
			o := &output.Options{Format: output.FormatTable}
			var out, progress bytes.Buffer
			if err := runHostDrain(context.Background(), client, o, "cmp-1", &f.hostDrainFlags,
				mode, &out, &progress); err != nil {
				t.Fatalf("runHostDrain: %v", err)
			}
			body, ok := rec.body(idWeb1)[tc.action].(map[string]any)
			if !ok {
				t.Fatalf("action body is not a %s object: %v", tc.action, rec.body(idWeb1))
			}
			if body["host"] != "cmp-9" {
				t.Errorf("host = %v, want cmp-9", body["host"])
			}
		})
	}
}

// coldFixture answers GET /servers/<id> as a server that has landed on cmp-2,
// and records how many confirms were posted.
type coldFixture struct {
	mu       sync.Mutex
	confirms int
	// status is what the server reports once it has left the host.
	status string
	// confirmCode is the HTTP status the confirmResize action answers with.
	confirmCode int
	// statusAfterConfirm, when set, is reported by GET after a rejected confirm
	// — the re-read that tells a race from a real refusal.
	statusAfterConfirm string
}

func (c *coldFixture) install(t *testing.T, fakeServer th.FakeServer) {
	t.Helper()
	fakeServer.Mux.HandleFunc("/servers/"+idWeb1, func(w http.ResponseWriter, _ *http.Request) {
		c.mu.Lock()
		status := c.status
		if c.confirms > 0 && c.statusAfterConfirm != "" {
			status = c.statusAfterConfirm
		}
		c.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"server": {"id": "` + idWeb1 + `", "status": "` + status + `",
		  "OS-EXT-STS:task_state": null, "OS-EXT-SRV-ATTR:host": "cmp-2"}}`))
	})
	fakeServer.Mux.HandleFunc("/servers/"+idWeb1+"/action", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if _, isConfirm := body["confirmResize"]; !isConfirm {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		c.mu.Lock()
		c.confirms++
		c.mu.Unlock()
		code := c.confirmCode
		if code == 0 {
			code = http.StatusNoContent
		}
		if code >= 400 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"conflictingRequest": {"message":
			  "Cannot 'confirmResize' instance while it is in vm_state active"}}`))
			return
		}
		w.WriteHeader(code)
	})
}

func (c *coldFixture) confirmCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.confirms
}

// runCold drives a --cold drain against the fixture.
func runCold(t *testing.T, fx *coldFixture, f *hostDrainVerbFlags) (out, progress bytes.Buffer, err error) {
	t.Helper()
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	serveServers(fakeServer, migrateOnlyBody, nil)
	fx.install(t, fakeServer)

	client := computeClient(fakeServer, "latest")
	mode, merr := drainModeFor(client, f, &bytes.Buffer{})
	if merr != nil {
		t.Fatalf("drainModeFor: %v", merr)
	}
	o := &output.Options{Format: output.FormatTable}
	err = runHostDrain(context.Background(), client, o, "cmp-1", &f.hostDrainFlags, mode, &out, &progress)
	return out, progress, err
}

// TestColdDrainConfirms is the default --cold path: the server lands in
// VERIFY_RESIZE and koc confirms it, so the host is actually empty when the
// command returns — which is what novaclient's host-servers-migrate does not do.
func TestColdDrainConfirms(t *testing.T) {
	fx := &coldFixture{status: "VERIFY_RESIZE"}
	out, _, err := runCold(t, fx, coldFlags())
	if err != nil {
		t.Fatalf("runHostDrain: %v", err)
	}
	if fx.confirmCount() != 1 {
		t.Fatalf("posted %d confirms, want 1", fx.confirmCount())
	}
	if !strings.Contains(out.String(), "migrated and confirmed") {
		t.Errorf("table does not report the confirm:\n%s", out.String())
	}
}

// TestColdDrainNovaAutoConfirmed covers a cloud with resize_confirm_window set:
// nova's own periodic task confirmed the resize before koc looked, so the
// server is already ACTIVE on its new host. That is a normal outcome, not a
// failure, and koc must not post a confirm for it.
func TestColdDrainNovaAutoConfirmed(t *testing.T) {
	fx := &coldFixture{status: "ACTIVE"}
	out, _, err := runCold(t, fx, coldFlags())
	if err != nil {
		t.Fatalf("a resize nova already confirmed must not fail the drain: %v", err)
	}
	if fx.confirmCount() != 0 {
		t.Errorf("posted %d confirms for a server nova had already confirmed", fx.confirmCount())
	}
	if !strings.Contains(out.String(), "auto-confirmed by nova") {
		t.Errorf("table does not report nova's own confirm:\n%s", out.String())
	}
}

// TestColdDrainConfirmRace is the same window, hit one step later: the server
// still read VERIFY_RESIZE, but nova's periodic task confirmed it between that
// read and koc's POST, so the confirm comes back 409. The re-read is what tells
// that apart from a real refusal.
func TestColdDrainConfirmRace(t *testing.T) {
	fx := &coldFixture{
		status:             "VERIFY_RESIZE",
		confirmCode:        http.StatusConflict,
		statusAfterConfirm: "ACTIVE",
	}
	out, _, err := runCold(t, fx, coldFlags())
	if err != nil {
		t.Fatalf("losing the confirm race to nova must not fail the drain: %v", err)
	}
	if !strings.Contains(out.String(), "auto-confirmed by nova") {
		t.Errorf("table does not report the lost race as success:\n%s", out.String())
	}
}

// TestColdDrainConfirmRefused is the other side of that 409: the server is
// still in VERIFY_RESIZE after the rejection, so nova meant it and the drain
// fails rather than reporting a confirm that never happened.
func TestColdDrainConfirmRefused(t *testing.T) {
	fx := &coldFixture{status: "VERIFY_RESIZE", confirmCode: http.StatusConflict}
	out, _, err := runCold(t, fx, coldFlags())
	if err == nil {
		t.Fatal("a genuinely refused confirm must fail the command")
	}
	if !strings.Contains(out.String(), "409 Conflict") {
		t.Errorf("table does not carry nova's refusal:\n%s", out.String())
	}
}

// TestColdDrainNoConfirm leaves the resizes pending, and must not post a
// confirm for any of them.
func TestColdDrainNoConfirm(t *testing.T) {
	f := coldFlags()
	f.confirm = false
	fx := &coldFixture{status: "VERIFY_RESIZE"}
	out, _, err := runCold(t, fx, f)
	if err != nil {
		t.Fatalf("runHostDrain: %v", err)
	}
	if fx.confirmCount() != 0 {
		t.Errorf("--confirm=false posted %d confirms", fx.confirmCount())
	}
	if !strings.Contains(out.String(), "VERIFY_RESIZE") {
		t.Errorf("table does not show the pending resize:\n%s", out.String())
	}
}

func TestColdMigrateBody(t *testing.T) {
	// No target host: nova's own {"migrate": null}, which is what gophercloud's
	// servers.Migrate posts.
	body := coldMigrateBody("")
	v, ok := body["migrate"]
	if !ok || v != nil {
		t.Errorf("coldMigrateBody(\"\") = %v, want {\"migrate\": null}", body)
	}
	// A target host is a nova 2.56 field and needs the object form.
	body = coldMigrateBody("cmp-9")
	obj, ok := body["migrate"].(map[string]any)
	if !ok || obj["host"] != "cmp-9" {
		t.Errorf("coldMigrateBody(\"cmp-9\") = %v, want the host in a migrate object", body)
	}
}

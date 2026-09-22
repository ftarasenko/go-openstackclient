package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Server IDs the drain fixtures use, so a handler can be registered per server.
const (
	idWeb1  = "11111111-1111-1111-1111-111111111111"
	idDB1   = "22222222-2222-2222-2222-222222222222"
	idWeb2  = "33333333-3333-3333-3333-333333333333"
	idCache = "44444444-4444-4444-4444-444444444444"
)

// hostServersBody is one host's worth of servers, spanning the statuses the
// three drains disagree about: ACTIVE (all three), SHUTOFF (cold and evacuate
// only), PAUSED (live only) and ERROR (evacuate only).
const hostServersBody = `{
  "servers": [
    {"id": "` + idWeb1 + `", "name": "web-1", "status": "ACTIVE", "OS-EXT-SRV-ATTR:host": "cmp-1"},
    {"id": "` + idDB1 + `", "name": "db-1", "status": "SHUTOFF", "OS-EXT-SRV-ATTR:host": "cmp-1"},
    {"id": "` + idWeb2 + `", "name": "web-2", "status": "PAUSED", "OS-EXT-SRV-ATTR:host": "cmp-1"},
    {"id": "` + idCache + `", "name": "cache-1", "status": "ERROR", "OS-EXT-SRV-ATTR:host": "cmp-1"}
  ]
}`

const noServersBody = `{"servers": []}`

// computeServicesBody is os-services filtered to one host, as nova answers the
// existence and liveness checks. The service is up, which is what a drain
// requires; the evacuate tests serve their own with state "down".
const computeServicesBody = `{
  "services": [
    {"id": 7, "binary": "nova-compute", "host": "cmp-1", "state": "up", "status": "enabled",
     "zone": "nova"}
  ]
}`

const noServicesBody = `{"services": []}`

// drainFlags is the flag set the tests start from: serial, no waiting, nothing
// forced — the defaults the commands register.
func drainFlags() *hostDrainFlags {
	return &hostDrainFlags{parallel: 1, waitTimeout: time.Minute}
}

// serveServers registers the discovery listing and records its query.
func serveServers(fakeServer th.FakeServer, body string, query *url.Values) {
	fakeServer.Mux.HandleFunc("/servers/detail", func(w http.ResponseWriter, r *http.Request) {
		if query != nil {
			*query = r.URL.Query()
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
}

// actionRecorder captures every server action body, keyed by server ID.
type actionRecorder struct {
	mu     sync.Mutex
	bodies map[string]map[string]any
	order  []string
}

func newActionRecorder(t *testing.T, fakeServer th.FakeServer, status int, ids ...string) *actionRecorder {
	t.Helper()
	rec := &actionRecorder{bodies: map[string]map[string]any{}}
	for _, id := range ids {
		fakeServer.Mux.HandleFunc("/servers/"+id+"/action", func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decoding action body for %s: %v", id, err)
			}
			rec.mu.Lock()
			rec.bodies[id] = body
			rec.order = append(rec.order, id)
			rec.mu.Unlock()
			w.WriteHeader(status)
		})
	}
	return rec
}

func (r *actionRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.bodies)
}

func (r *actionRecorder) body(id string) map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.bodies[id]
}

func (r *actionRecorder) touched(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.bodies[id]
	return ok
}

// TestRunHostDrain covers the engine end to end on the live mode: the discovery
// query (exact host + all_tenants), one action per eligible server, the
// ineligible ones left untouched, and the rendered table.
func TestRunHostDrain(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var query url.Values
	serveServers(fakeServer, hostServersBody, &query)
	rec := newActionRecorder(t, fakeServer, http.StatusAccepted, idWeb1, idDB1, idWeb2, idCache)

	client := computeClient(fakeServer, "latest")
	o := &output.Options{Format: output.FormatTable}
	var out, progress bytes.Buffer
	mode := liveDrainMode(map[string]any{"block_migration": "auto", "host": nil})
	if err := runHostDrain(context.Background(), client, o, "cmp-1", drainFlags(), mode, drainOutput{table: &out, progress: &progress}); err != nil {
		t.Fatalf("runHostDrain: %v", err)
	}

	if got := query.Get("host"); got != "cmp-1" {
		t.Errorf("discovery host = %q, want %q", got, "cmp-1")
	}
	if got := query.Get("all_tenants"); got != "true" {
		t.Errorf("discovery all_tenants = %q, want %q", got, "true")
	}
	// ACTIVE and PAUSED only: live migration cannot touch SHUTOFF or ERROR.
	if rec.count() != 2 {
		t.Fatalf("posted %d actions, want 2", rec.count())
	}
	for _, id := range []string{idDB1, idCache} {
		if rec.touched(id) {
			t.Errorf("server %s was migrated; it should have been skipped", id)
		}
	}

	table := out.String()
	for _, want := range []string{"web-1", drainAccepted, "db-1", drainSkipped, "web-2", "cache-1"} {
		if !strings.Contains(table, want) {
			t.Errorf("table missing %q:\n%s", want, table)
		}
	}
	if !strings.Contains(progress.String(), "2 skipped") {
		t.Errorf("summary does not count the skipped servers:\n%s", progress.String())
	}
}

// TestRunHostDrain_MaxServers leaves the rest of the host in place and says so.
func TestRunHostDrain_MaxServers(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	serveServers(fakeServer, hostServersBody, nil)
	rec := newActionRecorder(t, fakeServer, http.StatusAccepted, idWeb1, idWeb2)

	f := drainFlags()
	f.maxServers = 1
	client := computeClient(fakeServer, "latest")
	o := &output.Options{Format: output.FormatTable}
	var out, progress bytes.Buffer
	mode := liveDrainMode(map[string]any{})
	if err := runHostDrain(context.Background(), client, o, "cmp-1", f, mode, drainOutput{table: &out, progress: &progress}); err != nil {
		t.Fatalf("runHostDrain: %v", err)
	}
	if rec.count() != 1 {
		t.Fatalf("posted %d actions, want 1 under --max-servers=1", rec.count())
	}
	if !strings.Contains(out.String(), drainDeferred) {
		t.Errorf("table does not mark the servers left in place:\n%s", out.String())
	}
	if !strings.Contains(progress.String(), "were not attempted") {
		t.Errorf("summary does not warn that the host is not empty:\n%s", progress.String())
	}
}

// TestRunHostDrain_FailureExitsNonZero is the difference from novaclient that
// matters most: a failure is reported in the table *and* fails the command,
// rather than being printed while the process exits 0.
func TestRunHostDrain_FailureExitsNonZero(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	serveServers(fakeServer, hostServersBody, nil)
	newActionRecorder(t, fakeServer, http.StatusAccepted, idWeb1)
	fakeServer.Mux.HandleFunc("/servers/"+idWeb2+"/action", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"conflictingRequest": {"message": "Instance is locked"}}`))
	})

	client := computeClient(fakeServer, "latest")
	o := &output.Options{Format: output.FormatTable}
	var out, progress bytes.Buffer
	mode := liveDrainMode(map[string]any{})
	err := runHostDrain(context.Background(), client, o, "cmp-1", drainFlags(), mode, drainOutput{table: &out, progress: &progress})
	if err == nil {
		t.Fatal("a failed migration must fail the command")
	}
	if !strings.Contains(err.Error(), "1 of 2") {
		t.Errorf("error does not count the failures: %v", err)
	}
	// The table is still the command's output: a partial drain has to say which
	// half worked.
	if !strings.Contains(out.String(), drainAccepted) || !strings.Contains(out.String(), drainFailed) {
		t.Errorf("table does not carry both outcomes:\n%s", out.String())
	}
	// Nova's reason, not gophercloud's request line plus the whole body: five of
	// those stacked in a Detail column are unreadable.
	if !strings.Contains(out.String(), "409 Conflict: Instance is locked") {
		t.Errorf("table does not carry nova's condensed reason:\n%s", out.String())
	}
	if strings.Contains(out.String(), "Expected HTTP response code") {
		t.Errorf("table carries gophercloud's raw error string:\n%s", out.String())
	}
}

// TestRunHostDrain_UnknownHost is the typo guard: an exact-match listing
// returns nothing both for an empty host and for a misspelled one, and only the
// second is an error.
func TestRunHostDrain_UnknownHost(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	serveServers(fakeServer, noServersBody, nil)
	fakeServer.Mux.HandleFunc("/os-services", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// nova filters os-services server-side, so the existence check is the
		// query koc sends, not a scan of the answer.
		if r.URL.Query().Get("host") == "cmp-1" && r.URL.Query().Get("binary") == "nova-compute" {
			_, _ = w.Write([]byte(computeServicesBody))
			return
		}
		_, _ = w.Write([]byte(noServicesBody))
	})

	client := computeClient(fakeServer, "latest")
	o := &output.Options{Format: output.FormatTable}
	mode := liveDrainMode(map[string]any{})
	var out, progress bytes.Buffer
	err := runHostDrain(context.Background(), client, o, "cmp-typo", drainFlags(), mode, drainOutput{table: &out, progress: &progress})
	if err == nil {
		t.Fatal("a host nova does not have must be an error, not an empty drain")
	}
	if !strings.Contains(err.Error(), "no compute host named") {
		t.Errorf("unexpected error: %v", err)
	}

	// The same listing for a host that does exist — and whose service is up, so
	// the drain's own precheck passes — is a clean no-op.
	out.Reset()
	progress.Reset()
	if err := runHostDrain(context.Background(), client, o, "cmp-1", drainFlags(), mode, drainOutput{table: &out, progress: &progress}); err != nil {
		t.Fatalf("an empty but real host must not fail: %v", err)
	}
	if !strings.Contains(progress.String(), "nothing to move") {
		t.Errorf("empty host not reported:\n%s", progress.String())
	}
}

// TestRunHostDrain_DryRun must reach nova for the listing and for nothing else.
func TestRunHostDrain_DryRun(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	serveServers(fakeServer, hostServersBody, nil)
	fakeServer.Mux.HandleFunc("/servers/", func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("--dry-run must not call %s %s", r.Method, r.URL.Path)
	})

	f := drainFlags()
	f.dryRun = true
	client := computeClient(fakeServer, "latest")
	o := &output.Options{Format: output.FormatTable}
	var out, progress bytes.Buffer
	mode := liveDrainMode(map[string]any{})
	if err := runHostDrain(context.Background(), client, o, "cmp-1", f, mode, drainOutput{table: &out, progress: &progress}); err != nil {
		t.Fatalf("runHostDrain: %v", err)
	}
	if !strings.Contains(out.String(), drainPlanned) {
		t.Errorf("dry run does not mark the planned servers:\n%s", out.String())
	}
	if !strings.Contains(progress.String(), "Dry run: 2 of 4") {
		t.Errorf("dry-run summary wrong:\n%s", progress.String())
	}
}

// TestRunHostDrain_Parallel checks that --parallel really overlaps: each
// handler blocks until all three are in flight, which only completes if three
// workers run at once.
func TestRunHostDrain_Parallel(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	serveServers(fakeServer, `{"servers": [
	  {"id": "`+idWeb1+`", "name": "a", "status": "ACTIVE"},
	  {"id": "`+idDB1+`", "name": "b", "status": "ACTIVE"},
	  {"id": "`+idWeb2+`", "name": "c", "status": "ACTIVE"}]}`, nil)
	var wg sync.WaitGroup
	wg.Add(3)
	handler := func(w http.ResponseWriter, _ *http.Request) {
		wg.Done()
		wg.Wait() // deadlocks unless all three are posted concurrently
		w.WriteHeader(http.StatusAccepted)
	}
	for _, id := range []string{idWeb1, idDB1, idWeb2} {
		fakeServer.Mux.HandleFunc("/servers/"+id+"/action", handler)
	}

	f := drainFlags()
	f.parallel = 3
	client := computeClient(fakeServer, "latest")
	o := &output.Options{Format: output.FormatTable}
	var out, progress bytes.Buffer
	mode := liveDrainMode(map[string]any{})
	done := make(chan error, 1)
	go func() {
		done <- runHostDrain(context.Background(), client, o, "cmp-1", f, mode, drainOutput{table: &out, progress: &progress})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runHostDrain: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("--parallel 3 did not run three moves at once")
	}
}

func TestPlanDrain(t *testing.T) {
	list := extractTestServers(t, hostServersBody)
	plan := planDrain(list, 0, liveDrainMode(map[string]any{}))
	if len(plan.migrate) != 2 {
		t.Fatalf("planned %d moves, want 2", len(plan.migrate))
	}
	// Listing order is the table's order, so the ineligible servers keep their
	// slots.
	want := []string{drainPlanned, drainSkipped, drainPlanned, drainSkipped}
	for i, w := range want {
		if plan.results[i].result != w {
			t.Errorf("results[%d] = %q, want %q", i, plan.results[i].result, w)
		}
	}
}

// TestDrainEligibility pins each verb's accepted statuses to nova's own
// check_instance_state decorators (nova/compute/api.py). The three sets are
// deliberately different: a stopped server can be cold-migrated or evacuated
// but not live-migrated, a paused one only live-migrated, an errored one only
// evacuated.
func TestDrainEligibility(t *testing.T) {
	for _, tc := range []struct {
		status              string
		live, cold, evacuat bool
	}{
		{"ACTIVE", true, true, true},
		{"active", true, true, true},
		{"PAUSED", true, false, false},
		{"SHUTOFF", false, true, true},
		{"ERROR", false, false, true},
		{"SUSPENDED", false, false, false},
		{"SHELVED", false, false, false},
		{"VERIFY_RESIZE", false, false, false},
	} {
		if got := liveMigratable(tc.status); got != tc.live {
			t.Errorf("liveMigratable(%q) = %v, want %v", tc.status, got, tc.live)
		}
		if got := coldMigratable(tc.status); got != tc.cold {
			t.Errorf("coldMigratable(%q) = %v, want %v", tc.status, got, tc.cold)
		}
		if got := evacuable(tc.status); got != tc.evacuat {
			t.Errorf("evacuable(%q) = %v, want %v", tc.status, got, tc.evacuat)
		}
	}
}

// TestAwaitServerLeft pins the settle rule: the server has left the host and
// has no task in flight. Status alone cannot decide it — here the server is
// ACTIVE throughout, which is both where it starts and where it ends.
func TestAwaitServerLeft(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var mu sync.Mutex
	var gets int
	fakeServer.Mux.HandleFunc("/servers/"+idWeb1, func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		gets++
		n := gets
		mu.Unlock()
		// 1: still here, migrating. 2: still here, task cleared (a status-based
		// wait would stop here). 3: moved.
		host, task := "cmp-1", `"migrating"`
		switch n {
		case 2:
			task = "null"
		case 3:
			host, task = "cmp-2", "null"
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"server": {"id": "` + idWeb1 + `", "status": "ACTIVE",
		  "OS-EXT-STS:task_state": ` + task + `, "OS-EXT-SRV-ATTR:host": "` + host + `"}}`))
	})

	client := computeClient(fakeServer, "latest")
	status, err := awaitServerLeft(context.Background(), client, "web-1", idWeb1, "cmp-1", "ACTIVE", time.Minute)
	if err != nil {
		t.Fatalf("awaitServerLeft: %v", err)
	}
	if status != "ACTIVE" {
		t.Errorf("settled status = %q, want ACTIVE", status)
	}
	mu.Lock()
	defer mu.Unlock()
	if gets < 3 {
		t.Errorf("polled %d times; the wait settled before the server left the host", gets)
	}
}

// TestAwaitServerLeft_Error stops on ERROR rather than polling out the timeout.
func TestAwaitServerLeft_Error(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/servers/"+idWeb1, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"server": {"id": "` + idWeb1 + `", "status": "ERROR",
		  "OS-EXT-STS:task_state": null, "OS-EXT-SRV-ATTR:host": "cmp-1"}}`))
	})

	client := computeClient(fakeServer, "latest")
	_, err := awaitServerLeft(context.Background(), client, "web-1", idWeb1, "cmp-1", "ACTIVE", time.Minute)
	if err == nil {
		t.Fatal("ERROR must be terminal")
	}
	if !strings.Contains(err.Error(), "ERROR status") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestClassifyDrainState pins what ERROR means, which depends on where the
// server started. An evacuation moves instances that are already in ERROR, so
// treating the status alone as terminal aborts the very drain it exists for.
func TestClassifyDrainState(t *testing.T) {
	for _, tc := range []struct {
		name                string
		status, startStatus string
		left                bool
		wantErr             string
	}{
		{
			name: "healthy server still moving", status: "ACTIVE", startStatus: "ACTIVE",
		},
		{
			name: "healthy server that reached the destination", status: "ACTIVE",
			startStatus: "ACTIVE", left: true,
		},
		{
			// An evacuation's normal starting point: keep polling, do not abort.
			name: "errored server still on the failed host", status: "ERROR", startStatus: "ERROR",
		},
		{
			name: "healthy server that broke on the way", status: "ERROR", startStatus: "ACTIVE",
			wantErr: `server "web-1" entered ERROR status while leaving host "cmp-1"`,
		},
		{
			// It moved, so the rebuild ran — and failed.
			name: "errored server that reached the destination still broken", status: "ERROR",
			startStatus: "ERROR", left: true,
			wantErr: `server "web-1" reached its new host in ERROR status`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := classifyDrainState("web-1", "cmp-1", tc.status, tc.startStatus, tc.left)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.wantErr != "" && err == nil:
				t.Fatalf("error = nil, want %q", tc.wantErr)
			case tc.wantErr != "" && err.Error() != tc.wantErr:
				t.Fatalf("error = %q, want %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// TestDrainProgressNonTTY pins the log-file shape: one line per state change,
// no carriage returns or escape sequences.
func TestDrainProgressNonTTY(t *testing.T) {
	var buf bytes.Buffer
	p := newDrainProgress(&buf, "cmp-1", 1)
	if p.tty {
		t.Fatal("a bytes.Buffer must never be treated as a terminal")
	}
	stop := p.start()
	p.began("web-1", "starting")
	p.settled("web-1", drainCompleted, "")
	stop()

	out := buf.String()
	if strings.ContainsAny(out, "\r\x1b") {
		t.Errorf("non-terminal progress must not paint in place:\n%q", out)
	}
	for _, want := range []string{"web-1: starting", "web-1: completed"} {
		if !strings.Contains(out, want) {
			t.Errorf("progress missing %q:\n%s", want, out)
		}
	}
}

func TestTruncateRunes(t *testing.T) {
	for _, tc := range []struct {
		in   string
		max  int
		want string
	}{
		{"draining cmp-1", 0, "draining cmp-1"},
		{"draining cmp-1", 100, "draining cmp-1"},
		{"draining cmp-1", 8, "drainin…"},
		{"draining cmp-1", 1, "…"},
		// Multi-byte runes are counted, not bytes, so a truncated line is still
		// one terminal row wide.
		{"дренаж cmp-1", 7, "дренаж…"},
	} {
		if got := truncateRunes(tc.in, tc.max); got != tc.want {
			t.Errorf("truncateRunes(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
		}
	}
}

func TestDrainError(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"nova fault object": {
			err: gophercloud.ErrUnexpectedResponseCode{
				Actual: 409,
				Body:   []byte(`{"conflictingRequest": {"message": "Instance is locked"}}`),
			},
			want: "409 Conflict: Instance is locked",
		},
		"body that is not a nova fault": {
			err:  gophercloud.ErrUnexpectedResponseCode{Actual: 500, Body: []byte("<html>oops</html>")},
			want: "500 Internal Server Error",
		},
		"empty message falls back to the status": {
			err:  gophercloud.ErrUnexpectedResponseCode{Actual: 400, Body: []byte(`{"badRequest": {"message": ""}}`)},
			want: "400 Bad Request",
		},
		"a plain error keeps its first line": {
			err:  errors.New("dial tcp: connection refused\nstack trace"),
			want: "dial tcp: connection refused",
		},
	} {
		if got := drainError(tc.err); got != tc.want {
			t.Errorf("%s: drainError() = %q, want %q", name, got, tc.want)
		}
	}
}

// TestWhyNotEligible pins the advice a skipped row carries. The three verbs'
// accepted states barely overlap, so naming the wrong alternative is worse
// than naming none.
func TestWhyNotEligible(t *testing.T) {
	for _, tc := range []struct {
		op, status, want string
	}{
		// A stopped server: both of the other two take it.
		{"live-migrated", "SHUTOFF",
			"status SHUTOFF cannot be live-migrated; try compute host drain --cold or compute host evacuate"},
		// An errored one: only evacuation. Suggesting a cold migration here
		// would send the operator at a second 409.
		{"live-migrated", "ERROR",
			"status ERROR cannot be live-migrated; try compute host evacuate"},
		// A paused one cannot be cold-migrated, but can be live-migrated.
		{"cold-migrated", "PAUSED",
			"status PAUSED cannot be cold-migrated; try compute host drain"},
		// Nothing accepts a suspended server, so nothing is suggested.
		{"evacuated", "SUSPENDED", "status SUSPENDED cannot be evacuated"},
		{"live-migrated", "SHELVED", "status SHELVED cannot be live-migrated"},
	} {
		if got := whyNotEligible(tc.op, tc.status); got != tc.want {
			t.Errorf("whyNotEligible(%q, %q) =\n  %q\nwant\n  %q", tc.op, tc.status, got, tc.want)
		}
	}
}

func TestHostDrainFlagsValidate(t *testing.T) {
	for name, f := range map[string]*hostDrainFlags{
		"negative max-servers": {maxServers: -1, parallel: 1},
		"zero parallel":        {parallel: 0},
	} {
		if err := f.validate(); err == nil {
			t.Errorf("%s: want an error, got nil", name)
		}
	}
	if err := drainFlags().validate(); err != nil {
		t.Errorf("default flags rejected: %v", err)
	}
}

// extractTestServers decodes a canned /servers/detail body the way the command
// does, so planDrain is exercised on the same shape nova sends.
func extractTestServers(t *testing.T, body string) []servers.Server {
	t.Helper()
	var doc struct {
		Servers []servers.Server `json:"servers"`
	}
	if err := json.NewDecoder(strings.NewReader(body)).Decode(&doc); err != nil {
		t.Fatalf("decoding fixture: %v", err)
	}
	return doc.Servers
}

package server

import (
	"fmt"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"context"
)

// The gatherActuals tests below are the only thing in the tree that runs the
// gatherer's goroutines, so they are what makes `go test -race` say anything
// about it: N goroutines write to distinct &rows[i] addresses of a
// caller-owned slice, guarded only by a bounded semaphore and a WaitGroup.
//
// gatherActuals builds every URL as "<scheme>://<address>:<port>/metrics" from a
// single shared port, so pointing several rows at several ports is impossible.
// Instead one server is bound to all interfaces and each row is addressed by a
// distinct loopback alias (127.0.0.1, 127.0.0.2, …); the handler dispatches on
// the Host header, which is how one process serves per-row behaviour on one port.

// neSample1/neSample2 are two consecutive scrapes of one host: idle +5 of total
// +20 → 75% busy, and 6 GiB of 8 GB in use → 75% memory.
const (
	neSample1 = `# HELP node_cpu_seconds_total Seconds the CPUs spent in each mode.
node_cpu_seconds_total{cpu="0",mode="idle"} 100
node_cpu_seconds_total{cpu="0",mode="user"} 30
node_memory_MemTotal_bytes 8.0e9
node_memory_MemAvailable_bytes 2.0e9
`
	neSample2 = `node_cpu_seconds_total{cpu="0",mode="idle"} 105
node_cpu_seconds_total{cpu="0",mode="user"} 45
node_memory_MemTotal_bytes 8.0e9
node_memory_MemAvailable_bytes 2.0e9
`
)

// neHostBehaviour describes what the fake node_exporter fleet does for one
// loopback alias.
type neHostBehaviour struct {
	// status, when non-zero, is returned instead of metrics.
	status int
	// secondStatus, when non-zero, is returned on the second and later scrapes.
	secondStatus int
	// hang blocks until the client gives up (or the server shuts down), which is
	// how a --ne-timeout is exercised.
	hang bool
}

// neFleet is a fake node_exporter fleet: one listener, many loopback aliases.
type neFleet struct {
	port     int
	hosts    map[string]neHostBehaviour
	inFlight atomic.Int64
	maxSeen  atomic.Int64
	// completed counts scrapes whose handler has returned, i.e. whose body is on
	// the wire. Cancellation tests wait on this rather than on arrivals.
	completed atomic.Int64

	mu      sync.Mutex
	scrapes map[string]int64 // host -> scrapes served
}

// count records a scrape of host and returns how many it has now served.
func (f *neFleet) count(host string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.scrapes == nil {
		f.scrapes = map[string]int64{}
	}
	f.scrapes[host]++
	return f.scrapes[host]
}

func (f *neFleet) scrapesFor(host string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.scrapes[host]
}

// newNEFleet starts the fleet and returns it, skipping the test when the sandbox
// will not let a listener answer on loopback aliases.
func newNEFleet(t *testing.T, hosts map[string]neHostBehaviour) *neFleet {
	t.Helper()
	f := &neFleet{hosts: hosts}

	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "0.0.0.0:0")
	if err != nil {
		t.Skipf("cannot bind all interfaces (%v); per-row loopback aliases unavailable", err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Track observed parallelism so the semaphore can be asserted on.
		cur := f.inFlight.Add(1)
		for {
			old := f.maxSeen.Load()
			if cur <= old || f.maxSeen.CompareAndSwap(old, cur) {
				break
			}
		}
		defer f.inFlight.Add(-1)

		host, _, splitErr := net.SplitHostPort(r.Host)
		if splitErr != nil {
			host = r.Host
		}
		if r.URL.Path == "/probe" {
			_, _ = fmt.Fprint(w, "ok")
			return
		}
		if r.URL.Path != "/metrics" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		n := f.count(host)
		defer f.completed.Add(1)
		b := f.hosts[host]
		switch {
		case b.hang:
			// Wait for the client's timeout; return promptly once it disconnects so
			// the test does not pay for the full sleep.
			select {
			case <-r.Context().Done():
			case <-time.After(10 * time.Second):
			}
			return
		case b.status != 0:
			w.WriteHeader(b.status)
			return
		case b.secondStatus != 0 && n > 1:
			w.WriteHeader(b.secondStatus)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		if n == 1 {
			_, _ = fmt.Fprint(w, neSample1)
			return
		}
		_, _ = fmt.Fprint(w, neSample2)
	}))
	_ = srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)

	_, portStr, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("listener address %q: %v", ln.Addr(), err)
	}
	f.port, err = strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("listener port %q: %v", portStr, err)
	}

	// Confirm a non-.1 loopback alias really reaches the listener before relying
	// on it for per-row behaviour.
	probeCtx, cancelProbe := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelProbe()
	probe, err := http.NewRequestWithContext(probeCtx, http.MethodGet,
		fmt.Sprintf("http://127.0.0.2:%d/probe", f.port), nil)
	if err != nil {
		t.Fatalf("building the loopback-alias probe: %v", err)
	}
	resp, err := http.DefaultClient.Do(probe)
	if err != nil {
		t.Skipf("loopback alias 127.0.0.2 unreachable (%v); cannot address rows individually", err)
	}
	_ = resp.Body.Close()
	return f
}

func (f *neFleet) opts(concurrency int, timeout, interval float64) neOpts {
	return neOpts{
		port:           f.port,
		scheme:         "http",
		addressFrom:    "host_ip",
		timeout:        timeout,
		sampleInterval: interval,
		concurrency:    concurrency,
	}
}

func nearly(got, want float64) bool { return math.Abs(got-want) < 0.01 }

// TestGatherActuals_AllTargetsSucceed runs the gatherer over more targets than
// its concurrency limit, so the semaphore, the WaitGroup and the per-row writes
// all get exercised under -race.
func TestGatherActuals_AllTargetsSucceed(t *testing.T) {
	t.Parallel()
	hosts := map[string]neHostBehaviour{
		"127.0.0.1": {}, "127.0.0.2": {}, "127.0.0.3": {}, "127.0.0.4": {}, "127.0.0.5": {},
	}
	f := newNEFleet(t, hosts)

	rows := []hostRow{
		{name: "hv-1", hostIP: "127.0.0.1", state: "up", status: "enabled", cpuPhysPct: -1, ramPhysPct: -1},
		{name: "hv-2", hostIP: "127.0.0.2", state: "up", status: "enabled", cpuPhysPct: -1, ramPhysPct: -1},
		{name: "hv-3", hostIP: "127.0.0.3", state: "up", status: "enabled", cpuPhysPct: -1, ramPhysPct: -1},
		{name: "hv-4", hostIP: "127.0.0.4", state: "up", status: "enabled", cpuPhysPct: -1, ramPhysPct: -1},
		{name: "hv-5", hostIP: "127.0.0.5", state: "up", status: "enabled", cpuPhysPct: -1, ramPhysPct: -1},
	}
	gatherActuals(context.Background(), rows, f.opts(3, 5, 0.01))

	for i, r := range rows {
		if r.actualErr != "" {
			t.Errorf("rows[%d] (%s): actualErr = %q, want none", i, r.name, r.actualErr)
		}
		if !nearly(r.cpuPhysPct, 75) {
			t.Errorf("rows[%d] (%s): cpuPhysPct = %v, want 75", i, r.name, r.cpuPhysPct)
		}
		if !nearly(r.ramPhysPct, 75) {
			t.Errorf("rows[%d] (%s): ramPhysPct = %v, want 75", i, r.name, r.ramPhysPct)
		}
		if r.ramPhysUsedB != 6e9 {
			t.Errorf("rows[%d] (%s): ramPhysUsedB = %v, want 6e9", i, r.name, r.ramPhysUsedB)
		}
		// Two samples per host: the CPU percentage is a delta.
		if n := f.scrapesFor(r.hostIP); n != 2 {
			t.Errorf("rows[%d] (%s): %d scrapes, want 2", i, r.name, n)
		}
	}
	if n := f.maxSeen.Load(); n > 3 {
		t.Errorf("observed %d concurrent scrapes, want at most the --ne-concurrency of 3", n)
	}
}

// TestGatherActuals_FailuresDoNotCorruptOtherRows mixes healthy hosts with an
// HTTP error, a timeout, a failure on the second sample, a down host and a host
// with no address, and pins each row's outcome — a per-row write must not leak
// into its neighbours.
func TestGatherActuals_FailuresDoNotCorruptOtherRows(t *testing.T) {
	t.Parallel()
	f := newNEFleet(t, map[string]neHostBehaviour{
		"127.0.0.1": {},
		"127.0.0.2": {status: http.StatusInternalServerError},
		"127.0.0.3": {hang: true},
		"127.0.0.4": {},
		"127.0.0.5": {secondStatus: http.StatusServiceUnavailable},
	})

	rows := []hostRow{
		{name: "hv-ok-1", hostIP: "127.0.0.1", state: "up", status: "enabled", cpuPhysPct: -1, ramPhysPct: -1},
		{name: "hv-500", hostIP: "127.0.0.2", state: "up", status: "enabled", cpuPhysPct: -1, ramPhysPct: -1},
		{name: "hv-slow", hostIP: "127.0.0.3", state: "up", status: "enabled", cpuPhysPct: -1, ramPhysPct: -1},
		{name: "hv-ok-2", hostIP: "127.0.0.4", state: "up", status: "enabled", cpuPhysPct: -1, ramPhysPct: -1},
		{name: "hv-503-2nd", hostIP: "127.0.0.5", state: "up", status: "enabled", cpuPhysPct: -1, ramPhysPct: -1},
		// A down host is never scraped: the renderer shows "n/a (down)".
		{name: "hv-down", hostIP: "127.0.0.1", state: "down", status: "enabled", cpuPhysPct: 12, ramPhysPct: 34},
		// No host_ip and no resolvable name at all.
		{name: "", hostIP: "", state: "up", status: "enabled", cpuPhysPct: 12, ramPhysPct: 34},
	}
	// A short timeout keeps the hanging host cheap; the sample interval stays
	// small so the healthy hosts finish quickly.
	gatherActuals(context.Background(), rows, f.opts(4, 0.4, 0.01))

	// The two healthy hosts are unaffected by their failing neighbours.
	for _, i := range []int{0, 3} {
		r := rows[i]
		if r.actualErr != "" || !nearly(r.cpuPhysPct, 75) || !nearly(r.ramPhysPct, 75) {
			t.Errorf("rows[%d] (%s): healthy row corrupted: err=%q cpu=%v ram=%v",
				i, r.name, r.actualErr, r.cpuPhysPct, r.ramPhysPct)
		}
	}
	if got := rows[1].actualErr; got != "http 500" {
		t.Errorf("rows[1] (hv-500): actualErr = %q, want %q", got, "http 500")
	}
	if got := rows[2].actualErr; got == "" || !strings.Contains(got, "context deadline exceeded") {
		t.Errorf("rows[2] (hv-slow): actualErr = %q, want a timeout error", got)
	}
	if got := rows[4].actualErr; got != "http 503" {
		t.Errorf("rows[4] (hv-503-2nd): actualErr = %q, want %q (second sample failed)", got, "http 503")
	}
	if got := rows[5].actualErr; got != "down" {
		t.Errorf("rows[5] (hv-down): actualErr = %q, want %q", got, "down")
	}
	if got := rows[6].actualErr; got != "no address" {
		t.Errorf("rows[6] (no address): actualErr = %q, want %q", got, "no address")
	}
	// Every failing row reports "unknown" (-1) rather than a stale number.
	for _, i := range []int{1, 2, 4, 5, 6} {
		if rows[i].cpuPhysPct != -1 || rows[i].ramPhysPct != -1 {
			t.Errorf("rows[%d] (%s): failed row kept cpu=%v ram=%v, want -1/-1",
				i, rows[i].name, rows[i].cpuPhysPct, rows[i].ramPhysPct)
		}
	}
	// The down row and the addressless row must not have issued a scrape; hosts
	// 127.0.0.1 is shared with a healthy row, so only count the second sample.
	if n := f.scrapesFor("127.0.0.1"); n != 2 {
		t.Errorf("127.0.0.1 scraped %d times, want 2 (the down row must not scrape)", n)
	}
	if n := f.scrapesFor("127.0.0.5"); n != 2 {
		t.Errorf("127.0.0.5 scraped %d times, want 2", n)
	}
}

// TestGatherActuals_ContextCancelledMidFlight cancels the context once every
// goroutine has taken its first sample, so each row lands in the ctx.Done() arm
// of the inter-sample wait (nodeexporter.go:162).
func TestGatherActuals_ContextCancelledMidFlight(t *testing.T) {
	t.Parallel()
	f := newNEFleet(t, map[string]neHostBehaviour{
		"127.0.0.1": {}, "127.0.0.2": {}, "127.0.0.3": {},
	})
	rows := []hostRow{
		{name: "hv-1", hostIP: "127.0.0.1", state: "up", status: "enabled", cpuPhysPct: -1, ramPhysPct: -1},
		{name: "hv-2", hostIP: "127.0.0.2", state: "up", status: "enabled", cpuPhysPct: -1, ramPhysPct: -1},
		{name: "hv-3", hostIP: "127.0.0.3", state: "up", status: "enabled", cpuPhysPct: -1, ramPhysPct: -1},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// All three rows run at once (concurrency 3), so once three scrapes have been
	// *served* every goroutine has its first sample and is waiting out the sample
	// interval. The grace period after that covers the handful of microseconds
	// between the handler returning and the client parsing the response, so the
	// cancellation lands in the inter-sample wait and not in the scrape itself.
	done := make(chan struct{})
	go func() {
		defer close(done)
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) && f.completed.Load() < 3 {
			time.Sleep(time.Millisecond)
		}
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()

	// A 30s sample interval: the run can only finish by way of the cancellation.
	start := time.Now()
	gatherActuals(ctx, rows, f.opts(3, 5, 30))
	<-done

	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("gatherActuals took %v; the inter-sample wait must honour ctx", elapsed)
	}
	for i, r := range rows {
		if r.actualErr != "cancelled" {
			t.Errorf("rows[%d] (%s): actualErr = %q, want %q", i, r.name, r.actualErr, "cancelled")
		}
		if r.cpuPhysPct != -1 || r.ramPhysPct != -1 {
			t.Errorf("rows[%d] (%s): cancelled row kept cpu=%v ram=%v, want -1/-1",
				i, r.name, r.cpuPhysPct, r.ramPhysPct)
		}
	}
}

// TestGatherActuals_ConcurrencyFloor: --ne-concurrency 0 must not deadlock on a
// zero-capacity semaphore; it is clamped to 1 and the rows run serially.
func TestGatherActuals_ConcurrencyFloor(t *testing.T) {
	t.Parallel()
	f := newNEFleet(t, map[string]neHostBehaviour{"127.0.0.1": {}, "127.0.0.2": {}})
	rows := []hostRow{
		{name: "hv-1", hostIP: "127.0.0.1", state: "up", status: "enabled", cpuPhysPct: -1, ramPhysPct: -1},
		{name: "hv-2", hostIP: "127.0.0.2", state: "up", status: "enabled", cpuPhysPct: -1, ramPhysPct: -1},
	}
	gatherActuals(context.Background(), rows, f.opts(0, 5, 0.01))

	for i, r := range rows {
		if r.actualErr != "" || !nearly(r.cpuPhysPct, 75) {
			t.Errorf("rows[%d] (%s): err=%q cpu=%v, want a clean 75", i, r.name, r.actualErr, r.cpuPhysPct)
		}
	}
	if n := f.maxSeen.Load(); n > 1 {
		t.Errorf("observed %d concurrent scrapes with --ne-concurrency 0, want 1", n)
	}
}

// TestGatherActuals_NoRows is the guard the caller relies on
// (hypervisor.go:218 checks len(rows) > 0): an empty slice is a no-op.
func TestGatherActuals_NoRows(t *testing.T) {
	t.Parallel()
	gatherActuals(context.Background(), nil, neOpts{port: 9100, scheme: "http", concurrency: 4, timeout: 1, sampleInterval: 0.01})
}

// TestGatherActuals_AddressFromName reaches the fleet through the name +
// --ne-domain-suffix path instead of host_ip.
func TestGatherActuals_AddressFromName(t *testing.T) {
	t.Parallel()
	f := newNEFleet(t, map[string]neHostBehaviour{"127.0.0.1": {}})
	o := f.opts(2, 5, 0.01)
	o.addressFrom = "name"
	o.domainSuffix = ".1"

	rows := []hostRow{{name: "127.0.0", state: "up", status: "enabled", cpuPhysPct: -1, ramPhysPct: -1}}
	gatherActuals(context.Background(), rows, o)

	if rows[0].actualErr != "" || !nearly(rows[0].cpuPhysPct, 75) {
		t.Errorf("name-addressed row: err=%q cpu=%v", rows[0].actualErr, rows[0].cpuPhysPct)
	}
}

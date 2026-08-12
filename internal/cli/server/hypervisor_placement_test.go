package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	th "github.com/gophercloud/gophercloud/v2/testhelper"
	fakeclient "github.com/gophercloud/gophercloud/v2/testhelper/client"
)

// enrichFromPlacement fans out one inventories+usages pair of GETs per row over
// a bounded semaphore, writing each result into its own &rows[i]. These tests are
// what makes `go test -race` cover that fan-out: every case drives 4+ rows
// concurrently, and the mixed case proves one provider's failure cannot bleed
// into another row.

// plcProvider is one fake placement resource provider.
type plcProvider struct {
	uuid string
	name string
	// invStatus/useStatus, when non-zero, are returned instead of a body.
	invStatus int
	useStatus int
	vcpuTotal int
	vcpuUsed  int
	ramTotal  int
	ramUsed   int
	diskTotal int // 0 omits DISK_GB from the inventory entirely
	diskUsed  int
}

// plcFleet is a fake placement API: a provider listing plus per-provider
// inventories and usages.
type plcFleet struct {
	providers []plcProvider
	listCode  int // non-zero: fail the provider listing
	requests  atomic.Int64

	mu      sync.Mutex
	perPath map[string]int64
}

func (f *plcFleet) hit(path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.perPath == nil {
		f.perPath = map[string]int64{}
	}
	f.perPath[path]++
}

func (f *plcFleet) hits(path string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.perPath[path]
}

func (f *plcFleet) install(t *testing.T) *gophercloud.ServiceClient {
	t.Helper()
	fakeServer := th.SetupHTTP()
	t.Cleanup(fakeServer.Teardown)

	byUUID := make(map[string]plcProvider, len(f.providers))
	for _, p := range f.providers {
		byUUID[p.uuid] = p
	}

	fakeServer.Mux.HandleFunc("/resource_providers", func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		th.AssertEquals(t, http.MethodGet, r.Method)
		if f.listCode != 0 {
			w.WriteHeader(f.listCode)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		parts := make([]string, 0, len(f.providers))
		for _, p := range f.providers {
			parts = append(parts, fmt.Sprintf(`{"uuid":%q,"name":%q,"generation":1}`, p.uuid, p.name))
		}
		_, _ = fmt.Fprintf(w, `{"resource_providers":[%s]}`, strings.Join(parts, ","))
	})

	fakeServer.Mux.HandleFunc("/resource_providers/", func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		f.hit(r.URL.Path)
		th.AssertEquals(t, http.MethodGet, r.Method)

		seg := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(seg) != 3 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		p, ok := byUUID[seg[1]]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch seg[2] {
		case "inventories":
			if p.invStatus != 0 {
				w.WriteHeader(p.invStatus)
				return
			}
			inv := []string{
				fmt.Sprintf(`"VCPU":{"total":%d,"allocation_ratio":4.0,"max_unit":%d,"min_unit":1,"reserved":0,"step_size":1}`, p.vcpuTotal, p.vcpuTotal),
				fmt.Sprintf(`"MEMORY_MB":{"total":%d,"allocation_ratio":1.0,"max_unit":%d,"min_unit":1,"reserved":0,"step_size":1}`, p.ramTotal, p.ramTotal),
			}
			if p.diskTotal > 0 {
				inv = append(inv, fmt.Sprintf(`"DISK_GB":{"total":%d,"allocation_ratio":1.0,"max_unit":%d,"min_unit":1,"reserved":0,"step_size":1}`, p.diskTotal, p.diskTotal))
			}
			_, _ = fmt.Fprintf(w, `{"resource_provider_generation":1,"inventories":{%s}}`, strings.Join(inv, ","))
		case "usages":
			if p.useStatus != 0 {
				w.WriteHeader(p.useStatus)
				return
			}
			use := []string{
				fmt.Sprintf(`"VCPU":%d`, p.vcpuUsed),
				fmt.Sprintf(`"MEMORY_MB":%d`, p.ramUsed),
			}
			if p.diskTotal > 0 {
				use = append(use, fmt.Sprintf(`"DISK_GB":%d`, p.diskUsed))
			}
			_, _ = fmt.Fprintf(w, `{"resource_provider_generation":1,"usages":{%s}}`, strings.Join(use, ","))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	return fakeclient.ServiceClient(fakeServer)
}

// novaRow is a row as gatherHypervisorRows left it: nova-sourced numbers that
// placement may override.
func novaRow(name string) hostRow {
	return hostRow{
		name: name, htype: "QEMU", state: "up", status: "enabled",
		vcpusUsed: 1, vcpusTotal: 2, overcommit: 0.5, cpuAllocPct: 50,
		ramUsedMB: 1024, ramTotalMB: 2048, ramPct: 50,
		diskUsedGB: 100, diskTotGB: 400, diskPct: 25,
		cpuPhysPct: -1, ramPhysPct: -1,
	}
}

// TestEnrichFromPlacement_MixedOutcomes drives every branch of the fan-out at
// once: a fully enriched row, a row whose provider has no DISK_GB, a row whose
// inventories call fails, a row whose usages call fails, and a row placement has
// never heard of. A failure must leave that row's nova values alone and touch no
// other row.
func TestEnrichFromPlacement_MixedOutcomes(t *testing.T) {
	t.Parallel()
	f := &plcFleet{providers: []plcProvider{
		{uuid: "11111111-1111-4111-8111-111111111111", name: "hv-full",
			vcpuTotal: 96, vcpuUsed: 384, ramTotal: 512 * 1024, ramUsed: 256 * 1024, diskTotal: 3666, diskUsed: 1800},
		{uuid: "22222222-2222-4222-8222-222222222222", name: "hv-nodisk",
			vcpuTotal: 88, vcpuUsed: 44, ramTotal: 256 * 1024, ramUsed: 64 * 1024},
		{uuid: "33333333-3333-4333-8333-333333333333", name: "hv-invfail",
			invStatus: http.StatusInternalServerError, vcpuTotal: 8, vcpuUsed: 8},
		{uuid: "44444444-4444-4444-8444-444444444444", name: "hv-usefail",
			useStatus: http.StatusForbidden, vcpuTotal: 8, vcpuUsed: 8, ramTotal: 4096, diskTotal: 40},
	}}
	pc := f.install(t)

	rows := []hostRow{
		novaRow("hv-full"), novaRow("hv-nodisk"), novaRow("hv-invfail"),
		novaRow("hv-usefail"), novaRow("hv-unknown"),
	}
	enrichFromPlacement(context.Background(), pc, rows)

	// Fully enriched: 384 of 96 vCPU is a 4.0 overcommit.
	full := rows[0]
	if full.vcpusTotal != 96 || full.vcpusUsed != 384 {
		t.Errorf("hv-full vcpu = %d/%d, want 384/96", full.vcpusUsed, full.vcpusTotal)
	}
	if !nearly(full.overcommit, 4) || !nearly(full.cpuAllocPct, 400) {
		t.Errorf("hv-full overcommit = %v (%v%%), want 4.0 (400%%)", full.overcommit, full.cpuAllocPct)
	}
	if full.ramTotalMB != 512*1024 || !nearly(full.ramPct, 50) {
		t.Errorf("hv-full ram = %v%% of %v", full.ramPct, full.ramTotalMB)
	}
	if full.diskTotGB != 3666 || full.diskUsedGB != 1800 {
		t.Errorf("hv-full disk = %v/%v, want 1800/3666", full.diskUsedGB, full.diskTotGB)
	}

	// No DISK_GB inventory: nova's disk numbers survive, the rest is overridden.
	nodisk := rows[1]
	if nodisk.vcpusTotal != 88 || nodisk.ramTotalMB != 256*1024 {
		t.Errorf("hv-nodisk vcpu/ram = %d/%v, want 88/262144", nodisk.vcpusTotal, nodisk.ramTotalMB)
	}
	if nodisk.diskTotGB != 400 || nodisk.diskUsedGB != 100 || !nearly(nodisk.diskPct, 25) {
		t.Errorf("hv-nodisk disk = %v/%v (%v%%), want the nova values 100/400 (25%%)",
			nodisk.diskUsedGB, nodisk.diskTotGB, nodisk.diskPct)
	}

	// A failing provider leaves its row exactly as nova had it.
	for _, i := range []int{2, 3, 4} {
		want := novaRow(rows[i].name)
		if rows[i] != want {
			t.Errorf("rows[%d] (%s) changed on a failed/absent provider:\n got %+v\nwant %+v",
				i, rows[i].name, rows[i], want)
		}
	}

	// The unknown host is never looked up (no UUID to look up with).
	for _, p := range f.providers {
		if n := f.hits("/resource_providers/" + p.uuid + "/inventories"); n != 1 {
			t.Errorf("%s inventories fetched %d times, want 1", p.name, n)
		}
	}
	// usages is only reached when inventories succeeded.
	if n := f.hits("/resource_providers/33333333-3333-4333-8333-333333333333/usages"); n != 0 {
		t.Errorf("usages fetched %d times for a provider whose inventories failed, want 0", n)
	}
	// 1 listing + 4 inventories + 3 usages.
	th.AssertEquals(t, int64(8), f.requests.Load())
}

// TestEnrichFromPlacement_ManyRows pushes more rows through than the internal
// semaphore's capacity (8), so the queueing path and the concurrent writes to 20
// distinct row addresses are both exercised under -race.
func TestEnrichFromPlacement_ManyRows(t *testing.T) {
	t.Parallel()
	const n = 20
	f := &plcFleet{}
	rows := make([]hostRow, 0, n)
	for i := range n {
		name := fmt.Sprintf("hv-%02d", i)
		f.providers = append(f.providers, plcProvider{
			uuid:      fmt.Sprintf("%08d-0000-4000-8000-000000000000", i),
			name:      name,
			vcpuTotal: 64, vcpuUsed: 32 + i,
			ramTotal: 1024 * (i + 1), ramUsed: 512 * (i + 1),
			diskTotal: 1000, diskUsed: 10 * i,
		})
		rows = append(rows, novaRow(name))
	}
	pc := f.install(t)

	enrichFromPlacement(context.Background(), pc, rows)

	for i := range rows {
		if rows[i].vcpusTotal != 64 || rows[i].vcpusUsed != 32+i {
			t.Fatalf("rows[%d] (%s) vcpu = %d/%d, want %d/64",
				i, rows[i].name, rows[i].vcpusUsed, rows[i].vcpusTotal, 32+i)
		}
		if rows[i].ramTotalMB != float64(1024*(i+1)) || !nearly(rows[i].ramPct, 50) {
			t.Fatalf("rows[%d] (%s) ram = %v%% of %v", i, rows[i].name, rows[i].ramPct, rows[i].ramTotalMB)
		}
		if rows[i].diskUsedGB != float64(10*i) {
			t.Fatalf("rows[%d] (%s) disk used = %v, want %v", i, rows[i].name, rows[i].diskUsedGB, 10*i)
		}
	}
	// 1 listing + 2 per row.
	th.AssertEquals(t, int64(1+2*n), f.requests.Load())
}

// TestEnrichFromPlacement_ListingFailsIsBestEffort: placement being unreachable
// (or the token lacking the role) must leave the nova-sourced rows untouched
// rather than zeroing them, since the caller ignores the outcome.
func TestEnrichFromPlacement_ListingFails(t *testing.T) {
	t.Parallel()
	f := &plcFleet{listCode: http.StatusForbidden, providers: []plcProvider{
		{uuid: "11111111-1111-4111-8111-111111111111", name: "hv-1", vcpuTotal: 96, vcpuUsed: 1},
	}}
	pc := f.install(t)

	rows := []hostRow{novaRow("hv-1"), novaRow("hv-2")}
	enrichFromPlacement(context.Background(), pc, rows)

	for i := range rows {
		if want := novaRow(rows[i].name); rows[i] != want {
			t.Errorf("rows[%d] changed although the placement listing failed:\n got %+v\nwant %+v", i, rows[i], want)
		}
	}
	th.AssertEquals(t, int64(1), f.requests.Load())
}

// TestEnrichFromPlacement_CancelledContext: with a cancelled context every
// per-provider fetch fails, so the function is a no-op that still terminates
// (the WaitGroup is always released).
func TestEnrichFromPlacement_CancelledContext(t *testing.T) {
	t.Parallel()
	f := &plcFleet{providers: []plcProvider{
		{uuid: "11111111-1111-4111-8111-111111111111", name: "hv-1", vcpuTotal: 96, vcpuUsed: 1, ramTotal: 1024},
		{uuid: "22222222-2222-4222-8222-222222222222", name: "hv-2", vcpuTotal: 96, vcpuUsed: 1, ramTotal: 1024},
		{uuid: "33333333-3333-4333-8333-333333333333", name: "hv-3", vcpuTotal: 96, vcpuUsed: 1, ramTotal: 1024},
	}}
	pc := f.install(t)
	rows := []hostRow{novaRow("hv-1"), novaRow("hv-2"), novaRow("hv-3")}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	enrichFromPlacement(ctx, pc, rows)

	for i := range rows {
		if want := novaRow(rows[i].name); rows[i] != want {
			t.Errorf("rows[%d] changed under a cancelled context:\n got %+v\nwant %+v", i, rows[i], want)
		}
	}
	th.AssertEquals(t, int64(0), f.requests.Load())
}

// TestEnrichFromPlacement_NoRows is the degenerate case: no goroutines, no
// requests beyond the listing.
func TestEnrichFromPlacement_NoRows(t *testing.T) {
	t.Parallel()
	f := &plcFleet{providers: []plcProvider{
		{uuid: "11111111-1111-4111-8111-111111111111", name: "hv-1", vcpuTotal: 8, vcpuUsed: 1},
	}}
	pc := f.install(t)
	enrichFromPlacement(context.Background(), pc, nil)
	th.AssertEquals(t, int64(1), f.requests.Load())
}

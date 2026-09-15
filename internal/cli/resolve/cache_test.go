package resolve

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	th "github.com/gophercloud/gophercloud/v2/testhelper"
)

// projectServer answers Keystone project lookups and counts them.
func projectServer(t *testing.T, calls *int) *gophercloud.ServiceClient {
	t.Helper()
	fakeServer := th.SetupHTTP()
	t.Cleanup(fakeServer.Teardown)
	fakeServer.Mux.HandleFunc("/projects", func(w http.ResponseWriter, r *http.Request) {
		*calls++
		name := r.URL.Query().Get("name")
		domain := r.URL.Query().Get("domain_id")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"projects": []map[string]any{
				{"id": "id-of-" + name + "-in-" + domain, "name": name, "domain_id": domain},
			},
		})
	})
	return projectFakeClient(fakeServer)
}

// emptyProjectServer answers every lookup with no matches.
func emptyProjectServer(t *testing.T, calls *int) *gophercloud.ServiceClient {
	t.Helper()
	fakeServer := th.SetupHTTP()
	t.Cleanup(fakeServer.Teardown)
	fakeServer.Mux.HandleFunc("/projects", func(w http.ResponseWriter, _ *http.Request) {
		*calls++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"projects": []map[string]any{}})
	})
	return projectFakeClient(fakeServer)
}

func TestMemoIsOffUntilEnabled(t *testing.T) {
	ResetCacheForTest()
	var calls int
	sc := projectServer(t, &calls)

	// A one-shot invocation resolves each name once, so the memo buys it
	// nothing and stays out of the way — including out of every command
	// package's tests.
	for range 3 {
		if _, err := ProjectID(context.Background(), sc, "payments"); err != nil {
			t.Fatalf("ProjectID: %v", err)
		}
	}
	if calls != 3 {
		t.Errorf("issued %d lookups, want 3 — the memo must be inert until Enable", calls)
	}
}

func TestMemoCollapsesRepeatedLookups(t *testing.T) {
	ResetCacheForTest()
	Enable()
	t.Cleanup(ResetCacheForTest)

	var calls int
	sc := projectServer(t, &calls)

	// This is the per-tick cost --watch would otherwise pay: a watched
	// `server list --project payments` re-runs the whole command every second.
	for range 5 {
		id, err := ProjectID(context.Background(), sc, "payments")
		if err != nil {
			t.Fatalf("ProjectID: %v", err)
		}
		if id != "id-of-payments-in-" {
			t.Fatalf("resolved to %q", id)
		}
	}
	if calls != 1 {
		t.Errorf("issued %d lookups across five refreshes, want 1", calls)
	}
}

func TestMemoKeepsDomainsApart(t *testing.T) {
	ResetCacheForTest()
	Enable()
	t.Cleanup(ResetCacheForTest)

	var calls int
	sc := projectServer(t, &calls)

	// The same project name in two domains is exactly why --project-domain
	// exists; a memo keyed on the name alone would hand the second lookup the
	// first one's ID.
	a, err := ProjectIDInDomain(context.Background(), sc, "payments", "11111111111111111111111111111111")
	if err != nil {
		t.Fatalf("ProjectIDInDomain: %v", err)
	}
	b, err := ProjectIDInDomain(context.Background(), sc, "payments", "22222222222222222222222222222222")
	if err != nil {
		t.Fatalf("ProjectIDInDomain: %v", err)
	}
	if a == b {
		t.Errorf("both domains resolved to %q", a)
	}
}

func TestMemoExpires(t *testing.T) {
	ResetCacheForTest()
	Enable()
	t.Cleanup(func() {
		cacheNow = time.Now
		ResetCacheForTest()
	})

	now := time.Now()
	cacheNow = func() time.Time { return now }

	var calls int
	sc := projectServer(t, &calls)
	if _, err := ProjectID(context.Background(), sc, "payments"); err != nil {
		t.Fatalf("ProjectID: %v", err)
	}
	// A project can be deleted and recreated under the same name, so an
	// hours-long watch must not hold the first answer forever.
	now = now.Add(cacheTTL + time.Second)
	if _, err := ProjectID(context.Background(), sc, "payments"); err != nil {
		t.Fatalf("ProjectID: %v", err)
	}
	if calls != 2 {
		t.Errorf("issued %d lookups, want 2 — the entry should have expired", calls)
	}
}

func TestMemoDoesNotCacheTheZeroMatchFallback(t *testing.T) {
	ResetCacheForTest()
	Enable()
	t.Cleanup(ResetCacheForTest)

	var calls int
	sc := emptyProjectServer(t, &calls)

	// Zero matches falls back to treating the reference as an opaque ID. That
	// is a guess, not a resolution: caching it would mean a watch started
	// before the project exists stays wrong for five minutes after it appears.
	for range 3 {
		if _, err := ProjectID(context.Background(), sc, "not-yet"); err != nil {
			t.Fatalf("ProjectID: %v", err)
		}
	}
	if calls != 3 {
		t.Errorf("issued %d lookups, want 3 — the fallback must not be memoized", calls)
	}
}

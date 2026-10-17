package resolve_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	th "github.com/gophercloud/gophercloud/v2/testhelper"
	fakeclient "github.com/gophercloud/gophercloud/v2/testhelper/client"

	"github.com/ftarasenko/go-openstackclient/pkg/openstack"
	"github.com/ftarasenko/go-openstackclient/pkg/openstack/resolve"
)

// listing serves GET /<collection>?name=… from names, a map of name to the
// IDs carrying it, and counts the lookups.
func listing(t *testing.T, collection string, names map[string][]string, calls *atomic.Int64) *gophercloud.ServiceClient {
	t.Helper()
	fakeServer := th.SetupHTTP()
	t.Cleanup(fakeServer.Teardown)
	fakeServer.Mux.HandleFunc("/"+collection, func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		calls.Add(1)
		name := r.URL.Query().Get("name")
		items := []map[string]string{}
		for _, id := range names[name] {
			items = append(items, map[string]string{"id": id, "name": name})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{collection: items})
	})
	return fakeclient.ServiceClient(fakeServer)
}

func TestStrictPolicy(t *testing.T) {
	var calls atomic.Int64
	sc := listing(t, "images", map[string][]string{
		"ubuntu": {"img-1"},
		"twice":  {"img-2", "img-3"},
	}, &calls)
	ctx := context.Background()

	if id, err := resolve.ImageID(ctx, sc, "ubuntu"); err != nil || id != "img-1" {
		t.Errorf("one match: got %q, %v", id, err)
	}

	_, err := resolve.ImageID(ctx, sc, "missing")
	var nf *resolve.NotFoundError
	if !errors.As(err, &nf) || nf.Kind != "image" || nf.Ref != "missing" {
		t.Fatalf("no match: err = %v (%T), want a *NotFoundError", err, err)
	}
	if !errors.Is(err, openstack.ErrNotFound) {
		t.Error("a NotFoundError must match openstack.ErrNotFound")
	}
	if got, want := err.Error(), `no image found for "missing"`; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}

	_, err = resolve.ImageID(ctx, sc, "twice")
	var amb *resolve.AmbiguousError
	if !errors.As(err, &amb) || amb.Count != 2 {
		t.Fatalf("two matches: err = %v (%T), want an *AmbiguousError", err, err)
	}
	if got, want := err.Error(), `multiple images named "twice"; specify an ID instead`; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}

	calls.Store(0)
	for _, ref := range []string{"", "11111111-2222-4333-8444-555555555555", "5f0f658d5bc34e1fb3fb0862a79489a4"} {
		if id, err := resolve.ImageID(ctx, sc, ref); err != nil || id != ref {
			t.Errorf("%q: got %q, %v; want it passed through", ref, id, err)
		}
	}
	if calls.Load() != 0 {
		t.Errorf("an empty or UUID reference made %d lookups", calls.Load())
	}
}

func TestLookupErrorNamesTheLookup(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/networks", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	_, err := resolve.NetworkID(context.Background(), fakeclient.ServiceClient(fakeServer), "private")
	if err == nil || !strings.HasPrefix(err.Error(), `looking up network "private": `) {
		t.Errorf("err = %v", err)
	}
	if errors.Is(err, openstack.ErrNotFound) {
		t.Error("a failed lookup is not a not-found")
	}
}

func TestServerID_ExactNameOnly(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/servers", func(w http.ResponseWriter, r *http.Request) {
		th.TestFormValues(t, r, map[string]string{"name": "web", "all_tenants": "true"})
		w.Header().Set("Content-Type", "application/json")
		// nova's name filter is a regex: "web" also matches "web-2".
		_, _ = w.Write([]byte(`{"servers":[{"id":"s-1","name":"web"},{"id":"s-2","name":"web-2"}]}`))
	})
	id, err := resolve.ServerID(context.Background(), fakeclient.ServiceClient(fakeServer), "web")
	if err != nil || id != "s-1" {
		t.Errorf("got %q, %v; want the exact match s-1", id, err)
	}
}

func TestProjectIDInDomain_StrictDomain(t *testing.T) {
	var calls atomic.Int64
	sc := listing(t, "domains", map[string][]string{}, &calls)
	_, err := resolve.ProjectIDInDomain(context.Background(), sc, "payments", "nowhere")
	var nf *resolve.NotFoundError
	if !errors.As(err, &nf) || nf.Kind != "domain" {
		t.Errorf("err = %v; an unknown domain is a not-found for the domain", err)
	}
}

// regionalImages is one Session whose image catalog entry differs by region,
// each region answering the same image name with its own ID.
func regionalImages(t *testing.T, calls map[string]*atomic.Int64) *openstack.Session {
	t.Helper()
	urls := map[string]string{}
	for region, n := range calls {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"images": []map[string]string{
				{"id": "img-in-" + region, "name": r.URL.Query().Get("name")},
			}})
		}))
		t.Cleanup(srv.Close)
		urls[region] = srv.URL + "/"
	}
	pc := &gophercloud.ProviderClient{TokenID: fakeclient.TokenID, EndpointLocator: func(eo gophercloud.EndpointOpts) (string, error) {
		return urls[eo.Region], nil
	}}
	return openstack.FromProvider(pc, gophercloud.EndpointOpts{Region: "RegionOne"}, openstack.Options{})
}

func TestCache_KeepsRegionsApart(t *testing.T) {
	calls := map[string]*atomic.Int64{"RegionOne": {}, "RegionTwo": {}}
	s := regionalImages(t, calls)
	r := resolve.Resolver{Cache: resolve.NewCache(time.Minute)}
	ctx := context.Background()

	got := map[string]string{}
	for range 3 {
		for _, sess := range []*openstack.Session{s, s.ForRegion("RegionTwo")} {
			sc, err := sess.Image()
			if err != nil {
				t.Fatal(err)
			}
			id, err := r.ImageID(ctx, sc, "ubuntu")
			if err != nil {
				t.Fatal(err)
			}
			got[sess.Region()] = id
		}
	}
	if got["RegionOne"] != "img-in-RegionOne" || got["RegionTwo"] != "img-in-RegionTwo" {
		t.Errorf("resolved %v; one region was handed the other's answer", got)
	}
	for region, n := range calls {
		if n.Load() != 1 {
			t.Errorf("%s: %d lookups across three rounds, want 1", region, n.Load())
		}
	}
}

func TestCache_ExpiresAndSkipsMisses(t *testing.T) {
	var calls atomic.Int64
	sc := listing(t, "projects", map[string][]string{"payments": {"p-1"}}, &calls)
	now := time.Now()
	cache := resolve.NewCache(time.Minute, resolve.WithClock(func() time.Time { return now }))
	r := resolve.Resolver{Cache: cache}
	ctx := context.Background()

	for range 3 {
		if _, err := r.ProjectID(ctx, sc, "payments"); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("%d lookups, want 1 while the entry is fresh", calls.Load())
	}
	now = now.Add(time.Minute + time.Second)
	if _, err := r.ProjectID(ctx, sc, "payments"); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Errorf("%d lookups, want 2 after the entry expired", calls.Load())
	}
	cache.Purge()
	if _, err := r.ProjectID(ctx, sc, "payments"); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Errorf("%d lookups, want 3 after Purge", calls.Load())
	}

	// A miss is asked again every time: a project created after the first
	// attempt must be found by the next one.
	calls.Store(0)
	for range 2 {
		if _, err := r.ProjectID(ctx, sc, "not-yet"); err == nil {
			t.Fatal("want a not-found")
		}
	}
	if calls.Load() != 2 {
		t.Errorf("%d lookups for a miss, want 2: misses must not be cached", calls.Load())
	}
}

func TestCache_KeepsDomainsApart(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/projects", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"projects": []map[string]string{
			{"id": "p-in-" + q.Get("domain_id"), "name": q.Get("name")},
		}})
	})
	sc := fakeclient.ServiceClient(fakeServer)
	r := resolve.Resolver{Cache: resolve.NewCache(time.Minute)}
	a, err := r.ProjectIDInDomainID(context.Background(), sc, "payments", "11111111111111111111111111111111")
	if err != nil {
		t.Fatal(err)
	}
	b, err := r.ProjectIDInDomainID(context.Background(), sc, "payments", "22222222222222222222222222222222")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Errorf("both domains resolved to %q", a)
	}
}

func TestNilCacheCachesNothing(t *testing.T) {
	var calls atomic.Int64
	sc := listing(t, "users", map[string][]string{"alice": {"u-1"}}, &calls)
	var nilCache *resolve.Cache
	nilCache.Purge()
	r := resolve.Resolver{Cache: nilCache}
	for range 2 {
		if _, err := r.UserID(context.Background(), sc, "alice"); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 2 {
		t.Errorf("%d lookups, want 2 with no cache", calls.Load())
	}
}

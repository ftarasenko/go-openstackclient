package paging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/pagination"
	th "github.com/gophercloud/gophercloud/v2/testhelper"
	fakeclient "github.com/gophercloud/gophercloud/v2/testhelper/client"
)

// item is a minimal paginated resource; only its name matters to these tests.
type item struct {
	Name string `json:"name"`
}

// itemPage is a link-paginated page, the shape every OpenStack list koc calls
// through paging.Collect uses (nova servers, cinder volumes, neutron ports, …).
type itemPage struct {
	pagination.LinkedPageBase
}

func (p itemPage) IsEmpty() (bool, error) {
	items, err := extractItems(p)
	return len(items) == 0, err
}

func extractItems(page pagination.Page) ([]item, error) {
	var s struct {
		Items []item `json:"items"`
	}
	ip, ok := page.(itemPage)
	if !ok {
		return nil, fmt.Errorf("unexpected page type %T", page)
	}
	b, err := json.Marshal(ip.Body)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	return s.Items, nil
}

// fakeCollection is a paginated endpoint that counts the requests it served.
//
// alwaysNext models the (common) server that hands out a "next" link on every
// page including the last full one, so a client that pages to exhaustion must
// fetch one extra, empty page to learn it is done. That extra request is what
// the regression in Collect's doc comment was about.
type fakeCollection struct {
	total      int
	pageSize   int
	alwaysNext bool

	requests atomic.Int64
	failFrom int  // 1-based request number that starts returning 500 (0 = never)
	badJSON  bool // serve a body that cannot be extracted
}

func (c *fakeCollection) install(t *testing.T) (*gophercloud.ServiceClient, string) {
	t.Helper()
	fakeServer := th.SetupHTTP()
	t.Cleanup(fakeServer.Teardown)

	base := fakeServer.Endpoint() + "items"
	fakeServer.Mux.HandleFunc("/items", func(w http.ResponseWriter, r *http.Request) {
		n := int(c.requests.Add(1))
		th.AssertEquals(t, http.MethodGet, r.Method)
		if c.failFrom > 0 && n >= c.failFrom {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if c.badJSON {
			_, _ = fmt.Fprint(w, `{"items": {"not": "a list"}}`)
			return
		}
		start, _ := strconv.Atoi(r.URL.Query().Get("start"))
		if start < 0 {
			start = 0
		}
		end := start + c.pageSize
		if end > c.total {
			end = c.total
		}
		items := make([]item, 0, c.pageSize)
		for i := start; i < end; i++ {
			items = append(items, item{Name: fmt.Sprintf("item-%02d", i)})
		}
		body := map[string]any{"items": items}
		// A "next" link is offered while there is more to read; with alwaysNext it
		// is also offered on the final non-empty page, so exhaustive paging costs
		// one extra request.
		if end < c.total || (c.alwaysNext && len(items) > 0) {
			body["links"] = map[string]any{"next": fmt.Sprintf("%s?start=%d", base, end)}
		}
		_ = json.NewEncoder(w).Encode(body)
	})

	return fakeclient.ServiceClient(fakeServer), base
}

func (c *fakeCollection) pager(client *gophercloud.ServiceClient, url string) pagination.Pager {
	return pagination.NewPager(client, url, func(r pagination.PageResult) pagination.Page {
		return itemPage{pagination.LinkedPageBase{PageResult: r}}
	})
}

func names(items []item) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Name
	}
	return out
}

// TestCollect pins the request count as tightly as the result: bounding requests
// by --limit rather than by collection size is the whole point of Collect (see
// its doc comment — "server list --limit 1" once issued 35 requests against a
// 34-server project).
func TestCollect(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		total        int
		pageSize     int
		alwaysNext   bool
		limit        int
		wantCount    int
		wantFirst    string
		wantLast     string
		wantRequests int64
	}{{
		// limit unset: page to exhaustion. The server withholds "next" on the
		// last page, so three requests cover 25 items.
		name: "limit unset collects everything", total: 25, pageSize: 10,
		limit: 0, wantCount: 25, wantFirst: "item-00", wantLast: "item-24", wantRequests: 3,
	}, {
		// Same, against a server that always offers "next": exhaustion costs the
		// extra empty page. This is the baseline the --limit cases improve on.
		name:  "limit unset on an always-next server reads the trailing empty page",
		total: 25, pageSize: 10, alwaysNext: true,
		limit: 0, wantCount: 25, wantFirst: "item-00", wantLast: "item-24", wantRequests: 4,
	}, {
		name: "negative limit is unlimited", total: 25, pageSize: 10,
		limit: -1, wantCount: 25, wantFirst: "item-00", wantLast: "item-24", wantRequests: 3,
	}, {
		// The regression case: one row must cost one request, not one per page.
		name:  "limit smaller than a page stops after the first page",
		total: 25, pageSize: 10, alwaysNext: true,
		limit: 1, wantCount: 1, wantFirst: "item-00", wantLast: "item-00", wantRequests: 1,
	}, {
		// Boundary at paging.go:33 — len(all) < limit is false at exactly limit,
		// so a cap equal to the page size must NOT fetch a second page.
		name:  "limit equal to the page size stops after the first page",
		total: 25, pageSize: 10, alwaysNext: true,
		limit: 10, wantCount: 10, wantFirst: "item-00", wantLast: "item-09", wantRequests: 1,
	}, {
		name:  "limit one over the page size reads a second page",
		total: 25, pageSize: 10, alwaysNext: true,
		limit: 11, wantCount: 11, wantFirst: "item-00", wantLast: "item-10", wantRequests: 2,
	}, {
		// Overshoot: the second page brings 20 items and the cap trims to 15
		// (paging.go:38), without touching the third page.
		name:  "limit mid-page truncates the overshoot",
		total: 25, pageSize: 10, alwaysNext: true,
		limit: 15, wantCount: 15, wantFirst: "item-00", wantLast: "item-14", wantRequests: 2,
	}, {
		// limit == total on an always-next server: stop on the cap instead of
		// paying for the empty page (3 requests, not 4).
		name:  "limit equal to the total stops before the empty page",
		total: 25, pageSize: 10, alwaysNext: true,
		limit: 25, wantCount: 25, wantFirst: "item-00", wantLast: "item-24", wantRequests: 3,
	}, {
		// limit exactly on a page boundary that is also the total.
		name:  "limit equal to a total that lands on a page boundary",
		total: 20, pageSize: 10, alwaysNext: true,
		limit: 20, wantCount: 20, wantFirst: "item-00", wantLast: "item-19", wantRequests: 2,
	}, {
		// A cap above the total cannot short-circuit anything: every page is read
		// and the always-next server still charges for the empty one.
		name:  "limit larger than the total reads everything",
		total: 25, pageSize: 10, alwaysNext: true,
		limit: 100, wantCount: 25, wantFirst: "item-00", wantLast: "item-24", wantRequests: 4,
	}, {
		name:  "limit larger than the total on a well-behaved server",
		total: 25, pageSize: 10,
		limit: 100, wantCount: 25, wantFirst: "item-00", wantLast: "item-24", wantRequests: 3,
	}, {
		name: "single short page under the limit", total: 3, pageSize: 10,
		limit: 5, wantCount: 3, wantFirst: "item-00", wantLast: "item-02", wantRequests: 1,
	}, {
		name: "single empty page", total: 0, pageSize: 10,
		limit: 0, wantCount: 0, wantRequests: 1,
	}, {
		name: "single empty page with a limit", total: 0, pageSize: 10,
		limit: 5, wantCount: 0, wantRequests: 1,
	}}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := &fakeCollection{total: tc.total, pageSize: tc.pageSize, alwaysNext: tc.alwaysNext}
			client, url := c.install(t)

			got, err := Collect(context.Background(), c.pager(client, url), tc.limit, extractItems)
			th.AssertNoErr(t, err)

			if len(got) != tc.wantCount {
				t.Fatalf("collected %d items (%v), want %d", len(got), names(got), tc.wantCount)
			}
			if tc.wantCount > 0 {
				th.AssertEquals(t, tc.wantFirst, got[0].Name)
				th.AssertEquals(t, tc.wantLast, got[len(got)-1].Name)
			}
			if n := c.requests.Load(); n != tc.wantRequests {
				t.Errorf("issued %d requests, want %d (a --limit must bound the request count, not just the result)", n, tc.wantRequests)
			}
		})
	}
}

// TestCollect_EmptyPageYieldsNilSlice documents that an empty collection comes
// back as a nil slice rather than an empty one — callers render it as "no rows".
func TestCollect_EmptyPageYieldsNilSlice(t *testing.T) {
	t.Parallel()
	c := &fakeCollection{total: 0, pageSize: 10}
	client, url := c.install(t)

	got, err := Collect(context.Background(), c.pager(client, url), 0, extractItems)
	th.AssertNoErr(t, err)
	if got != nil {
		t.Fatalf("want nil slice for an empty collection, got %#v", got)
	}
}

// TestCollect_ClientSideFilterKeepsPaging is the contract in the second half of
// Collect's doc comment, modelled on the real caller at
// internal/cli/volume/volume.go:179 ("--type" filters client-side, so the cap is
// disabled): passing the user's --limit as the collect cap short-reads the
// collection and the client-side filter then returns fewer rows than were asked
// for. Collecting with cap 0 and truncating after the filter returns the right
// number.
func TestCollect_ClientSideFilterKeepsPaging(t *testing.T) {
	t.Parallel()

	// 25 items, every fifth of which survives the filter → 5 matches total, but
	// only 2 of them live in the first page (and only 1 survives a cap of 3).
	keep := func(items []item) []item {
		var out []item
		for _, it := range items {
			n, err := strconv.Atoi(it.Name[len("item-"):])
			if err == nil && n%5 == 0 {
				out = append(out, it)
			}
		}
		return out
	}
	const userLimit = 3

	t.Run("cap passed through short-reads (why the cap must be disabled)", func(t *testing.T) {
		t.Parallel()
		c := &fakeCollection{total: 25, pageSize: 10, alwaysNext: true}
		client, url := c.install(t)

		all, err := Collect(context.Background(), c.pager(client, url), userLimit, extractItems)
		th.AssertNoErr(t, err)
		filtered := keep(all)
		if len(filtered) >= userLimit {
			t.Fatalf("expected the short read to under-fill the filter, got %v", names(filtered))
		}
		// The cap both stopped paging after one page AND truncated that page to 3
		// items, so exactly one match ("item-00") survives instead of three.
		th.AssertEquals(t, 1, len(filtered))
		th.AssertEquals(t, int64(1), c.requests.Load())
	})

	t.Run("cap disabled keeps paging and fills the limit", func(t *testing.T) {
		t.Parallel()
		c := &fakeCollection{total: 25, pageSize: 10, alwaysNext: true}
		client, url := c.install(t)

		all, err := Collect(context.Background(), c.pager(client, url), 0, extractItems)
		th.AssertNoErr(t, err)
		filtered := keep(all)
		if len(filtered) > userLimit {
			filtered = filtered[:userLimit]
		}
		th.AssertEquals(t, userLimit, len(filtered))
		th.AssertEquals(t, "item-00", filtered[0].Name)
		th.AssertEquals(t, "item-10", filtered[2].Name)
		// Every page, including the always-next server's trailing empty one.
		th.AssertEquals(t, int64(4), c.requests.Load())
	})
}

func TestCollect_ExtractError(t *testing.T) {
	t.Parallel()
	c := &fakeCollection{total: 25, pageSize: 10}
	client, url := c.install(t)

	sentinel := errors.New("boom")
	got, err := Collect(context.Background(), c.pager(client, url), 0,
		func(pagination.Page) ([]item, error) { return nil, sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want %v", err, sentinel)
	}
	if got != nil {
		t.Errorf("want no items on an extract error, got %v", names(got))
	}
	th.AssertEquals(t, int64(1), c.requests.Load())
}

// TestCollect_ExtractErrorOnSecondPage shows that a mid-collection extract
// failure discards the pages already gathered rather than returning a partial
// result.
func TestCollect_ExtractErrorOnSecondPage(t *testing.T) {
	t.Parallel()
	c := &fakeCollection{total: 25, pageSize: 10}
	client, url := c.install(t)

	sentinel := errors.New("boom")
	var calls int
	got, err := Collect(context.Background(), c.pager(client, url), 0,
		func(p pagination.Page) ([]item, error) {
			calls++
			if calls > 1 {
				return nil, sentinel
			}
			return extractItems(p)
		})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want %v", err, sentinel)
	}
	if got != nil {
		t.Errorf("want no items on an extract error, got %v", names(got))
	}
}

// TestCollect_MalformedPage covers a body that extracts to nothing usable: the
// error surfaces (from the page's own IsEmpty) instead of a silent empty list.
func TestCollect_MalformedPage(t *testing.T) {
	t.Parallel()
	c := &fakeCollection{total: 25, pageSize: 10, badJSON: true}
	client, url := c.install(t)

	got, err := Collect(context.Background(), c.pager(client, url), 0, extractItems)
	if err == nil {
		t.Fatalf("want an error for a malformed page, got %v", names(got))
	}
	if got != nil {
		t.Errorf("want no items for a malformed page, got %v", names(got))
	}
}

func TestCollect_RequestErrorOnSecondPage(t *testing.T) {
	t.Parallel()
	c := &fakeCollection{total: 25, pageSize: 10, failFrom: 2}
	client, url := c.install(t)

	got, err := Collect(context.Background(), c.pager(client, url), 0, extractItems)
	if err == nil {
		t.Fatalf("want an error from the failing page, got %v", names(got))
	}
	if got != nil {
		t.Errorf("want no items on a request error, got %v", names(got))
	}
	th.AssertEquals(t, int64(2), c.requests.Load())
}

// TestCollect_CapPreventsTheFailingPage: a cap satisfied by page one never
// reaches the page that would have failed.
func TestCollect_CapPreventsTheFailingPage(t *testing.T) {
	t.Parallel()
	c := &fakeCollection{total: 25, pageSize: 10, alwaysNext: true, failFrom: 2}
	client, url := c.install(t)

	got, err := Collect(context.Background(), c.pager(client, url), 10, extractItems)
	th.AssertNoErr(t, err)
	th.AssertEquals(t, 10, len(got))
	th.AssertEquals(t, int64(1), c.requests.Load())
}

func TestCollect_CancelledContext(t *testing.T) {
	t.Parallel()
	c := &fakeCollection{total: 25, pageSize: 10}
	client, url := c.install(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := Collect(ctx, c.pager(client, url), 0, extractItems)
	if err == nil {
		t.Fatalf("want an error for a cancelled context, got %v", names(got))
	}
	if got != nil {
		t.Errorf("want no items for a cancelled context, got %v", names(got))
	}
	th.AssertEquals(t, int64(0), c.requests.Load())
}

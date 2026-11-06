package compute_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/quotasets"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/usage"

	"github.com/ftarasenko/koc/pkg/openstack"
	"github.com/ftarasenko/koc/pkg/openstack/compute"
)

// novaRoutes is a fake nova that answers "METHOD /path" from routes, 404
// otherwise, and records every request.
func novaRoutes(t *testing.T, routes map[string]func(*http.Request) (int, string)) (*calls, *gophercloud.ServiceClient) {
	t.Helper()
	fs, sc := nova(t, "2.93")
	c := &calls{}
	fs.Mux.HandleFunc("/", c.handler(func(r *http.Request, _ string) (int, string) {
		if reply, ok := routes[r.Method+" "+r.URL.Path]; ok {
			return reply(r)
		}
		return http.StatusNotFound, `{"itemNotFound":{"code":404,"message":"not found"}}`
	}))
	return c, sc
}

func body(status int, s string) func(*http.Request) (int, string) {
	return func(*http.Request) (int, string) { return status, s }
}

func TestQuotaSets(t *testing.T) {
	c, sc := novaRoutes(t, map[string]func(*http.Request) (int, string){
		"GET /os-quota-sets/" + projectID:               body(200, `{"quota_set":{"id":"`+projectID+`","cores":20}}`),
		"GET /os-quota-sets/" + projectID + "/defaults": body(200, `{"quota_set":{"cores":10}}`),
		"PUT /os-quota-sets/" + projectID:               body(200, `{"quota_set":{"cores":8}}`),
		"GET /os-quota-sets/empty":                      body(200, `{}`),
		"GET /os-quota-sets/denied/defaults":            body(403, `{"forbidden":{"code":403,"message":"no"}}`),
	})
	ctx := context.Background()

	if qs, err := compute.GetQuotaSet(ctx, sc, projectID); err != nil || qs.Cores != 20 {
		t.Fatalf("GetQuotaSet = %+v, %v", qs, err)
	}
	if qs, err := compute.GetDefaultQuotaSet(ctx, sc, projectID); err != nil || qs.Cores != 10 {
		t.Fatalf("GetDefaultQuotaSet = %+v, %v", qs, err)
	}
	cores := 8
	if qs, err := compute.UpdateQuotaSet(ctx, sc, projectID, quotasets.UpdateOpts{Cores: &cores}); err != nil || qs.Cores != 8 {
		t.Fatalf("UpdateQuotaSet = %+v, %v", qs, err)
	}
	c.take()

	_, err := compute.GetQuotaSet(ctx, sc, "empty")
	if !errors.Is(err, openstack.ErrNoObject) ||
		err.Error() != `showing compute quotas for project "empty": the server returned success but no object` {
		t.Errorf("empty reply: %v", err)
	}
	_, err = compute.GetDefaultQuotaSet(ctx, sc, "denied")
	if !gophercloud.ResponseCodeIs(err, http.StatusForbidden) {
		t.Errorf("denied defaults: %v; want the 403 reachable", err)
	}
	if want := `showing default compute quotas for project "denied": `; err == nil || err.Error()[:len(want)] != want {
		t.Errorf("denied defaults: %v", err)
	}
	cores = 1
	if _, err := compute.UpdateQuotaSet(ctx, sc, "nope", quotasets.UpdateOpts{Cores: &cores}); err == nil ||
		err.Error()[:len(`setting compute quotas for project "nope": `)] != `setting compute quotas for project "nope": ` {
		t.Errorf("update of an unknown project: %v", err)
	}
}

func TestQuotaSets_UpdateBody(t *testing.T) {
	c, sc := novaRoutes(t, map[string]func(*http.Request) (int, string){
		"PUT /os-quota-sets/" + projectID: body(200, `{"quota_set":{"cores":8}}`),
	})
	cores, ram := 8, 0
	if _, err := compute.UpdateQuotaSet(context.Background(), sc, projectID, quotasets.UpdateOpts{Cores: &cores, RAM: &ram}); err != nil {
		t.Fatal(err)
	}
	if got := c.only(t); got.Body != `{"quota_set":{"cores":8,"ram":0}}` {
		t.Errorf("body = %s; want only the given quotas, an explicit 0 included", got.Body)
	}
}

func TestGetLimits(t *testing.T) {
	var query string
	_, sc := novaRoutes(t, map[string]func(*http.Request) (int, string){
		"GET /limits": func(r *http.Request) (int, string) {
			query = r.URL.RawQuery
			if r.URL.Query().Get("tenant_id") == "empty" {
				return 200, `{}`
			}
			return 200, `{"limits":{"absolute":{"maxTotalCores":20,"totalCoresUsed":4},"rate":[]}}`
		},
	})
	ctx := context.Background()

	l, err := compute.GetLimits(ctx, sc, "")
	if err != nil || l.Absolute.MaxTotalCores != 20 || query != "" {
		t.Fatalf("GetLimits = %+v, %v (query %q)", l, err, query)
	}
	if _, err := compute.GetLimits(ctx, sc, projectID); err != nil || query != "tenant_id="+projectID {
		t.Fatalf("GetLimits(project) = %v, query %q", err, query)
	}
	if _, err := compute.GetLimits(ctx, sc, "empty"); !errors.Is(err, openstack.ErrNoObject) ||
		err.Error() != "getting compute limits: the server returned success but no object" {
		t.Errorf("empty reply: %v", err)
	}
}

func TestListAvailabilityZones(t *testing.T) {
	zones := `{"availabilityZoneInfo":[{"zoneName":"nova","zoneState":{"available":true},"hosts":null}]}`
	detail := http.StatusOK
	c, sc := novaRoutes(t, map[string]func(*http.Request) (int, string){
		"GET /os-availability-zone/detail": func(*http.Request) (int, string) { return detail, zones },
		"GET /os-availability-zone":        body(200, zones),
	})
	ctx := context.Background()

	if got, err := compute.ListAvailabilityZones(ctx, sc); err != nil || len(got) != 1 || got[0].ZoneName != "nova" {
		t.Fatalf("ListAvailabilityZones = %+v, %v", got, err)
	}
	if got := c.only(t); got.Path != "/os-availability-zone/detail" {
		t.Errorf("path = %s; want the detail listing first", got.Path)
	}

	detail = http.StatusForbidden
	if got, err := compute.ListAvailabilityZones(ctx, sc); err != nil || len(got) != 1 {
		t.Fatalf("after a 403 = %+v, %v", got, err)
	}
	if got := c.take(); len(got) != 2 || got[1].Path != "/os-availability-zone" {
		t.Errorf("requests = %+v; want the detail listing, then the plain one", got)
	}

	detail = http.StatusInternalServerError
	_, err := compute.ListAvailabilityZones(ctx, sc)
	if !gophercloud.ResponseCodeIs(err, http.StatusInternalServerError) {
		t.Errorf("a 500 = %v; want it returned, not retried", err)
	}
	if want := "listing compute availability zones: "; err == nil || err.Error()[:len(want)] != want {
		t.Errorf("a 500 = %v", err)
	}
}

func TestUsage(t *testing.T) {
	var query string
	c, sc := novaRoutes(t, map[string]func(*http.Request) (int, string){
		"GET /os-simple-tenant-usage": func(r *http.Request) (int, string) {
			query = r.URL.RawQuery
			return 200, `{"tenant_usages":[{"tenant_id":"a","total_hours":1},{"tenant_id":"b"}]}`
		},
		"GET /os-simple-tenant-usage/" + projectID: func(r *http.Request) (int, string) {
			if r.URL.Query().Get("marker") == "" {
				return 200, `{"tenant_usage":{"tenant_id":"` + projectID + `","total_hours":2,"server_usages":[{"name":"one"}]},
					"tenant_usage_links":[{"rel":"next","href":"http://` + r.Host + `/os-simple-tenant-usage/` + projectID + `?marker=one"}]}`
			}
			return 200, `{"tenant_usage":{"tenant_id":"` + projectID + `","total_hours":9,"server_usages":[{"name":"two"}]}}`
		},
		"GET /os-simple-tenant-usage/idle":   body(200, `{}`),
		"GET /os-simple-tenant-usage/broken": body(500, `{"computeFault":{"code":500,"message":"boom"}}`),
	})
	ctx := context.Background()
	start := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 7)

	all, err := compute.ListUsage(ctx, sc, usage.AllTenantsOpts{Start: &start, End: &end, Detailed: true})
	if err != nil || len(all) != 2 || all[0].TenantID != "a" {
		t.Fatalf("ListUsage = %+v, %v", all, err)
	}
	if query != "detailed=1&end=2026-01-09T00%3A00%3A00&start=2026-01-02T00%3A00%3A00" {
		t.Errorf("query = %q", query)
	}
	c.take()

	u, err := compute.GetProjectUsage(ctx, sc, projectID, usage.SingleTenantOpts{Start: &start, End: &end})
	if err != nil || u.TenantID != projectID || u.TotalHours != 2 || len(u.ServerUsages) != 2 ||
		u.ServerUsages[1].Name != "two" {
		t.Fatalf("GetProjectUsage = %+v, %v; want both pages' servers and the first page's totals", u, err)
	}
	if got := c.take(); len(got) != 2 {
		t.Errorf("requests = %d; want two pages", len(got))
	}

	if u, err := compute.GetProjectUsage(ctx, sc, "idle", usage.SingleTenantOpts{}); err != nil || u != nil {
		t.Errorf("no usage = %+v, %v; want nil, nil", u, err)
	}
	_, err = compute.GetProjectUsage(ctx, sc, "broken", usage.SingleTenantOpts{})
	if want := "getting compute usage for project broken: "; err == nil || err.Error()[:len(want)] != want {
		t.Errorf("a 500 = %v", err)
	}
	if _, err := compute.ListUsage(ctx, sc, badUsageQuery{}); err == nil {
		t.Error("a bad query built no error")
	}
}

type badUsageQuery struct{}

func (badUsageQuery) ToUsageAllTenantsQuery() (string, error) { return "", errors.New("bad") }

// take returns the requests served so far and forgets them.
func (c *calls) take() []call {
	c.mu.Lock()
	defer c.mu.Unlock()
	got := c.seen
	c.seen = nil
	return got
}

package loadbalancer_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/loadbalancer/v2/amphorae"
	"github.com/gophercloud/gophercloud/v2/openstack/loadbalancer/v2/flavorprofiles"
	"github.com/gophercloud/gophercloud/v2/openstack/loadbalancer/v2/flavors"
	"github.com/gophercloud/gophercloud/v2/openstack/loadbalancer/v2/l7policies"
	"github.com/gophercloud/gophercloud/v2/openstack/loadbalancer/v2/listeners"
	"github.com/gophercloud/gophercloud/v2/openstack/loadbalancer/v2/loadbalancers"
	"github.com/gophercloud/gophercloud/v2/openstack/loadbalancer/v2/monitors"
	"github.com/gophercloud/gophercloud/v2/openstack/loadbalancer/v2/pools"
	"github.com/gophercloud/gophercloud/v2/openstack/loadbalancer/v2/providers"
	"github.com/gophercloud/gophercloud/v2/openstack/loadbalancer/v2/quotas"
	th "github.com/gophercloud/gophercloud/v2/testhelper"
	fakeclient "github.com/gophercloud/gophercloud/v2/testhelper/client"

	"github.com/ftarasenko/go-openstackclient/pkg/openstack/loadbalancer"
	"github.com/ftarasenko/go-openstackclient/pkg/openstack/resolve"
)

const (
	lbID     = "1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d"
	lsnID    = "2b3c4d5e-6f7a-4b8c-9d0e-1f2a3b4c5d6e"
	poolID   = "3c4d5e6f-7a8b-4c9d-8e1f-2a3b4c5d6e7f"
	memberID = "4d5e6f7a-8b9c-4d0e-9f2a-3b4c5d6e7f8a"
	hmID     = "5e6f7a8b-9c0d-4e1f-8a3b-4c5d6e7f8a9b"
	l7ID     = "6f7a8b9c-0d1e-4f2a-9b4c-5d6e7f8a9b0c"
	ruleID   = "7a8b9c0d-1e2f-4a3b-8c5d-6e7f8a9b0c1d"
	flvID    = "8b9c0d1e-2f3a-4b4c-9d6e-7f8a9b0c1d2e"
	fpID     = "9c0d1e2f-3a4b-4c5d-8e7f-8a9b0c1d2e3f"
	amphID   = "0d1e2f3a-4b5c-4d6e-9f8a-9b0c1d2e3f4a"
	project  = "5f0c1d2e3a4b4c5d8e9f0a1b2c3d4e5f"
)

type req struct{ Method, Path, Query, Body string }

type reply struct {
	Status int
	Body   string
}

// fakeOctavia answers "METHOD /path?query" or "METHOD /path" from routes, an
// octavia 404 otherwise, and records every request with its body compacted.
func fakeOctavia(t *testing.T, routes map[string]reply) (*[]req, *gophercloud.ServiceClient) {
	t.Helper()
	fs := th.SetupHTTP()
	t.Cleanup(fs.Teardown)
	var seen []req
	fs.Mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body := string(raw)
		var v any
		if json.Unmarshal(raw, &v) == nil {
			b, _ := json.Marshal(v)
			body = string(b)
		}
		seen = append(seen, req{r.Method, r.URL.Path, r.URL.RawQuery, body})
		w.Header().Set("Content-Type", "application/json")
		key := r.Method + " " + r.URL.Path
		out, ok := routes[key+"?"+r.URL.RawQuery]
		if !ok {
			out, ok = routes[key]
		}
		if !ok {
			out = reply{http.StatusNotFound, `{"faultcode":"Client","faultstring":"not found","debuginfo":null}`}
		}
		if out.Status == 0 {
			out.Status = http.StatusOK
		}
		w.WriteHeader(out.Status)
		_, _ = io.WriteString(w, out.Body)
	})
	sc := fakeclient.ServiceClient(fs)
	sc.Type = "load-balancer"
	sc.ResourceBase = sc.Endpoint + "v2.0/"
	return &seen, sc
}

func last(t *testing.T, seen *[]req) req {
	t.Helper()
	if len(*seen) == 0 {
		t.Fatal("no request was sent")
	}
	return (*seen)[len(*seen)-1]
}

func want(t *testing.T, got req, method, path, query, body string) {
	t.Helper()
	if got.Method != method || got.Path != path || got.Query != query || (body != "" && got.Body != body) {
		t.Errorf("request = %+v; want %s %s?%s %s", got, method, path, query, body)
	}
}

func is404(t *testing.T, err error, prefix string) {
	t.Helper()
	if !gophercloud.ResponseCodeIs(err, http.StatusNotFound) || !strings.HasPrefix(err.Error(), prefix) {
		t.Errorf("error = %v; want the 404 behind %q", err, prefix)
	}
}

func TestLoadBalancers(t *testing.T) {
	lb := `{"id":"` + lbID + `","name":"web","provisioning_status":"ACTIVE","vip_address":"192.0.2.10"}`
	base := "/v2.0/lbaas/loadbalancers"
	p := base + "/" + lbID
	seen, sc := fakeOctavia(t, map[string]reply{
		"GET " + base:              {Body: `{"loadbalancers":[` + lb + `]}`},
		"GET " + p:                 {Body: `{"loadbalancer":` + lb + `}`},
		"POST " + base:             {Status: http.StatusCreated, Body: `{"loadbalancer":` + lb + `}`},
		"PUT " + p:                 {Body: `{"loadbalancer":` + lb + `}`},
		"DELETE " + p:              {Status: http.StatusNoContent},
		"PUT " + p + "/failover":   {Status: http.StatusAccepted},
		"GET " + p + "/stats":      {Body: `{"stats":{"bytes_in":10,"total_connections":2}}`},
		"GET " + p + "/status":     {Body: `{"statuses":{"loadbalancer":{"id":"` + lbID + `","name":"web","listeners":[{"id":"` + lsnID + `"}]}}}`},
		"GET " + base + "/missing": {Status: http.StatusNotFound, Body: `{}`},
	})
	ctx := context.Background()

	all, err := loadbalancer.ListLoadBalancers(ctx, sc, loadbalancers.ListOpts{Name: "web"})
	if err != nil || len(all) != 1 || all[0].VipAddress != "192.0.2.10" {
		t.Fatalf("ListLoadBalancers = %+v, %v", all, err)
	}
	want(t, last(t, seen), "GET", base, "name=web", "")

	if got, err := loadbalancer.GetLoadBalancer(ctx, sc, lbID); err != nil || got.Name != "web" {
		t.Fatalf("GetLoadBalancer = %+v, %v", got, err)
	}
	if _, err := loadbalancer.CreateLoadBalancer(ctx, sc, loadbalancers.CreateOpts{Name: "web", VipSubnetID: poolID}); err != nil {
		t.Fatalf("CreateLoadBalancer: %v", err)
	}
	want(t, last(t, seen), "POST", base, "", `{"loadbalancer":{"name":"web","vip_subnet_id":"`+poolID+`"}}`)

	name := "web2"
	if _, err := loadbalancer.UpdateLoadBalancer(ctx, sc, lbID, loadbalancers.UpdateOpts{Name: &name}); err != nil {
		t.Fatalf("UpdateLoadBalancer: %v", err)
	}
	want(t, last(t, seen), "PUT", p, "", `{"loadbalancer":{"name":"web2"}}`)

	if err := loadbalancer.DeleteLoadBalancer(ctx, sc, lbID, loadbalancers.DeleteOpts{Cascade: true}); err != nil {
		t.Fatalf("DeleteLoadBalancer: %v", err)
	}
	want(t, last(t, seen), "DELETE", p, "cascade=true", "")
	if err := loadbalancer.DeleteLoadBalancer(ctx, sc, lbID, nil); err != nil {
		t.Fatalf("DeleteLoadBalancer(nil opts): %v", err)
	}
	want(t, last(t, seen), "DELETE", p, "", "")

	if err := loadbalancer.FailoverLoadBalancer(ctx, sc, lbID); err != nil {
		t.Fatalf("FailoverLoadBalancer: %v", err)
	}
	want(t, last(t, seen), "PUT", p+"/failover", "", "")

	if s, err := loadbalancer.GetLoadBalancerStats(ctx, sc, lbID); err != nil || s.BytesIn != 10 || s.TotalConnections != 2 {
		t.Fatalf("GetLoadBalancerStats = %+v, %v", s, err)
	}
	if tree, err := loadbalancer.GetLoadBalancerStatuses(ctx, sc, lbID); err != nil || tree.Loadbalancer == nil || len(tree.Loadbalancer.Listeners) != 1 {
		t.Fatalf("GetLoadBalancerStatuses = %+v, %v", tree, err)
	}

	_, err = loadbalancer.GetLoadBalancer(ctx, sc, "missing")
	is404(t, err, "getting load balancer missing: ")
	_, err = loadbalancer.GetLoadBalancerStats(ctx, sc, "missing")
	is404(t, err, "getting statistics for load balancer missing: ")
	_, err = loadbalancer.GetLoadBalancerStatuses(ctx, sc, "missing")
	is404(t, err, "getting status tree for load balancer missing: ")
	is404(t, loadbalancer.FailoverLoadBalancer(ctx, sc, "missing"), "failing over load balancer missing: ")
}

func TestListErrors(t *testing.T) {
	_, sc := fakeOctavia(t, nil)
	ctx := context.Background()
	for prefix, call := range map[string]func() error{
		"listing load balancers: ":           func() error { _, err := loadbalancer.ListLoadBalancers(ctx, sc, nil); return err },
		"listing listeners: ":                func() error { _, err := loadbalancer.ListListeners(ctx, sc, nil); return err },
		"listing pools: ":                    func() error { _, err := loadbalancer.ListPools(ctx, sc, nil); return err },
		"listing members of pool " + poolID:  func() error { _, err := loadbalancer.ListMembers(ctx, sc, poolID, nil); return err },
		"listing health monitors: ":          func() error { _, err := loadbalancer.ListHealthMonitors(ctx, sc, nil); return err },
		"listing l7 policies: ":              func() error { _, err := loadbalancer.ListL7Policies(ctx, sc, nil); return err },
		"listing rules of l7 policy " + l7ID: func() error { _, err := loadbalancer.ListL7Rules(ctx, sc, l7ID, nil); return err },
		"listing load balancer flavors: ":    func() error { _, err := loadbalancer.ListFlavors(ctx, sc, nil); return err },
		"listing flavor profiles: ":          func() error { _, err := loadbalancer.ListFlavorProfiles(ctx, sc, nil); return err },
		"listing amphorae: ":                 func() error { _, err := loadbalancer.ListAmphorae(ctx, sc, nil); return err },
		"listing load balancer providers: ":  func() error { _, err := loadbalancer.ListProviders(ctx, sc, nil); return err },
		"listing load balancer quotas: ":     func() error { _, err := loadbalancer.ListQuotas(ctx, sc, ""); return err },
	} {
		is404(t, call(), prefix)
	}
}

// Find…ID is strict: a UUID passes through without a request, one match is
// its ID, none is a resolve.NotFoundError and several a resolve.AmbiguousError.
func TestFindStrict(t *testing.T) {
	one := func(id string) string { return `{"id":"` + id + `","name":"x"}` }
	seen, sc := fakeOctavia(t, map[string]reply{
		"GET /v2.0/lbaas/loadbalancers?name=one":                    {Body: `{"loadbalancers":[` + one(lbID) + `]}`},
		"GET /v2.0/lbaas/loadbalancers?name=none":                   {Body: `{"loadbalancers":[]}`},
		"GET /v2.0/lbaas/loadbalancers?name=two":                    {Body: `{"loadbalancers":[` + one(lbID) + `,` + one(poolID) + `]}`},
		"GET /v2.0/lbaas/listeners?name=one":                        {Body: `{"listeners":[` + one(lsnID) + `]}`},
		"GET /v2.0/lbaas/pools?name=one":                            {Body: `{"pools":[` + one(poolID) + `]}`},
		"GET /v2.0/lbaas/pools/" + poolID + "/members?name=one":     {Body: `{"members":[` + one(memberID) + `]}`},
		"GET /v2.0/lbaas/healthmonitors?name=one":                   {Body: `{"healthmonitors":[` + one(hmID) + `]}`},
		"GET /v2.0/lbaas/l7policies?name=one":                       {Body: `{"l7policies":[` + one(l7ID) + `]}`},
		"GET /v2.0/lbaas/flavors?name=one":                          {Body: `{"flavors":[` + one(flvID) + `]}`},
		"GET /v2.0/lbaas/flavorprofiles":                            {Body: `{"flavorprofiles":[{"id":"` + fpID + `","name":"one"},{"id":"` + amphID + `","name":"other"}]}`},
		"GET /v2.0/lbaas/loadbalancers?name=broken":                 {Status: http.StatusInternalServerError, Body: `{}`},
		"GET /v2.0/lbaas/pools/" + poolID + "/members?name=missing": {Body: `{"members":[]}`},
	})
	ctx := context.Background()

	n := len(*seen)
	if id, err := loadbalancer.FindLoadBalancerID(ctx, sc, lbID); err != nil || id != lbID || len(*seen) != n {
		t.Fatalf("FindLoadBalancerID(uuid) = %q, %v after %d requests", id, err, len(*seen)-n)
	}
	for name, find := range map[string]func() (string, error){
		lbID:     func() (string, error) { return loadbalancer.FindLoadBalancerID(ctx, sc, "one") },
		lsnID:    func() (string, error) { return loadbalancer.FindListenerID(ctx, sc, "one") },
		poolID:   func() (string, error) { return loadbalancer.FindPoolID(ctx, sc, "one") },
		memberID: func() (string, error) { return loadbalancer.FindMemberID(ctx, sc, poolID, "one") },
		hmID:     func() (string, error) { return loadbalancer.FindHealthMonitorID(ctx, sc, "one") },
		l7ID:     func() (string, error) { return loadbalancer.FindL7PolicyID(ctx, sc, "one") },
		flvID:    func() (string, error) { return loadbalancer.FindFlavorID(ctx, sc, "one") },
		fpID:     func() (string, error) { return loadbalancer.FindFlavorProfileID(ctx, sc, "one") },
	} {
		if id, err := find(); err != nil || id != name {
			t.Errorf("find = %q, %v; want %s", id, err, name)
		}
	}

	var nf *resolve.NotFoundError
	if _, err := loadbalancer.FindLoadBalancerID(ctx, sc, "none"); !errors.As(err, &nf) || nf.Kind != "load balancer" || nf.Ref != "none" {
		t.Errorf("FindLoadBalancerID(none) = %v; want a NotFoundError", err)
	}
	if _, err := loadbalancer.FindMemberID(ctx, sc, poolID, "missing"); !errors.As(err, &nf) || nf.Kind != "member" {
		t.Errorf("FindMemberID(missing) = %v; want a NotFoundError", err)
	}
	if _, err := loadbalancer.FindFlavorProfileID(ctx, sc, "nosuch"); !errors.As(err, &nf) || nf.Kind != "flavor profile" {
		t.Errorf("FindFlavorProfileID(nosuch) = %v; want a NotFoundError", err)
	}
	var amb *resolve.AmbiguousError
	if _, err := loadbalancer.FindLoadBalancerID(ctx, sc, "two"); !errors.As(err, &amb) || amb.Count != 2 {
		t.Errorf("FindLoadBalancerID(two) = %v; want an AmbiguousError of 2", err)
	}
	_, err := loadbalancer.FindLoadBalancerID(ctx, sc, "broken")
	if !gophercloud.ResponseCodeIs(err, http.StatusInternalServerError) || !strings.HasPrefix(err.Error(), `looking up load balancer "broken": `) {
		t.Errorf("FindLoadBalancerID(broken) = %v", err)
	}
}

// The nouns under a load balancer: one CRUD round each, asserting the paths.
func TestNouns(t *testing.T) {
	type noun struct {
		path, one, many, id string
	}
	nouns := []noun{
		{"/v2.0/lbaas/listeners", "listener", "listeners", lsnID},
		{"/v2.0/lbaas/pools", "pool", "pools", poolID},
		{"/v2.0/lbaas/pools/" + poolID + "/members", "member", "members", memberID},
		{"/v2.0/lbaas/healthmonitors", "healthmonitor", "healthmonitors", hmID},
		{"/v2.0/lbaas/l7policies", "l7policy", "l7policies", l7ID},
		{"/v2.0/lbaas/l7policies/" + l7ID + "/rules", "rule", "rules", ruleID},
		{"/v2.0/lbaas/flavors", "flavor", "flavors", flvID},
		{"/v2.0/lbaas/flavorprofiles", "flavorprofile", "flavorprofiles", fpID},
	}
	routes := map[string]reply{}
	for _, n := range nouns {
		item := `{"id":"` + n.id + `","name":"x"}`
		routes["GET "+n.path] = reply{Body: `{"` + n.many + `":[` + item + `]}`}
		routes["POST "+n.path] = reply{Status: http.StatusCreated, Body: `{"` + n.one + `":` + item + `}`}
		routes["GET "+n.path+"/"+n.id] = reply{Body: `{"` + n.one + `":` + item + `}`}
		routes["PUT "+n.path+"/"+n.id] = reply{Body: `{"` + n.one + `":` + item + `}`}
		routes["DELETE "+n.path+"/"+n.id] = reply{Status: http.StatusNoContent}
	}
	seen, sc := fakeOctavia(t, routes)
	ctx := context.Background()
	name := "y"
	weight := 3
	for _, n := range nouns {
		var calls []func() error
		switch n.one {
		case "listener":
			calls = []func() error{
				func() error { _, err := loadbalancer.ListListeners(ctx, sc, listeners.ListOpts{}); return err },
				func() error {
					_, err := loadbalancer.CreateListener(ctx, sc, listeners.CreateOpts{LoadbalancerID: lbID, Protocol: listeners.ProtocolHTTP, ProtocolPort: 80})
					return err
				},
				func() error { _, err := loadbalancer.GetListener(ctx, sc, lsnID); return err },
				func() error {
					_, err := loadbalancer.UpdateListener(ctx, sc, lsnID, listeners.UpdateOpts{Name: &name})
					return err
				},
				func() error { return loadbalancer.DeleteListener(ctx, sc, lsnID) },
			}
		case "pool":
			calls = []func() error{
				func() error { _, err := loadbalancer.ListPools(ctx, sc, pools.ListOpts{}); return err },
				func() error {
					_, err := loadbalancer.CreatePool(ctx, sc, pools.CreateOpts{LBMethod: pools.LBMethodRoundRobin, Protocol: pools.ProtocolHTTP, LoadbalancerID: lbID})
					return err
				},
				func() error { _, err := loadbalancer.GetPool(ctx, sc, poolID); return err },
				func() error {
					_, err := loadbalancer.UpdatePool(ctx, sc, poolID, pools.UpdateOpts{Name: &name})
					return err
				},
				func() error { return loadbalancer.DeletePool(ctx, sc, poolID) },
			}
		case "member":
			calls = []func() error{
				func() error { _, err := loadbalancer.ListMembers(ctx, sc, poolID, pools.ListMembersOpts{}); return err },
				func() error {
					_, err := loadbalancer.CreateMember(ctx, sc, poolID, pools.CreateMemberOpts{Address: "192.0.2.5", ProtocolPort: 80})
					return err
				},
				func() error { _, err := loadbalancer.GetMember(ctx, sc, poolID, memberID); return err },
				func() error {
					_, err := loadbalancer.UpdateMember(ctx, sc, poolID, memberID, pools.UpdateMemberOpts{Weight: &weight})
					return err
				},
				func() error { return loadbalancer.DeleteMember(ctx, sc, poolID, memberID) },
			}
		case "healthmonitor":
			calls = []func() error{
				func() error { _, err := loadbalancer.ListHealthMonitors(ctx, sc, monitors.ListOpts{}); return err },
				func() error {
					_, err := loadbalancer.CreateHealthMonitor(ctx, sc, monitors.CreateOpts{PoolID: poolID, Type: monitors.TypeHTTP, Delay: 5, Timeout: 3, MaxRetries: 3})
					return err
				},
				func() error { _, err := loadbalancer.GetHealthMonitor(ctx, sc, hmID); return err },
				func() error {
					_, err := loadbalancer.UpdateHealthMonitor(ctx, sc, hmID, monitors.UpdateOpts{Name: &name})
					return err
				},
				func() error { return loadbalancer.DeleteHealthMonitor(ctx, sc, hmID) },
			}
		case "l7policy":
			calls = []func() error{
				func() error { _, err := loadbalancer.ListL7Policies(ctx, sc, l7policies.ListOpts{}); return err },
				func() error {
					_, err := loadbalancer.CreateL7Policy(ctx, sc, l7policies.CreateOpts{ListenerID: lsnID, Action: l7policies.ActionReject})
					return err
				},
				func() error { _, err := loadbalancer.GetL7Policy(ctx, sc, l7ID); return err },
				func() error {
					_, err := loadbalancer.UpdateL7Policy(ctx, sc, l7ID, l7policies.UpdateOpts{Name: &name})
					return err
				},
				func() error { return loadbalancer.DeleteL7Policy(ctx, sc, l7ID) },
			}
		case "rule":
			calls = []func() error{
				func() error {
					_, err := loadbalancer.ListL7Rules(ctx, sc, l7ID, l7policies.ListRulesOpts{})
					return err
				},
				func() error {
					_, err := loadbalancer.CreateL7Rule(ctx, sc, l7ID, l7policies.CreateRuleOpts{RuleType: l7policies.TypePath, CompareType: l7policies.CompareTypeStartWith, Value: "/api"})
					return err
				},
				func() error { _, err := loadbalancer.GetL7Rule(ctx, sc, l7ID, ruleID); return err },
				func() error {
					v := "/v2"
					_, err := loadbalancer.UpdateL7Rule(ctx, sc, l7ID, ruleID, l7policies.UpdateRuleOpts{Value: v})
					return err
				},
				func() error { return loadbalancer.DeleteL7Rule(ctx, sc, l7ID, ruleID) },
			}
		case "flavor":
			calls = []func() error{
				func() error { _, err := loadbalancer.ListFlavors(ctx, sc, flavors.ListOpts{}); return err },
				func() error {
					_, err := loadbalancer.CreateFlavor(ctx, sc, flavors.CreateOpts{Name: "x", FlavorProfileId: fpID, Enabled: true})
					return err
				},
				func() error { _, err := loadbalancer.GetFlavor(ctx, sc, flvID); return err },
				func() error {
					_, err := loadbalancer.UpdateFlavor(ctx, sc, flvID, flavors.UpdateOpts{Name: name})
					return err
				},
				func() error { return loadbalancer.DeleteFlavor(ctx, sc, flvID) },
			}
		case "flavorprofile":
			calls = []func() error{
				func() error {
					_, err := loadbalancer.ListFlavorProfiles(ctx, sc, flavorprofiles.ListOpts{})
					return err
				},
				func() error {
					_, err := loadbalancer.CreateFlavorProfile(ctx, sc, flavorprofiles.CreateOpts{Name: "x", ProviderName: "amphora", FlavorData: "{}"})
					return err
				},
				func() error { _, err := loadbalancer.GetFlavorProfile(ctx, sc, fpID); return err },
				func() error {
					_, err := loadbalancer.UpdateFlavorProfile(ctx, sc, fpID, flavorprofiles.UpdateOpts{Name: name})
					return err
				},
				func() error { return loadbalancer.DeleteFlavorProfile(ctx, sc, fpID) },
			}
		}
		paths := []string{"GET " + n.path, "POST " + n.path, "GET " + n.path + "/" + n.id, "PUT " + n.path + "/" + n.id, "DELETE " + n.path + "/" + n.id}
		for i, call := range calls {
			if err := call(); err != nil {
				t.Fatalf("%s call %d: %v", n.one, i, err)
			}
			if got := last(t, seen); got.Method+" "+got.Path != paths[i] {
				t.Errorf("%s call %d sent %s %s; want %s", n.one, i, got.Method, got.Path, paths[i])
			}
		}
	}
}

// The calls gophercloud has no typed request for, and the error text each
// wraps a failure in.
func TestRawCalls(t *testing.T) {
	amph := "/v2.0/octavia/amphorae/" + amphID
	seen, sc := fakeOctavia(t, map[string]reply{
		"GET /v2.0/octavia/amphorae":                     {Body: `{"amphorae":[{"id":"` + amphID + `","role":"MASTER"}]}`},
		"GET " + amph:                                    {Body: `{"amphora":{"id":"` + amphID + `","status":"ALLOCATED"}}`},
		"PUT " + amph + "/failover":                      {Status: http.StatusAccepted},
		"PUT " + amph + "/config":                        {Status: http.StatusAccepted},
		"DELETE " + amph:                                 {Status: http.StatusNoContent},
		"GET " + amph + "/stats":                         {Body: `{"amphora_stats":[{"listener_id":"` + lsnID + `","bytes_in":7,"total_connections":3}]}`},
		"GET /v2.0/lbaas/providers":                      {Body: `{"providers":[{"name":"amphora","description":"d"}]}`},
		"GET /v2.0/lbaas/providers/amphora/capabilities": {Body: `{"flavor_capabilities":[{"name":"loadbalancer_topology","description":"t"}]}`},
		"PUT /v2.0/lbaas/flavors/" + flvID:               {Body: `{"flavor":{"id":"` + flvID + `","enabled":false}}`},
		"GET /v2.0/quotas/defaults":                      {Body: `{"quota":{"loadbalancer":-1,"listener":-1}}`},
		"GET /v2.0/quotas/" + project:                    {Body: `{"quota":{"loadbalancer":5}}`},
		"PUT /v2.0/quotas/" + project:                    {Status: http.StatusAccepted, Body: `{"quota":{"loadbalancer":5,"listener":-1}}`},
		"DELETE /v2.0/quotas/" + project:                 {Status: http.StatusAccepted},
		"GET /v2.0/quotas?project_id=" + project:         {Body: `{"quotas":[{"project_id":"` + project + `","loadbalancer":5}],"quotas_links":[]}`},
		"PUT /v2.0/octavia/amphorae/missing/config":      {Status: http.StatusConflict, Body: `{}`},
		"GET /v2.0/lbaas/providers/missing/capabilities": {Status: http.StatusBadRequest, Body: `{}`},
		"PUT /v2.0/lbaas/flavors/missing":                {Status: http.StatusNotFound, Body: `{}`},
		"PUT /v2.0/quotas/missing":                       {Status: http.StatusForbidden, Body: `{}`},
		"GET /v2.0/octavia/amphorae/missing/stats":       {Status: http.StatusNotFound, Body: `{}`},
		"DELETE /v2.0/octavia/amphorae/missing":          {Status: http.StatusNotFound, Body: `{}`},
	})
	ctx := context.Background()

	if all, err := loadbalancer.ListAmphorae(ctx, sc, amphorae.ListOpts{}); err != nil || len(all) != 1 || all[0].Role != "MASTER" {
		t.Fatalf("ListAmphorae = %+v, %v", all, err)
	}
	if am, err := loadbalancer.GetAmphora(ctx, sc, amphID); err != nil || am.Status != "ALLOCATED" {
		t.Fatalf("GetAmphora = %+v, %v", am, err)
	}
	if err := loadbalancer.FailoverAmphora(ctx, sc, amphID); err != nil {
		t.Fatalf("FailoverAmphora: %v", err)
	}
	if err := loadbalancer.ConfigureAmphora(ctx, sc, amphID); err != nil {
		t.Fatalf("ConfigureAmphora: %v", err)
	}
	want(t, last(t, seen), "PUT", amph+"/config", "", "")
	if err := loadbalancer.DeleteAmphora(ctx, sc, amphID); err != nil {
		t.Fatalf("DeleteAmphora: %v", err)
	}
	want(t, last(t, seen), "DELETE", amph, "", "")
	if st, err := loadbalancer.GetAmphoraStats(ctx, sc, amphID); err != nil || len(st) != 1 || st[0].BytesIn != 7 || st[0].ListenerID != lsnID {
		t.Fatalf("GetAmphoraStats = %+v, %v", st, err)
	}

	if all, err := loadbalancer.ListProviders(ctx, sc, providers.ListOpts{}); err != nil || len(all) != 1 || all[0].Name != "amphora" {
		t.Fatalf("ListProviders = %+v, %v", all, err)
	}
	if caps, err := loadbalancer.ListProviderCapabilities(ctx, sc, "amphora"); err != nil || len(caps) != 1 || caps[0].Name != "loadbalancer_topology" {
		t.Fatalf("ListProviderCapabilities = %+v, %v", caps, err)
	}

	if err := loadbalancer.SetFlavorEnabled(ctx, sc, flvID, false); err != nil {
		t.Fatalf("SetFlavorEnabled: %v", err)
	}
	want(t, last(t, seen), "PUT", "/v2.0/lbaas/flavors/"+flvID, "", `{"flavor":{"enabled":false}}`)

	if q, err := loadbalancer.GetDefaultQuota(ctx, sc); err != nil || q.Loadbalancer != -1 {
		t.Fatalf("GetDefaultQuota = %+v, %v", q, err)
	}
	if q, err := loadbalancer.GetQuota(ctx, sc, project); err != nil || q.Loadbalancer != 5 {
		t.Fatalf("GetQuota = %+v, %v", q, err)
	}
	five := 5
	if _, err := loadbalancer.UpdateQuota(ctx, sc, project, quotas.UpdateOpts{Loadbalancer: &five}); err != nil {
		t.Fatalf("UpdateQuota: %v", err)
	}
	want(t, last(t, seen), "PUT", "/v2.0/quotas/"+project, "", `{"quota":{"loadbalancer":5}}`)
	if q, err := loadbalancer.UnsetQuotas(ctx, sc, project, []string{"listener", "pool"}); err != nil || q.Listener != -1 {
		t.Fatalf("UnsetQuotas = %+v, %v", q, err)
	}
	want(t, last(t, seen), "PUT", "/v2.0/quotas/"+project, "", `{"quota":{"listener":null,"pool":null}}`)
	if err := loadbalancer.ResetQuota(ctx, sc, project); err != nil {
		t.Fatalf("ResetQuota: %v", err)
	}
	want(t, last(t, seen), "DELETE", "/v2.0/quotas/"+project, "", "")
	if all, err := loadbalancer.ListQuotas(ctx, sc, project); err != nil || len(all) != 1 || all[0].Loadbalancer == nil || *all[0].Loadbalancer != 5 {
		t.Fatalf("ListQuotas(project) = %+v, %v", all, err)
	}

	for prefix, err := range map[string]error{
		"configuring amphora missing: ":                          loadbalancer.ConfigureAmphora(ctx, sc, "missing"),
		"deleting amphora missing: ":                             loadbalancer.DeleteAmphora(ctx, sc, "missing"),
		"setting enabled=true on load balancer flavor missing: ": loadbalancer.SetFlavorEnabled(ctx, sc, "missing", true),
		"listing capabilities of provider missing: ": func() error {
			_, err := loadbalancer.ListProviderCapabilities(ctx, sc, "missing")
			return err
		}(),
		"getting statistics for amphora missing: ": func() error { _, err := loadbalancer.GetAmphoraStats(ctx, sc, "missing"); return err }(),
		"unsetting load balancer quotas [pool] for project missing: ": func() error {
			_, err := loadbalancer.UnsetQuotas(ctx, sc, "missing", []string{"pool"})
			return err
		}(),
	} {
		if err == nil || !strings.HasPrefix(err.Error(), prefix) {
			t.Errorf("error = %v; want prefix %q", err, prefix)
		}
	}
}

// ListQuotas follows quotas_links to the last page and stops on a link that
// names a page already read.
func TestListQuotasPaging(t *testing.T) {
	var sc *gophercloud.ServiceClient
	var seen *[]req
	routes := map[string]reply{}
	seen, sc = fakeOctavia(t, routes)
	page2 := sc.ResourceBase + "quotas?marker=a"
	routes["GET /v2.0/quotas"] = reply{Body: `{"quotas":[{"project_id":"a"}],"quotas_links":[{"rel":"next","href":"` + page2 + `"}]}`}
	routes["GET /v2.0/quotas?marker=a"] = reply{Body: `{"quotas":[{"project_id":"b"}],"quotas_links":[{"rel":"previous","href":"x"},{"rel":"next","href":"` + page2 + `"}]}`}
	all, err := loadbalancer.ListQuotas(context.Background(), sc, "")
	if err != nil || len(all) != 2 || all[0].ProjectID != "a" || all[1].ProjectID != "b" {
		t.Fatalf("ListQuotas = %+v, %v", all, err)
	}
	if len(*seen) != 2 {
		t.Errorf("ListQuotas sent %d requests; want 2", len(*seen))
	}
}

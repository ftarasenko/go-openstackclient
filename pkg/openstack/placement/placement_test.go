package placement_test

import (
	"context"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/placement/v1/allocationcandidates"
	"github.com/gophercloud/gophercloud/v2/openstack/placement/v1/allocations"
	"github.com/gophercloud/gophercloud/v2/openstack/placement/v1/resourceproviders"
	"github.com/gophercloud/gophercloud/v2/openstack/placement/v1/traits"
	th "github.com/gophercloud/gophercloud/v2/testhelper"
	fakeclient "github.com/gophercloud/gophercloud/v2/testhelper/client"

	"github.com/ftarasenko/koc/pkg/openstack/placement"
)

const (
	rp       = "4e8e5957-649f-477b-9e5b-f1f75b21c03c"
	rp2      = "9a0b1c2d-3e4f-4a5b-8c6d-7e8f9a0b1c2d"
	consumer = "8d1f1c4a-2b3c-4d5e-8f6a-7b8c9d0e1f2a"
	agg      = "1c2d3e4f-5a6b-4c7d-8e9f-0a1b2c3d4e5f"
	rpPath   = "/resource_providers/" + rp
	rpJSON   = `{"uuid":"` + rp + `","name":"rp","generation":1}`
)

// placementClient is a placement client on fs at microversion.
func placementClient(fs th.FakeServer, microversion string) *gophercloud.ServiceClient {
	sc := fakeclient.ServiceClient(fs)
	sc.Type = "placement"
	sc.Microversion = microversion
	return sc
}

type reply struct {
	status int
	body   string
}

// fake answers "METHOD /path[?query]" from routes and records every request
// with its JSON body and microversion header.
type fake struct {
	t      *testing.T
	routes map[string]reply
	seq    []string
	bodies map[string]string
	mv     map[string]string
}

func newFake(t *testing.T, microversion string, routes map[string]reply) (*fake, *gophercloud.ServiceClient) {
	t.Helper()
	fs := th.SetupHTTP()
	t.Cleanup(fs.Teardown)
	f := &fake{t: t, routes: routes, bodies: map[string]string{}, mv: map[string]string{}}
	fs.Mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.Path
		if r.URL.RawQuery != "" {
			key += "?" + r.URL.RawQuery
		}
		f.seq = append(f.seq, key)
		b, _ := io.ReadAll(r.Body)
		f.bodies[key] = string(b)
		f.mv[key] = r.Header.Get("OpenStack-API-Version")
		rep, ok := f.routes[key]
		if !ok {
			t.Errorf("unexpected request %s", key)
			rep = reply{http.StatusNotFound, `{}`}
		}
		if rep.body != "" {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(rep.status)
		_, _ = io.WriteString(w, rep.body)
	})
	return f, placementClient(fs, microversion)
}

// body asserts the JSON body sent with key.
func (f *fake) body(key, want string) {
	f.t.Helper()
	th.CheckJSONEquals(f.t, want, jsonValue(f.t, f.bodies[key]))
}

func jsonValue(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := jsonUnmarshal(s, &v); err != nil {
		t.Fatalf("request body %q: %v", s, err)
	}
	return v
}

func (f *fake) sequence(want ...string) {
	f.t.Helper()
	if !reflect.DeepEqual(f.seq, want) {
		f.t.Errorf("requests = %q; want %q", f.seq, want)
	}
}

func checkErr(t *testing.T, err error, prefix string) {
	t.Helper()
	if err == nil || !strings.HasPrefix(err.Error(), prefix) {
		t.Fatalf("error = %v; want prefix %q", err, prefix)
	}
	if !gophercloud.ResponseCodeIs(err, http.StatusInternalServerError) {
		t.Errorf("error %v does not wrap the 500", err)
	}
}

func ptr[T any](v T) *T { return &v }

// TestEndpoints sends each single-request call once to a route that accepts
// it and once to one that answers 500.
func TestEndpoints(t *testing.T) {
	tests := []struct {
		name  string
		route string
		ok    reply
		body  string // JSON the call must send, "" for none
		call  func(*testing.T, context.Context, *gophercloud.ServiceClient) error
		err   string
	}{
		{"ListResourceProviders", "GET /resource_providers?name=rp", reply{200, `{"resource_providers":[` + rpJSON + `]}`}, "",
			func(t *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				all, err := placement.ListResourceProviders(ctx, c, resourceproviders.ListOpts{Name: "rp"})
				if err == nil && (len(all) != 1 || all[0].UUID != rp) {
					t.Errorf("providers = %+v", all)
				}
				return err
			}, "listing resource providers: "},
		{"GetResourceProvider", "GET " + rpPath, reply{200, rpJSON}, "",
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				_, err := placement.GetResourceProvider(ctx, c, rp)
				return err
			}, "getting resource provider " + rp + ": "},
		{"CreateResourceProvider", "POST /resource_providers", reply{200, rpJSON}, `{"name":"rp","parent_provider_uuid":"` + rp2 + `"}`,
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				_, err := placement.CreateResourceProvider(ctx, c, resourceproviders.CreateOpts{Name: "rp", ParentProviderUUID: rp2})
				return err
			}, `creating resource provider "rp": `},
		{"UpdateResourceProvider", "PUT " + rpPath, reply{200, rpJSON}, `{"name":"rp2"}`,
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				_, err := placement.UpdateResourceProvider(ctx, c, rp, resourceproviders.UpdateOpts{Name: ptr("rp2")})
				return err
			}, "updating resource provider " + rp + ": "},
		{"DeleteResourceProvider", "DELETE " + rpPath, reply{204, ""}, "",
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				return placement.DeleteResourceProvider(ctx, c, rp)
			}, "deleting resource provider " + rp + ": "},
		{"GetProviderAllocations", "GET " + rpPath + "/allocations", reply{200, `{"allocations":{"` + consumer + `":{"resources":{"VCPU":2}}},"resource_provider_generation":1}`}, "",
			func(t *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				a, err := placement.GetProviderAllocations(ctx, c, rp)
				if err == nil && a.Allocations[consumer].Resources["VCPU"] != 2 {
					t.Errorf("allocations = %+v", a)
				}
				return err
			}, "getting allocations for resource provider " + rp + ": "},
		{"GetProviderUsages", "GET " + rpPath + "/usages", reply{200, `{"usages":{"VCPU":2}}`}, "",
			func(t *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				u, err := placement.GetProviderUsages(ctx, c, rp)
				if err == nil && u.Usages["VCPU"] != 2 {
					t.Errorf("usages = %+v", u)
				}
				return err
			}, "getting usages for resource provider " + rp + ": "},
		{"GetProviderTraits", "GET " + rpPath + "/traits", reply{200, `{"traits":["HW_CPU_X86_AVX"],"resource_provider_generation":1}`}, "",
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				_, err := placement.GetProviderTraits(ctx, c, rp)
				return err
			}, "listing traits for resource provider " + rp + ": "},
		{"DeleteProviderTraits", "DELETE " + rpPath + "/traits", reply{204, ""}, "",
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				return placement.DeleteProviderTraits(ctx, c, rp)
			}, "deleting the traits of resource provider " + rp + ": "},
		{"GetProviderAggregates", "GET " + rpPath + "/aggregates", reply{200, `{"aggregates":["` + agg + `"],"resource_provider_generation":1}`}, "",
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				_, err := placement.GetProviderAggregates(ctx, c, rp)
				return err
			}, "listing aggregates for resource provider " + rp + ": "},
		{"GetInventories", "GET " + rpPath + "/inventories", reply{200, `{"inventories":{"VCPU":{"total":8}},"resource_provider_generation":3}`}, "",
			func(t *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				inv, err := placement.GetInventories(ctx, c, rp)
				if err == nil && inv.Inventories["VCPU"].Total != 8 {
					t.Errorf("inventories = %+v", inv)
				}
				return err
			}, "listing inventories for resource provider " + rp + ": "},
		{"GetInventory", "GET " + rpPath + "/inventories/VCPU", reply{200, `{"total":8,"resource_provider_generation":3}`}, "",
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				_, err := placement.GetInventory(ctx, c, rp, "VCPU")
				return err
			}, "getting VCPU inventory for resource provider " + rp + ": "},
		{"DeleteInventory", "DELETE " + rpPath + "/inventories/VCPU", reply{204, ""}, "",
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				return placement.DeleteInventory(ctx, c, rp, "VCPU")
			}, "deleting VCPU inventory from resource provider " + rp + ": "},
		{"DeleteInventories", "DELETE " + rpPath + "/inventories", reply{204, ""}, "",
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				return placement.DeleteInventories(ctx, c, rp)
			}, "deleting the inventory of resource provider " + rp + ": "},
		{"GetAllocations", "GET /allocations/" + consumer, reply{200, `{"allocations":{"` + rp + `":{"resources":{"VCPU":2}}}}`}, "",
			func(t *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				a, err := placement.GetAllocations(ctx, c, consumer)
				if err == nil && a.Allocations[rp].Resources["VCPU"] != 2 {
					t.Errorf("allocations = %+v", a)
				}
				return err
			}, "showing the allocations of consumer " + consumer + ": "},
		{"UpdateAllocations", "PUT /allocations/" + consumer, reply{204, ""},
			`{"allocations":{"` + rp + `":{"resources":{"VCPU":2}}},"project_id":"p","user_id":"u","consumer_generation":null,"consumer_type":"INSTANCE"}`,
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				return placement.UpdateAllocations(ctx, c, consumer, allocations.UpdateOpts{
					Allocations: map[string]allocations.ProviderAllocationsOpts{rp: {Resources: map[string]int{"VCPU": 2}}},
					ProjectID:   "p", UserID: "u", ConsumerType: "INSTANCE",
				})
			}, "setting the allocations of consumer " + consumer + ": "},
		{"DeleteAllocations", "DELETE /allocations/" + consumer, reply{204, ""}, "",
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				return placement.DeleteAllocations(ctx, c, consumer)
			}, "deleting allocations for consumer " + consumer + ": "},
		{"ListAllocationCandidates", "GET /allocation_candidates?resources=VCPU%3A1", reply{200,
			`{"allocation_requests":[],"provider_summaries":{"` + rp + `":{"resources":{"VCPU":{"capacity":8,"used":1}}}}}`}, "",
			func(t *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				got, err := placement.ListAllocationCandidates(ctx, c, allocationcandidates.ListOpts{Resources: "VCPU:1"})
				if err == nil && got.ProviderSummaries[rp].Resources["VCPU"].Capacity != 8 {
					t.Errorf("candidates = %+v", got)
				}
				return err
			}, "listing allocation candidates: "},
		{"ListResourceClasses", "GET /resource_classes", reply{200, `{"resource_classes":[{"name":"VCPU"}]}`}, "",
			func(t *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				all, err := placement.ListResourceClasses(ctx, c)
				if err == nil && (len(all) != 1 || all[0].Name != "VCPU") {
					t.Errorf("classes = %+v", all)
				}
				return err
			}, "listing resource classes: "},
		{"GetResourceClass", "GET /resource_classes/CUSTOM_X", reply{200, `{"name":"CUSTOM_X"}`}, "",
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				_, err := placement.GetResourceClass(ctx, c, "CUSTOM_X")
				return err
			}, "showing resource class CUSTOM_X: "},
		{"CreateResourceClass", "POST /resource_classes", reply{201, ""}, `{"name":"CUSTOM_X"}`,
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				return placement.CreateResourceClass(ctx, c, "CUSTOM_X")
			}, "creating resource class CUSTOM_X: "},
		{"EnsureResourceClass", "PUT /resource_classes/CUSTOM_X", reply{204, ""}, "",
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				return placement.EnsureResourceClass(ctx, c, "CUSTOM_X")
			}, "setting resource class CUSTOM_X: "},
		{"DeleteResourceClass", "DELETE /resource_classes/CUSTOM_X", reply{204, ""}, "",
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				return placement.DeleteResourceClass(ctx, c, "CUSTOM_X")
			}, "deleting resource class CUSTOM_X: "},
		{"ListTraits", "GET /traits?name=startswith%3ACUSTOM", reply{200, `{"traits":["CUSTOM_A"]}`}, "",
			func(t *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				all, err := placement.ListTraits(ctx, c, traits.ListOpts{Name: "startswith:CUSTOM"})
				if err == nil && !reflect.DeepEqual(all, []string{"CUSTOM_A"}) {
					t.Errorf("traits = %v", all)
				}
				return err
			}, "listing traits: "},
		{"GetTrait", "GET /traits/CUSTOM_A", reply{204, ""}, "",
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				return placement.GetTrait(ctx, c, "CUSTOM_A")
			}, "showing trait CUSTOM_A: "},
		{"CreateTrait", "PUT /traits/CUSTOM_A", reply{201, ""}, "",
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				return placement.CreateTrait(ctx, c, "CUSTOM_A")
			}, "creating trait CUSTOM_A: "},
		{"DeleteTrait", "DELETE /traits/CUSTOM_A", reply{204, ""}, "",
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				return placement.DeleteTrait(ctx, c, "CUSTOM_A")
			}, "deleting trait CUSTOM_A: "},
		{"GetProjectUsage", "GET /usages?project_id=p&user_id=u", reply{200,
			`{"usages":{"INSTANCE":{"VCPU":2,"consumer_count":1},"MIGRATION":{"VCPU":1,"consumer_count":1}}}`}, "",
			func(t *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				u, err := placement.GetProjectUsage(ctx, c, "p", "u")
				if err == nil && !reflect.DeepEqual(u, map[string]int{"VCPU": 3}) {
					t.Errorf("usage = %v; want VCPU 3", u)
				}
				return err
			}, "showing resource usage for project p: "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, c := newFake(t, "1.39", map[string]reply{tt.route: tt.ok})
			if err := tt.call(t, context.Background(), c); err != nil {
				t.Fatalf("error = %v", err)
			}
			f.sequence(tt.route)
			if f.mv[tt.route] != "placement 1.39" {
				t.Errorf("microversion header = %q", f.mv[tt.route])
			}
			if tt.body != "" {
				f.body(tt.route, tt.body)
			}
		})
		t.Run(tt.name+" fails", func(t *testing.T) {
			_, c := newFake(t, "1.39", map[string]reply{tt.route: {500, `{}`}})
			checkErr(t, tt.call(t, context.Background(), c), tt.err)
		})
	}
}

// A usage body that is neither shape fails the call with the project named.
func TestGetProjectUsageMalformed(t *testing.T) {
	_, c := newFake(t, "1.39", map[string]reply{"GET /usages?project_id=p": {200, `{"usages":{"INSTANCE":"x"}}`}})
	if _, err := placement.GetProjectUsage(context.Background(), c, "p", ""); err == nil ||
		!strings.HasPrefix(err.Error(), `showing resource usage for project p: parsing usages for "INSTANCE": `) {
		t.Errorf("error = %v", err)
	}
}

package placement_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/gophercloud/gophercloud/v2"

	"github.com/ftarasenko/koc/pkg/openstack/placement"
)

func jsonUnmarshal(s string, v any) error { return json.Unmarshal([]byte(s), v) }

// Each Set… reads the provider's generation, then writes with it.
func TestGenerationWrites(t *testing.T) {
	inv := "GET " + rpPath + "/inventories"
	invGen := reply{200, `{"inventories":{},"resource_provider_generation":3}`}
	traitsKey := rpPath + "/traits"
	tests := []struct {
		name          string
		read, write   string
		readOK        reply
		writeOK       reply
		body          string
		call          func(context.Context, *gophercloud.ServiceClient) error
		readErr, wErr string
	}{
		{"SetInventory", inv, "PUT " + rpPath + "/inventories/VCPU", invGen,
			reply{200, `{"total":8,"resource_provider_generation":4}`},
			`{"resource_provider_generation":3,"total":8}`,
			func(ctx context.Context, c *gophercloud.ServiceClient) error {
				got, err := placement.SetInventory(ctx, c, rp, "VCPU", placement.InventoryFields{"total": 8})
				if err == nil && got.Total != 8 {
					return fmt.Errorf("inventory = %+v", got)
				}
				return err
			},
			"reading resource provider " + rp + " before writing its inventory: ",
			"setting VCPU inventory on resource provider " + rp + ": "},
		{"SetInventories", inv, "PUT " + rpPath + "/inventories", invGen,
			reply{200, `{"inventories":{"VCPU":{"total":8}},"resource_provider_generation":4}`},
			`{"resource_provider_generation":3,"inventories":{"VCPU":{"total":8,"allocation_ratio":2}}}`,
			func(ctx context.Context, c *gophercloud.ServiceClient) error {
				_, err := placement.SetInventories(ctx, c, rp, map[string]placement.InventoryFields{"VCPU": {"total": 8, "allocation_ratio": 2}})
				return err
			},
			"reading resource provider " + rp + " before writing its inventory: ",
			"setting the inventory of resource provider " + rp + ": "},
		{"SetProviderTraits", "GET " + traitsKey, "PUT " + traitsKey,
			reply{200, `{"traits":[],"resource_provider_generation":2}`},
			reply{200, `{"traits":["CUSTOM_A"],"resource_provider_generation":3}`},
			`{"resource_provider_generation":2,"traits":["CUSTOM_A"]}`,
			func(ctx context.Context, c *gophercloud.ServiceClient) error {
				_, err := placement.SetProviderTraits(ctx, c, rp, []string{"CUSTOM_A"})
				return err
			},
			"reading resource provider " + rp + " before writing its traits: ",
			"setting the traits of resource provider " + rp + ": "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, c := newFake(t, "1.39", map[string]reply{tt.read: tt.readOK, tt.write: tt.writeOK})
			if err := tt.call(context.Background(), c); err != nil {
				t.Fatal(err)
			}
			f.sequence(tt.read, tt.write)
			f.body(tt.write, tt.body)
		})
		t.Run(tt.name+" read fails", func(t *testing.T) {
			f, c := newFake(t, "1.39", map[string]reply{tt.read: {500, `{}`}})
			checkErr(t, tt.call(context.Background(), c), tt.readErr)
			f.sequence(tt.read)
		})
		t.Run(tt.name+" write fails", func(t *testing.T) {
			_, c := newFake(t, "1.39", map[string]reply{tt.read: tt.readOK, tt.write: {500, `{}`}})
			checkErr(t, tt.call(context.Background(), c), tt.wErr)
		})
	}
}

// A client at "latest" gets its number from the version document before the
// aggregate write, whose body shape depends on it; a numeric one does not.
func TestSetProviderAggregates(t *testing.T) {
	get, put := "GET "+rpPath+"/aggregates", "PUT "+rpPath+"/aggregates"
	routes := func() map[string]reply {
		return map[string]reply{
			get:     {200, `{"aggregates":[],"resource_provider_generation":4}`},
			"GET /": {200, `{"versions":[{"max_version":"1.39"}]}`},
			put:     {200, `{"aggregates":["` + agg + `"],"resource_provider_generation":5}`},
		}
	}
	want := `{"resource_provider_generation":4,"aggregates":["` + agg + `"]}`

	f, c := newFake(t, "latest", routes())
	got, err := placement.SetProviderAggregates(context.Background(), c, rp, []string{agg})
	if err != nil || len(got.Aggregates) != 1 {
		t.Fatalf("SetProviderAggregates = %+v, %v", got, err)
	}
	f.sequence(get, "GET /", put)
	f.body(put, want)
	if f.mv[put] != "placement 1.39" || c.Microversion != "latest" {
		t.Errorf("write sent %q, client left at %q; want 1.39 on a copy", f.mv[put], c.Microversion)
	}

	f, c = newFake(t, "1.20", routes())
	if _, err := placement.SetProviderAggregates(context.Background(), c, rp, []string{agg}); err != nil {
		t.Fatal(err)
	}
	f.sequence(get, put)

	_, c = newFake(t, "1.39", map[string]reply{get: {500, `{}`}})
	_, err = placement.SetProviderAggregates(context.Background(), c, rp, nil)
	checkErr(t, err, "reading resource provider "+rp+" before writing its aggregates: ")

	r := routes()
	r[put] = reply{500, `{}`}
	_, c = newFake(t, "1.39", r)
	_, err = placement.SetProviderAggregates(context.Background(), c, rp, nil)
	checkErr(t, err, "setting the aggregates of resource provider "+rp+": ")
}

func TestRemoveAllocations(t *testing.T) {
	get, put, del := "GET /allocations/"+consumer, "PUT /allocations/"+consumer, "DELETE /allocations/"+consumer
	current := reply{200, `{"allocations":{"` + rp + `":{"resources":{"VCPU":2,"MEMORY_MB":512}},"` + rp2 + `":{"resources":{"DISK_GB":10}}}}`}
	ctx := context.Background()

	t.Run("naming nothing deletes without a read", func(t *testing.T) {
		f, c := newFake(t, "1.39", map[string]reply{del: {204, ""}})
		left, err := placement.RemoveAllocations(ctx, c, consumer, nil, nil)
		if err != nil || left {
			t.Fatalf("= %v, %v; want nothing left", left, err)
		}
		f.sequence(del)
	})
	t.Run("the rest is written back", func(t *testing.T) {
		f, c := newFake(t, "1.39", map[string]reply{get: current, put: {204, ""}})
		left, err := placement.RemoveAllocations(ctx, c, consumer, []string{rp2}, []string{"MEMORY_MB"})
		if err != nil || !left {
			t.Fatalf("= %v, %v; want some left", left, err)
		}
		f.sequence(get, put)
		f.body(put, `{"allocations":{"`+rp+`":{"resources":{"VCPU":2}}},"consumer_generation":null,"project_id":"","user_id":""}`)
	})
	t.Run("nothing left is a delete", func(t *testing.T) {
		f, c := newFake(t, "1.39", map[string]reply{get: current, del: {204, ""}})
		left, err := placement.RemoveAllocations(ctx, c, consumer, []string{rp, rp2}, nil)
		if err != nil || left {
			t.Fatalf("= %v, %v; want nothing left", left, err)
		}
		f.sequence(get, del)
	})
	t.Run("errors", func(t *testing.T) {
		_, c := newFake(t, "1.39", map[string]reply{get: {500, `{}`}})
		_, err := placement.RemoveAllocations(ctx, c, consumer, []string{rp}, nil)
		checkErr(t, err, "reading the allocations of consumer "+consumer+": ")

		_, c = newFake(t, "1.39", map[string]reply{get: current, put: {500, `{}`}})
		_, err = placement.RemoveAllocations(ctx, c, consumer, []string{rp}, nil)
		checkErr(t, err, "updating the allocations of consumer "+consumer+": ")

		_, c = newFake(t, "1.39", map[string]reply{del: {500, `{}`}})
		_, err = placement.RemoveAllocations(ctx, c, consumer, nil, nil)
		checkErr(t, err, "deleting the allocations of consumer "+consumer+": ")
	})
}

func TestRequiresConsumerType(t *testing.T) {
	for mv, want := range map[string]bool{"latest": true, "": true, "1.37": false, "1.38": true, "1.39": true} {
		if got := placement.RequiresConsumerType(mv); got != want {
			t.Errorf("RequiresConsumerType(%q) = %v; want %v", mv, got, want)
		}
	}
}

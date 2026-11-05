package placement_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"

	"github.com/gophercloud/gophercloud/v2"

	"github.com/ftarasenko/go-openstackclient/pkg/openstack"
	"github.com/ftarasenko/go-openstackclient/pkg/openstack/placement"
)

// Setting one inventory class reads the provider's generation first and
// writes with it; the caller never handles the generation.
func ExampleSetInventory() {
	placementAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			fmt.Println("read the inventories")
			_, _ = io.WriteString(w, `{"inventories":{},"resource_provider_generation":7}`)
			return
		}
		var sent struct {
			Generation int `json:"resource_provider_generation"`
		}
		if json.NewDecoder(r.Body).Decode(&sent) == nil && sent.Generation == 7 {
			fmt.Println("wrote VCPU at the generation it read")
		}
		_, _ = io.WriteString(w, `{"total":64,"reserved":0,"allocation_ratio":4,"resource_provider_generation":8}`)
	}))
	defer placementAPI.Close()

	pc := &gophercloud.ProviderClient{
		EndpointLocator: func(gophercloud.EndpointOpts) (string, error) { return placementAPI.URL + "/", nil },
	}
	sess := openstack.FromProvider(pc, gophercloud.EndpointOpts{Region: "RegionOne"}, openstack.Options{})
	pl, err := sess.Placement()
	if err != nil {
		fmt.Println(err)
		return
	}

	inv, err := placement.SetInventory(context.Background(), pl, "rp-1", "VCPU",
		placement.InventoryFields{"total": 64, "allocation_ratio": 4.0})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println("VCPU total", inv.Total, "ratio", inv.AllocationRatio)
	// Output:
	// read the inventories
	// wrote VCPU at the generation it read
	// VCPU total 64 ratio 4
}

package loadbalancer_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/loadbalancer/v2/loadbalancers"

	"github.com/ftarasenko/koc/pkg/openstack"
	"github.com/ftarasenko/koc/pkg/openstack/loadbalancer"
)

// Create a load balancer by its VIP subnet and wait until octavia has built
// it.
func ExampleWaitForLoadBalancerActive() {
	polls := 0
	octaviaAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			fmt.Println(r.Method, r.URL.Path)
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"loadbalancer":{"id":"lb-1","name":"web","provisioning_status":"PENDING_CREATE"}}`)
			return
		}
		status := "PENDING_CREATE"
		if polls++; polls == 2 {
			status = "ACTIVE"
		}
		fmt.Println(r.Method, r.URL.Path, "→", status)
		_, _ = io.WriteString(w, `{"loadbalancer":{"id":"lb-1","provisioning_status":"`+status+`"}}`)
	}))
	defer octaviaAPI.Close()

	pc := &gophercloud.ProviderClient{
		EndpointLocator: func(gophercloud.EndpointOpts) (string, error) { return octaviaAPI.URL + "/", nil },
	}
	sess := openstack.FromProvider(pc, gophercloud.EndpointOpts{Region: "RegionOne"}, openstack.Options{})
	lc, err := sess.LoadBalancer()
	if err != nil {
		fmt.Println(err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	lb, err := loadbalancer.CreateLoadBalancer(ctx, lc, loadbalancers.CreateOpts{Name: "web", VipSubnetID: "subnet-1"})
	if err != nil {
		fmt.Println(err)
		return
	}
	if err := loadbalancer.WaitForLoadBalancerActive(ctx, lc, lb.ID, time.Millisecond); err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(lb.Name, "is ACTIVE")
	// Output:
	// POST /v2.0/lbaas/loadbalancers
	// GET /v2.0/lbaas/loadbalancers/lb-1 → PENDING_CREATE
	// GET /v2.0/lbaas/loadbalancers/lb-1 → ACTIVE
	// web is ACTIVE
}

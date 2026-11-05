package network_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"

	"github.com/gophercloud/gophercloud/v2"

	"github.com/ftarasenko/go-openstackclient/pkg/openstack"
	"github.com/ftarasenko/go-openstackclient/pkg/openstack/network"
)

// Read a project's network quotas and the defaults neutron would apply.
func ExampleGetQuota() {
	neutronAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Println(r.Method, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v2.0/quotas/demo/default" {
			_, _ = io.WriteString(w, `{"quota":{"network":100,"port":500}}`)
			return
		}
		_, _ = io.WriteString(w, `{"quota":{"network":10,"port":50}}`)
	}))
	defer neutronAPI.Close()

	pc := &gophercloud.ProviderClient{
		EndpointLocator: func(gophercloud.EndpointOpts) (string, error) { return neutronAPI.URL + "/", nil },
	}
	sess := openstack.FromProvider(pc, gophercloud.EndpointOpts{Region: "RegionOne"}, openstack.Options{})
	nc, err := sess.Network()
	if err != nil {
		fmt.Println(err)
		return
	}
	ctx := context.Background()
	q, err := network.GetQuota(ctx, nc, "demo")
	if err != nil {
		fmt.Println(err)
		return
	}
	d, err := network.GetDefaultQuota(ctx, nc, "demo")
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Printf("networks %d of a default %d, ports %d of %d\n", q.Network, d.Network, q.Port, d.Port)
	// Output:
	// GET /v2.0/quotas/demo
	// GET /v2.0/quotas/demo/default
	// networks 10 of a default 100, ports 50 of 500
}

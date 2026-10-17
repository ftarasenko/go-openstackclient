package resolve_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack"

	kocopenstack "github.com/ftarasenko/go-openstackclient/pkg/openstack"
	"github.com/ftarasenko/go-openstackclient/pkg/openstack/resolve"
)

// A name resolves to an ID with one filtered listing; a name that matches
// nothing is an error that matches ErrNotFound.
func ExampleNetworkID() {
	neutron := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("name") == "private" {
			_, _ = w.Write([]byte(`{"networks":[{"id":"9b5c1c2e-3f4a-4b6c-8d7e-0f1a2b3c4d5e","name":"private"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"networks":[]}`))
	}))
	defer neutron.Close()
	network := &gophercloud.ServiceClient{
		ProviderClient: &gophercloud.ProviderClient{},
		Endpoint:       neutron.URL + "/",
		Type:           "network",
	}

	ctx := context.Background()
	id, err := resolve.NetworkID(ctx, network, "private")
	fmt.Println(id, err)

	_, err = resolve.NetworkID(ctx, network, "public")
	fmt.Println(err, errors.Is(err, kocopenstack.ErrNotFound))
	// Output:
	// 9b5c1c2e-3f4a-4b6c-8d7e-0f1a2b3c4d5e <nil>
	// no network found for "public" true
}

// A Resolver with a Cache asks once for a name it has resolved, per endpoint
// and token: a refresh loop resolving the same names stops costing a lookup
// per name per tick.
func ExampleResolver() {
	lookups := 0
	glance := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		lookups++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"images":[{"id":"0b1c2d3e-4f5a-4b6c-8d7e-9f0a1b2c3d4e","name":"ubuntu-24.04"}]}`))
	}))
	defer glance.Close()
	image, _ := openstack.NewImageV2(&gophercloud.ProviderClient{
		EndpointLocator: func(gophercloud.EndpointOpts) (string, error) { return glance.URL + "/", nil },
	}, gophercloud.EndpointOpts{})

	r := resolve.Resolver{Cache: resolve.NewCache(5 * time.Minute)}
	for range 3 {
		id, err := r.ImageID(context.Background(), image, "ubuntu-24.04")
		if err != nil {
			fmt.Println(err)
			return
		}
		fmt.Println(id)
	}
	fmt.Println("lookups:", lookups)
	// Output:
	// 0b1c2d3e-4f5a-4b6c-8d7e-9f0a1b2c3d4e
	// 0b1c2d3e-4f5a-4b6c-8d7e-9f0a1b2c3d4e
	// 0b1c2d3e-4f5a-4b6c-8d7e-9f0a1b2c3d4e
	// lookups: 1
}

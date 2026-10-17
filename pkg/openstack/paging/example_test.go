package paging_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/networks"

	"github.com/ftarasenko/go-openstackclient/pkg/openstack/paging"
)

// Collect stops paging once the cap is met: two networks out of a collection
// served one per page cost two requests, not one per network.
func ExampleCollect() {
	requests := 0
	var neutron *httptest.Server
	neutron = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		marker := r.URL.Query().Get("marker")
		next := map[string]string{"": "net-1", "net-1": "net-2", "net-2": "net-3"}[marker]
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"networks":[{"id":%q,"name":%q}],"networks_links":[{"rel":"next","href":"%s/v2.0/networks?marker=%s"}]}`,
			next, next, neutron.URL, next)
	}))
	defer neutron.Close()
	network := &gophercloud.ServiceClient{
		ProviderClient: &gophercloud.ProviderClient{},
		Endpoint:       neutron.URL + "/",
		ResourceBase:   neutron.URL + "/v2.0/",
		Type:           "network",
	}

	nets, err := paging.Collect(context.Background(), networks.List(network, networks.ListOpts{}), 2, networks.ExtractNetworks)
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, n := range nets {
		fmt.Println(n.ID)
	}
	fmt.Println("requests:", requests)
	// Output:
	// net-1
	// net-2
	// requests: 2
}

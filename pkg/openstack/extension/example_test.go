package extension_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"

	"github.com/gophercloud/gophercloud/v2"

	"github.com/ftarasenko/koc/pkg/openstack"
	"github.com/ftarasenko/koc/pkg/openstack/extension"
)

// Check whether neutron offers an extension before relying on it.
func ExampleList() {
	neutronAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Println(r.Method, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"extensions":[{"name":"Trunk","alias":"trunk"},{"name":"Port Security","alias":"port-security"}]}`)
	}))
	defer neutronAPI.Close()

	pc := &gophercloud.ProviderClient{
		EndpointLocator: func(gophercloud.EndpointOpts) (string, error) { return neutronAPI.URL + "/", nil },
	}
	nc, err := openstack.FromProvider(pc, gophercloud.EndpointOpts{}, openstack.Options{}).Network()
	if err != nil {
		fmt.Println(err)
		return
	}
	exts, err := extension.List(context.Background(), nc)
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, e := range exts {
		if e.Alias == "trunk" {
			fmt.Println("trunks are available")
		}
	}
	// Output:
	// GET /v2.0/extensions
	// trunks are available
}

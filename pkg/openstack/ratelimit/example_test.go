package ratelimit_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"

	"github.com/gophercloud/gophercloud/v2"

	"github.com/ftarasenko/go-openstackclient/pkg/openstack"
	"github.com/ftarasenko/go-openstackclient/pkg/openstack/ratelimit"
)

// List the rate limits cinder is configured with.
func ExampleList() {
	cinderAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Println(r.Method, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"limits":{"rate":[{"uri":"*","regex":".*","limit":[{"verb":"POST","value":10,"remaining":10,"unit":"MINUTE"}]}]}}`)
	}))
	defer cinderAPI.Close()

	pc := &gophercloud.ProviderClient{
		EndpointLocator: func(gophercloud.EndpointOpts) (string, error) { return cinderAPI.URL + "/", nil },
	}
	vc, err := openstack.FromProvider(pc, gophercloud.EndpointOpts{}, openstack.Options{}).Volume()
	if err != nil {
		fmt.Println(err)
		return
	}
	limits, err := ratelimit.List(context.Background(), vc, "")
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, l := range limits {
		fmt.Printf("%s %s: %d per %s\n", l.Verb, l.URI, l.Value, l.Unit)
	}
	// Output:
	// GET /limits
	// POST *: 10 per MINUTE
}

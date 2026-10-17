package image_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/images"

	"github.com/ftarasenko/go-openstackclient/pkg/openstack"
	"github.com/ftarasenko/go-openstackclient/pkg/openstack/image"
)

// sessionFor is a Session whose catalog points every service at url — what
// openstack.New returns, minus the Keystone.
func sessionFor(url string) *openstack.Session {
	pc := &gophercloud.ProviderClient{
		TokenID:         "example-token",
		EndpointLocator: func(gophercloud.EndpointOpts) (string, error) { return url + "/", nil },
	}
	return openstack.FromProvider(pc, gophercloud.EndpointOpts{Region: "RegionOne"}, openstack.Options{})
}

// List every image the token can see, whatever its visibility.
func ExampleListImages() {
	glance := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Println(r.Method, r.URL.Path, r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"images":[
			{"id":"0b1c2d3e-4f5a-4b6c-8d7e-9f0a1b2c3d4e","name":"cirros","status":"active","visibility":"public"},
			{"id":"9e8d7c6b-5a4f-4e3d-8c2b-1a0f9e8d7c6b","name":"build-42","status":"active","visibility":"community"}]}`)
	}))
	defer glance.Close()

	client, err := sessionFor(glance.URL).Image()
	if err != nil {
		fmt.Println(err)
		return
	}
	all, err := image.ListImages(context.Background(), client, images.ListOpts{Visibility: image.VisibilityAll})
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, img := range all {
		fmt.Println(img.Name, img.Visibility)
	}
	// Output:
	// GET /v2/images visibility=all
	// cirros public
	// build-42 community
}

// Wait for an asynchronous import to finish, within a deadline.
func ExampleWaitForStatus() {
	reads := 0
	glance := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reads++
		status := "importing"
		if reads == 3 {
			status = "active"
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"0b1c2d3e-4f5a-4b6c-8d7e-9f0a1b2c3d4e","name":"cirros","status":%q}`, status)
	}))
	defer glance.Close()

	client, err := sessionFor(glance.URL).Image()
	if err != nil {
		fmt.Println(err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	img, err := image.WaitForStatus(ctx, client, "0b1c2d3e-4f5a-4b6c-8d7e-9f0a1b2c3d4e", images.ImageStatusActive, time.Millisecond)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(img.Name, img.Status, "after", reads, "reads")
	// Output:
	// cirros active after 3 reads
}

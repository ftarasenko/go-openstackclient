package volume_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/volumes"

	"github.com/ftarasenko/go-openstackclient/pkg/openstack"
	"github.com/ftarasenko/go-openstackclient/pkg/openstack/volume"
)

// Create a volume and wait until cinder has finished building it.
func ExampleWaitForVolumeAvailable() {
	reads := 0
	cinderAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Println(strings.TrimSpace(r.Method + " " + r.URL.Path))
		w.Header().Set("Content-Type", "application/json")
		status := "creating"
		if r.Method == http.MethodGet {
			if reads++; reads > 1 {
				status = "available"
			}
		} else {
			w.WriteHeader(http.StatusAccepted)
		}
		_, _ = io.WriteString(w, `{"volume":{"id":"0b6c5e9a-1f2d-4c3b-9a8e-7d6f5e4c3b2a","name":"data","size":10,"status":"`+status+`"}}`)
	}))
	defer cinderAPI.Close()

	pc := &gophercloud.ProviderClient{
		TokenID:         "***",
		EndpointLocator: func(gophercloud.EndpointOpts) (string, error) { return cinderAPI.URL + "/", nil },
	}
	client, err := openstack.FromProvider(pc, gophercloud.EndpointOpts{Region: "RegionOne"}, openstack.Options{}).Volume()
	if err != nil {
		fmt.Println(err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	v, err := volume.CreateVolume(ctx, client, volumes.CreateOpts{Name: "data", Size: 10}, nil)
	if err != nil {
		fmt.Println(err)
		return
	}
	if err := volume.WaitForVolumeAvailable(ctx, client, v.ID, 10*time.Millisecond); err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(v.Name, "is available")
	// Output:
	// POST /volumes
	// GET /volumes/0b6c5e9a-1f2d-4c3b-9a8e-7d6f5e4c3b2a
	// GET /volumes/0b6c5e9a-1f2d-4c3b-9a8e-7d6f5e4c3b2a
	// data is available
}

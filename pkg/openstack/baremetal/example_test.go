package baremetal_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/baremetal/v1/nodes"

	"github.com/ftarasenko/go-openstackclient/pkg/openstack"
	"github.com/ftarasenko/go-openstackclient/pkg/openstack/baremetal"
)

// session points an openstack.Session at a fake ironic.
func session(h http.HandlerFunc) (*openstack.Session, func()) {
	srv := httptest.NewServer(h)
	pc := &gophercloud.ProviderClient{
		EndpointLocator: func(gophercloud.EndpointOpts) (string, error) { return srv.URL + "/v1/", nil },
	}
	return openstack.FromProvider(pc, gophercloud.EndpointOpts{Region: "RegionOne"}, openstack.Options{}), srv.Close
}

// Deploying a node is a provision request and a wait for the transition to
// settle; the wait's deadline is the context's.
func ExampleWaitForProvisionState() {
	polls := 0
	sess, done := session(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPut {
			var sent struct {
				Target string `json:"target"`
			}
			if json.NewDecoder(r.Body).Decode(&sent) == nil && sent.Target == "active" {
				fmt.Println("requested deploy")
			}
			w.WriteHeader(http.StatusAccepted)
			return
		}
		polls++
		if polls < 3 {
			_, _ = io.WriteString(w, `{"provision_state":"deploying","target_provision_state":"active"}`)
			return
		}
		_, _ = io.WriteString(w, `{"provision_state":"active","target_provision_state":null}`)
	})
	defer done()
	ironic, err := sess.Baremetal()
	if err != nil {
		fmt.Println(err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := baremetal.SetProvisionState(ctx, ironic, "bm-1",
		nodes.ProvisionStateOpts{Target: nodes.TargetActive}); err != nil {
		fmt.Println(err)
		return
	}
	if err := baremetal.WaitForProvisionState(ctx, ironic, "bm-1", nodes.Active, time.Millisecond); err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println("bm-1 is active")
	// Output:
	// requested deploy
	// bm-1 is active
}

// A feature newer than the cloud fails as a *MicroversionError, which says
// which API version it needs and what the cloud serves.
func ExampleMicroversionError() {
	sess, done := session(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(baremetal.MaxVersionHeader, "1.80")
		w.WriteHeader(http.StatusNotFound)
	})
	defer done()
	ironic, err := sess.Baremetal()
	if err != nil {
		fmt.Println(err)
		return
	}

	_, err = baremetal.ListFirmware(context.Background(), ironic, "bm-1")
	var me *baremetal.MicroversionError
	if errors.As(err, &me) {
		fmt.Printf("%s needs ironic API %s; the cloud serves %s\n", me.Feature, me.Feature.Microversion(), me.CloudMax)
	}
	// Output:
	// node firmware needs ironic API 1.86; the cloud serves 1.80
}

// ListNodes sends a false maintenance filter, which gophercloud's plain bool
// cannot, and caps the result.
func ExampleListNodes() {
	sess, done := session(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("maintenance") == "false" {
			fmt.Println("asked for nodes out of maintenance")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"nodes":[{"name":"bm-1"},{"name":"bm-2"}]}`)
	})
	defer done()
	ironic, err := sess.Baremetal()
	if err != nil {
		fmt.Println(err)
		return
	}

	inService := false
	list, err := baremetal.ListNodes(context.Background(), ironic,
		baremetal.NodeListOpts{Maintenance: &inService}, false, 1)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(len(list), "node:", list[0]["name"])
	// Output:
	// asked for nodes out of maintenance
	// 1 node: bm-1
}

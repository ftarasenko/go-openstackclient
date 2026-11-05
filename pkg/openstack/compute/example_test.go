package compute_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/keypairs"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/services"

	"github.com/ftarasenko/go-openstackclient/pkg/openstack"
	"github.com/ftarasenko/go-openstackclient/pkg/openstack/compute"
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

// Find a flavor by name — an admin's private flavor too — and grant a project
// access to it.
func ExampleFindFlavor() {
	novaAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Println(strings.TrimSpace(r.Method + " " + r.URL.Path + " " + r.URL.RawQuery))
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/flavors/detail" && r.URL.RawQuery == "":
			_, _ = io.WriteString(w, `{"flavors":[{"id":"1","name":"m1.small","os-flavor-access:is_public":true}]}`)
		case r.URL.Path == "/flavors/detail":
			_, _ = io.WriteString(w, `{"flavors":[{"id":"1","name":"m1.small","os-flavor-access:is_public":true},
				{"id":"7","name":"gpu.large","os-flavor-access:is_public":false}]}`)
		default:
			_, _ = io.WriteString(w, `{"flavor_access":[{"flavor_id":"7","tenant_id":"6c1f0e2d3b4a4c5d8e9f0a1b2c3d4e5f"}]}`)
		}
	}))
	defer novaAPI.Close()

	client, err := sessionFor(novaAPI.URL).Compute()
	if err != nil {
		fmt.Println(err)
		return
	}
	ctx := context.Background()
	fl, err := compute.FindFlavor(ctx, client, "gpu.large")
	if err != nil {
		fmt.Println(err)
		return
	}
	access, err := compute.AddFlavorAccess(ctx, client, fl.ID, "6c1f0e2d3b4a4c5d8e9f0a1b2c3d4e5f")
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(fl.ID, fl.IsPublic, len(access))
	// Output:
	// GET /flavors/detail
	// GET /flavors/detail is_public=None
	// POST /flavors/7/action
	// 7 false 1
}

// Have nova generate a keypair; the private key is returned only this once.
func ExampleCreateKeypair() {
	novaAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Println(r.Method, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"keypair":{"name":"ops","type":"ssh","fingerprint":"aa:bb",
			"public_key":"ssh-ed25519 AAAA","private_key":"-----BEGIN OPENSSH PRIVATE KEY-----\n..."}}`)
	}))
	defer novaAPI.Close()

	client, err := sessionFor(novaAPI.URL).Compute()
	if err != nil {
		fmt.Println(err)
		return
	}
	k, err := compute.CreateKeypair(context.Background(), client, keypairs.CreateOpts{Name: "ops"})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(k.Name, k.PrivateKey != "")
	// Output:
	// POST /os-keypairs
	// ops true
}

// Bring a forced-down compute service back up and re-enable it, by host and
// binary. gophercloud's own UpdateOpts cannot send forced_down=false.
func ExampleUpdateService() {
	novaAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Println(strings.TrimSpace(r.Method + " " + r.URL.Path + " " + r.URL.RawQuery))
		if r.Method == http.MethodPut {
			body, _ := io.ReadAll(r.Body)
			fmt.Println("flat body:", string(body) == `{"forced_down":false,"status":"enabled"}`)
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, `{"services":[{"id":"7f1e2d3c-4b5a-4c6d-8e9f-0a1b2c3d4e5f","binary":"nova-compute","host":"cmp-01"}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"service":{"id":"7f1e2d3c-4b5a-4c6d-8e9f-0a1b2c3d4e5f","status":"enabled","forced_down":false}}`)
	}))
	defer novaAPI.Close()

	client, err := sessionFor(novaAPI.URL).Compute()
	if err != nil {
		fmt.Println(err)
		return
	}
	ctx := context.Background()
	id, err := compute.FindServiceID(ctx, client, "cmp-01", "nova-compute")
	if err != nil {
		fmt.Println(err)
		return
	}
	up := false
	s, err := compute.UpdateService(ctx, client, id, compute.ServiceUpdateOpts{Status: services.ServiceEnabled, ForcedDown: &up})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(s.Status, s.ForcedDown)
	// Output:
	// GET /os-services binary=nova-compute&host=cmp-01
	// PUT /os-services/7f1e2d3c-4b5a-4c6d-8e9f-0a1b2c3d4e5f
	// flat body: true
	// enabled false
}

// Associate a floating IP through nova's proxy. The client stays at its own
// microversion; the call goes out at 2.43, below nova's removal of the action.
func ExampleAddFloatingIP() {
	novaAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Println(r.Method, r.URL.Path, "pinned:", r.Header.Get("OpenStack-API-Version") == "compute 2.43")
		w.WriteHeader(http.StatusAccepted)
	}))
	defer novaAPI.Close()

	client, err := sessionFor(novaAPI.URL).Compute()
	if err != nil {
		fmt.Println(err)
		return
	}
	err = compute.AddFloatingIP(context.Background(), client, "1b2c3d4e-5f6a-4b7c-8d9e-0f1a2b3c4d5e", "203.0.113.5", "")
	fmt.Println(err, client.Microversion)
	// Output:
	// POST /servers/1b2c3d4e-5f6a-4b7c-8d9e-0f1a2b3c4d5e/action pinned: true
	// <nil> latest
}

// Stop a server and wait until it is SHUTOFF with no task running. The
// context's deadline bounds the wait.
func ExampleWaitForServerStatus() {
	reads := 0
	novaAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		reads++
		status, task := "ACTIVE", `"powering-off"`
		if reads > 1 {
			status, task = "SHUTOFF", "null"
		}
		_, _ = fmt.Fprintf(w, `{"server":{"id":"1b2c3d4e-5f6a-4b7c-8d9e-0f1a2b3c4d5e","status":%q,"OS-EXT-STS:task_state":%s}}`, status, task)
	}))
	defer novaAPI.Close()

	client, err := sessionFor(novaAPI.URL).Compute()
	if err != nil {
		fmt.Println(err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	const id = "1b2c3d4e-5f6a-4b7c-8d9e-0f1a2b3c4d5e"
	if err := compute.StopServer(ctx, client, id); err != nil {
		fmt.Println(err)
		return
	}
	err = compute.WaitForServerStatus(ctx, client, id, []string{"SHUTOFF"}, 10*time.Millisecond)
	fmt.Println(err, "after", reads, "reads")
	// Output:
	// <nil> after 2 reads
}

package compute_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/keypairs"

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

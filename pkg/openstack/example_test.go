package openstack_test

import (
	"context"
	"fmt"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	gcopenstack "github.com/gophercloud/gophercloud/v2/openstack"

	"github.com/ftarasenko/go-openstackclient/pkg/openstack"
)

// New authenticates from explicit credentials — here an application
// credential — and ForRegion reaches a second region with the same token.
func ExampleNew() {
	cloud := startFakeCloud() // a fake Keystone; use your cloud's auth URL
	defer cloud.Close()

	ctx := context.Background()
	s, err := openstack.New(ctx, openstack.Config{
		AuthOptions: &gophercloud.AuthOptions{
			IdentityEndpoint:            cloud.URL + "/v3/",
			ApplicationCredentialID:     "0123456789abcdef0123456789abcdef",
			ApplicationCredentialSecret: "example-secret",
		},
		Region: "RegionOne",
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, region := range []*openstack.Session{s, s.ForRegion("RegionTwo")} {
		images, err := region.Image()
		if err != nil {
			fmt.Println(err)
			return
		}
		fmt.Println(region.Region(), strings.TrimPrefix(images.ResourceBaseURL(), cloud.URL))
	}
	// Output:
	// RegionOne /r1/image/v2/
	// RegionTwo /r2/image/v2/
}

// FromProvider wraps a ProviderClient the importer authenticated itself, with
// gophercloud or otherwise. Options pins a microversion.
func ExampleFromProvider() {
	cloud := startFakeCloud()
	defer cloud.Close()

	ctx := context.Background()
	pc, err := gcopenstack.AuthenticatedClient(ctx, gophercloud.AuthOptions{
		IdentityEndpoint: cloud.URL + "/v3/",
		Username:         "alice",
		Password:         "example-password",
		DomainName:       "Default",
		Scope:            &gophercloud.AuthScope{ProjectName: "demo", DomainName: "Default"},
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	s := openstack.FromProvider(pc, gophercloud.EndpointOpts{Region: "RegionOne"}, openstack.Options{
		Microversions: openstack.Microversions{Compute: "2.79"},
	})
	compute, err := s.Compute()
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(compute.Type, compute.Microversion, s.ScopedProjectID())
	// Output:
	// compute 2.79 aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa
}

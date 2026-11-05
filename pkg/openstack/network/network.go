// Package network is koc's neutron (network v2) layer: networks, subnets,
// ports, routers, floating IPs, security groups and their rules, tags,
// quotas and availability zones, the strict Find…ID lookups, and the
// With…Attrs adapters that add attributes gophercloud does not model to a
// request body.
//
// Every function takes the network client openstack.Session.Network returns
// and speaks gophercloud's types. Where gophercloud has no call, the function
// here sends it raw, so an importer and the koc CLI send the same requests.
//
// Errors are wrapped with what the call was doing and the project or
// resource it was given; gophercloud's error stays reachable through
// errors.As, so gophercloud.ResponseCodeIs keeps working on it.
package network

import (
	"context"
	"fmt"

	"github.com/gophercloud/gophercloud/v2"
)

// wrap prefixes err with what the call was doing; nil stays nil.
func wrap(err error, format string, args ...any) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf(format+": %w", append(args, err)...)
}

// getJSON GETs url into out, accepting only 200.
func getJSON(ctx context.Context, c *gophercloud.ServiceClient, url string, out any) error {
	resp, err := c.Get(ctx, url, out, &gophercloud.RequestOpts{OkCodes: []int{200}})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	return err
}

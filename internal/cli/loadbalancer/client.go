package loadbalancer

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
)

// newLoadBalancerClient authenticates once and derives the octavia
// (load-balancer v2) service client shared by every loadbalancer subcommand.
// Octavia versions via the URL, so the client carries no microversion header.
func newLoadBalancerClient(ctx context.Context, a *auth.Options) (*gophercloud.ServiceClient, error) {
	return a.NewServiceClient(ctx, (*auth.Client).LoadBalancer)
}

// newLoadBalancerSession is newLoadBalancerClient for commands that also need the
// authenticated Client to lazily derive a second service — neutron for
// --vip-subnet/--vip-network name resolution, keystone for --project.
func newLoadBalancerSession(ctx context.Context, a *auth.Options) (*gophercloud.ServiceClient, *auth.Client, error) {
	return a.NewServiceSession(ctx, (*auth.Client).LoadBalancer)
}

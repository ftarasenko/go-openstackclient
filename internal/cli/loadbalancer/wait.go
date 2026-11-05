package loadbalancer

import (
	"context"
	"time"

	"github.com/gophercloud/gophercloud/v2"

	loadbalancerapi "github.com/ftarasenko/go-openstackclient/pkg/openstack/loadbalancer"
)

// Octavia builds and tears down load balancers asynchronously: create returns
// PENDING_CREATE and delete leaves the record in PENDING_DELETE for a while, and
// no other write on the same load balancer is accepted until it settles. --wait
// polls provisioning_status the way baremetal/node_provision.go polls
// provision_state.
var (
	provisioningPollInterval = 5 * time.Second
	provisioningPollTimeout  = 15 * time.Minute
)

const statusActive = loadbalancerapi.ProvisioningActive

// waitForLoadBalancerActive waits up to timeout (the default when not
// positive) for the load balancer to become ACTIVE.
func waitForLoadBalancerActive(ctx context.Context, client *gophercloud.ServiceClient, id string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, pollTimeout(timeout))
	defer cancel()
	return loadbalancerapi.WaitForLoadBalancerActive(ctx, client, id, provisioningPollInterval)
}

// waitForLoadBalancerDeleted waits up to timeout (the default when not
// positive) for the load balancer to be gone.
func waitForLoadBalancerDeleted(ctx context.Context, client *gophercloud.ServiceClient, id string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, pollTimeout(timeout))
	defer cancel()
	return loadbalancerapi.WaitForLoadBalancerDeleted(ctx, client, id, provisioningPollInterval)
}

func pollTimeout(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		return provisioningPollTimeout
	}
	return timeout
}

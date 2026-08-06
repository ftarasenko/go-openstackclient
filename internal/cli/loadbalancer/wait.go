package loadbalancer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/loadbalancer/v2/loadbalancers"
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

// Octavia's terminal provisioning states.
const (
	statusActive  = "ACTIVE"
	statusError   = "ERROR"
	statusDeleted = "DELETED"
)

// maxConsecutiveGetErrors bounds how many CONSECUTIVE Get failures the poll
// tolerates before giving up; the counter resets on any success.
const maxConsecutiveGetErrors = 5

// waitForLoadBalancerActive polls until the load balancer reaches ACTIVE, or
// fails fast on ERROR rather than spinning until the timeout.
func waitForLoadBalancerActive(ctx context.Context, client *gophercloud.ServiceClient, id string, timeout time.Duration) error {
	_, err := pollProvisioning(ctx, client, id, timeout, func(status string, gone bool) (bool, error) {
		switch {
		case gone:
			return false, fmt.Errorf("load balancer %s disappeared while waiting for it to become %s", id, statusActive)
		case status == statusActive:
			return true, nil
		case status == statusError:
			return false, fmt.Errorf("load balancer %s entered %s", id, statusError)
		}
		return false, nil
	})
	return err
}

// waitForLoadBalancerDeleted polls until the load balancer is gone (404) or
// reports DELETED. A 404 is the success condition, not an error, so the caller
// gets a clean result once octavia has finished the teardown.
func waitForLoadBalancerDeleted(ctx context.Context, client *gophercloud.ServiceClient, id string, timeout time.Duration) error {
	_, err := pollProvisioning(ctx, client, id, timeout, func(status string, gone bool) (bool, error) {
		switch {
		case gone, status == statusDeleted:
			return true, nil
		case status == statusError:
			return false, fmt.Errorf("load balancer %s entered %s while being deleted", id, statusError)
		}
		return false, nil
	})
	return err
}

// pollProvisioning is the shared polling loop. done decides, from the current
// provisioning_status (or the fact that the load balancer is gone), whether to
// stop; it returns an error to fail fast on a terminal-but-wrong state.
func pollProvisioning(ctx context.Context, client *gophercloud.ServiceClient, id string, timeout time.Duration,
	done func(status string, gone bool) (bool, error),
) (string, error) {
	if timeout <= 0 {
		timeout = provisioningPollTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(provisioningPollInterval)
	defer ticker.Stop()

	var getErrors int
	var last string
	for {
		lb, err := loadbalancers.Get(ctx, client, id).Extract()
		switch {
		case err == nil:
			getErrors = 0
			last = lb.ProvisioningStatus
			stop, derr := done(lb.ProvisioningStatus, false)
			if derr != nil {
				return last, derr
			}
			if stop {
				return last, nil
			}
		case isNotFound(err):
			stop, derr := done("", true)
			if derr != nil {
				return last, derr
			}
			if stop {
				return last, nil
			}
		default:
			// Tolerate a few consecutive transient failures, but stop promptly if
			// the context itself is done.
			if ctx.Err() != nil {
				return last, fmt.Errorf("waiting for load balancer %s: %w", id, ctx.Err())
			}
			getErrors++
			if getErrors > maxConsecutiveGetErrors {
				return last, fmt.Errorf("polling load balancer %s: %w", id, err)
			}
		}
		select {
		case <-ctx.Done():
			if last != "" {
				return last, fmt.Errorf("waiting for load balancer %s (last provisioning_status %q): %w", id, last, ctx.Err())
			}
			return last, fmt.Errorf("waiting for load balancer %s: %w", id, ctx.Err())
		case <-ticker.C:
		}
	}
}

// isNotFound reports whether err is octavia's 404, which during a delete wait is
// the success signal rather than a failure.
func isNotFound(err error) bool {
	var notFound gophercloud.ErrUnexpectedResponseCode
	if errors.As(err, &notFound) {
		return notFound.Actual == 404
	}
	return strings.Contains(err.Error(), "404")
}

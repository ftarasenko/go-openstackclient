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

// Octavia's terminal provisioning states.
const (
	ProvisioningActive  = "ACTIVE"
	ProvisioningError   = "ERROR"
	ProvisioningDeleted = "DELETED"
)

// maxConsecutiveGetErrors is how many failed reads in a row a wait tolerates;
// any successful read resets the count.
const maxConsecutiveGetErrors = 5

// WaitForLoadBalancerActive polls a load balancer every interval until it is
// ACTIVE, until it is ERROR or gone (an error), or until ctx is done. ctx's
// deadline is the timeout.
func WaitForLoadBalancerActive(ctx context.Context, c *gophercloud.ServiceClient, id string, interval time.Duration) error {
	return pollProvisioning(ctx, c, id, interval, func(status string, gone bool) (bool, error) {
		switch {
		case gone:
			return false, fmt.Errorf("load balancer %s disappeared while waiting for it to become %s", id, ProvisioningActive)
		case status == ProvisioningActive:
			return true, nil
		case status == ProvisioningError:
			return false, fmt.Errorf("load balancer %s entered %s", id, ProvisioningError)
		}
		return false, nil
	})
}

// WaitForLoadBalancerDeleted polls a load balancer every interval until it
// is gone (a 404) or DELETED, until it is ERROR (an error), or until ctx is
// done. ctx's deadline is the timeout.
func WaitForLoadBalancerDeleted(ctx context.Context, c *gophercloud.ServiceClient, id string, interval time.Duration) error {
	return pollProvisioning(ctx, c, id, interval, func(status string, gone bool) (bool, error) {
		switch {
		case gone, status == ProvisioningDeleted:
			return true, nil
		case status == ProvisioningError:
			return false, fmt.Errorf("load balancer %s entered %s while being deleted", id, ProvisioningError)
		}
		return false, nil
	})
}

// pollProvisioning reads the load balancer until done says stop or fails. A
// failed read is retried up to maxConsecutiveGetErrors times in a row.
func pollProvisioning(ctx context.Context, c *gophercloud.ServiceClient, id string, interval time.Duration,
	done func(status string, gone bool) (bool, error),
) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var getErrors int
	var last string
	for {
		status, gone, err := getStatus(loadbalancers.Get(ctx, c, id).Extract())
		if err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("waiting for load balancer %s%s: %w", id, lastStatus(last), ctx.Err())
			}
			getErrors++
			if getErrors > maxConsecutiveGetErrors {
				return fmt.Errorf("polling load balancer %s%s: %w", id, lastStatus(last), err)
			}
		} else {
			if !gone {
				getErrors = 0
				last = status
			}
			stop, derr := done(status, gone)
			if derr != nil || stop {
				return derr
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for load balancer %s%s: %w", id, lastStatus(last), ctx.Err())
		case <-ticker.C:
		}
	}
}

// getStatus maps one Get onto (status, gone): a 404 is gone, not an error.
func getStatus(lb *loadbalancers.LoadBalancer, err error) (status string, gone bool, _ error) {
	switch {
	case err == nil:
		return lb.ProvisioningStatus, false, nil
	case isNotFound(err):
		return "", true, nil
	}
	return "", false, err
}

// lastStatus names the last provisioning_status seen, for a wait that gave
// up.
func lastStatus(last string) string {
	if last == "" {
		return ""
	}
	return fmt.Sprintf(" (last provisioning_status %q)", last)
}

// isNotFound reports a 404. The text fallback, for a 404 a transport wrapped
// out of its type, matches gophercloud's "got 404" rather than the digits,
// which a URL's port or ID can contain.
func isNotFound(err error) bool {
	var code gophercloud.ErrUnexpectedResponseCode
	if errors.As(err, &code) {
		return code.Actual == 404
	}
	return strings.Contains(err.Error(), "got 404")
}

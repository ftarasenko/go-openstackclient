package loadbalancer

import (
	"errors"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/loadbalancer/v2/loadbalancers"
)

// getStatus is the mapping pollProvisioning applies to every Get before handing
// the result to a done callback. The 404 arm is the one that matters most: a
// delete wait treats it as success, so misclassifying it as an error would turn
// a completed teardown into a failure.
func TestGetStatus(t *testing.T) {
	tests := []struct {
		name       string
		lb         *loadbalancers.LoadBalancer
		err        error
		wantStatus string
		wantGone   bool
		wantErr    error
	}{
		{
			name:       "a successful get yields the provisioning status",
			lb:         &loadbalancers.LoadBalancer{ProvisioningStatus: statusActive},
			wantStatus: statusActive,
		},
		{
			name:       "a pending status is passed through unchanged",
			lb:         &loadbalancers.LoadBalancer{ProvisioningStatus: "PENDING_CREATE"},
			wantStatus: "PENDING_CREATE",
		},
		{
			name:     "a typed 404 means gone, not an error",
			err:      gophercloud.ErrUnexpectedResponseCode{Actual: 404},
			wantGone: true,
		},
		{
			// isNotFound falls back to the error text when the 404 is not a typed
			// gophercloud response code (e.g. wrapped by a transport).
			name:     "an untyped 404 still means gone",
			err:      errors.New(`Expected HTTP response code [200] when accessing URL; got 404`),
			wantGone: true,
		},
		{
			name:    "a 500 is a real error",
			err:     gophercloud.ErrUnexpectedResponseCode{Actual: 500},
			wantErr: gophercloud.ErrUnexpectedResponseCode{Actual: 500},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, gone, err := getStatus(tt.lb, tt.err)
			if tt.wantErr != nil {
				if err == nil || err.Error() != tt.wantErr.Error() {
					t.Fatalf("getStatus() error = %v, want %v", err, tt.wantErr)
				}
			} else if err != nil {
				t.Fatalf("getStatus() unexpected error: %v", err)
			}
			if status != tt.wantStatus {
				t.Fatalf("getStatus() status = %q, want %q", status, tt.wantStatus)
			}
			if gone != tt.wantGone {
				t.Fatalf("getStatus() gone = %v, want %v", gone, tt.wantGone)
			}
		})
	}
}

package loadbalancer_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	th "github.com/gophercloud/gophercloud/v2/testhelper"
	fakeclient "github.com/gophercloud/gophercloud/v2/testhelper/client"

	"github.com/ftarasenko/go-openstackclient/pkg/openstack/loadbalancer"
)

// sequenced answers successive GETs of the load balancer from seq: a
// provisioning status, "404" or "500". The last entry repeats.
func sequenced(t *testing.T, seq ...string) (*int, *gophercloud.ServiceClient) {
	t.Helper()
	fs := th.SetupHTTP()
	t.Cleanup(fs.Teardown)
	calls := 0
	fs.Mux.HandleFunc("/v2.0/lbaas/loadbalancers/"+lbID, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("wait sent %s", r.Method)
		}
		s := seq[min(calls, len(seq)-1)]
		calls++
		w.Header().Set("Content-Type", "application/json")
		switch s {
		case "404":
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{}`)
		case "500":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{}`)
		default:
			_, _ = io.WriteString(w, `{"loadbalancer":{"id":"`+lbID+`","provisioning_status":"`+s+`"}}`)
		}
	})
	sc := fakeclient.ServiceClient(fs)
	sc.Type = "load-balancer"
	sc.ResourceBase = sc.Endpoint + "v2.0/"
	return &calls, sc
}

func TestWaitForLoadBalancerActive(t *testing.T) {
	tests := []struct {
		name  string
		seq   []string
		calls int
		err   string
	}{
		{"active at once", []string{"ACTIVE"}, 1, ""},
		{"pending then active", []string{"PENDING_CREATE", "PENDING_CREATE", "ACTIVE"}, 3, ""},
		{"transient errors are retried", []string{"500", "500", "ACTIVE"}, 3, ""},
		{"error state", []string{"PENDING_UPDATE", "ERROR"}, 2, "load balancer " + lbID + " entered ERROR"},
		{"gone", []string{"404"}, 1, "load balancer " + lbID + " disappeared while waiting for it to become ACTIVE"},
		{"too many errors", []string{"PENDING_UPDATE", "500"}, 7, `polling load balancer ` + lbID + ` (last provisioning_status "PENDING_UPDATE"): `},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls, sc := sequenced(t, tt.seq...)
			err := loadbalancer.WaitForLoadBalancerActive(context.Background(), sc, lbID, time.Millisecond)
			if (tt.err == "") != (err == nil) || (err != nil && !strings.HasPrefix(err.Error(), tt.err)) {
				t.Errorf("error = %v; want %q", err, tt.err)
			}
			if *calls != tt.calls {
				t.Errorf("polled %d times; want %d", *calls, tt.calls)
			}
		})
	}
}

func TestWaitForLoadBalancerDeleted(t *testing.T) {
	tests := []struct {
		name string
		seq  []string
		err  string
	}{
		{"gone", []string{"PENDING_DELETE", "404"}, ""},
		{"deleted state", []string{"DELETED"}, ""},
		{"error state", []string{"PENDING_DELETE", "ERROR"}, "load balancer " + lbID + " entered ERROR while being deleted"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, sc := sequenced(t, tt.seq...)
			err := loadbalancer.WaitForLoadBalancerDeleted(context.Background(), sc, lbID, time.Millisecond)
			if (tt.err == "") != (err == nil) || (err != nil && err.Error() != tt.err) {
				t.Errorf("error = %v; want %q", err, tt.err)
			}
		})
	}
}

// ctx's deadline is the timeout, and the error names the last status seen.
func TestWaitDeadline(t *testing.T) {
	_, sc := sequenced(t, "PENDING_UPDATE")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := loadbalancer.WaitForLoadBalancerActive(ctx, sc, lbID, 5*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) ||
		!strings.HasPrefix(err.Error(), `waiting for load balancer `+lbID+` (last provisioning_status "PENDING_UPDATE"): `) {
		t.Errorf("error = %v; want the deadline with the last status", err)
	}
}

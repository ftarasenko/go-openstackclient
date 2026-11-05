package volume

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"
)

// StoragePool is one scheduler pool and the capabilities its driver reports.
// The keys vary by driver; capacities are numbers, or "infinite"/"unknown".
type StoragePool struct {
	Name         string         `json:"name"`
	Capabilities map[string]any `json:"capabilities"`
}

// ListStoragePools lists the scheduler's pools with their capabilities
// (admin; GET /scheduler-stats/get_pools?detail=True).
func ListStoragePools(ctx context.Context, c *gophercloud.ServiceClient) ([]StoragePool, error) {
	var resp struct {
		Pools []StoragePool `json:"pools"`
	}
	// Without detail=True cinder returns bare names.
	err := getJSON(ctx, c, c.ServiceURL("scheduler-stats", "get_pools")+"?detail=True", &resp)
	if err != nil {
		return nil, wrap(err, "listing storage pools")
	}
	return resp.Pools, nil
}

// GetBackendCapabilities returns what the driver of host ("<host>" or
// "<host>@<backend>") reports, verbatim (admin).
func GetBackendCapabilities(ctx context.Context, c *gophercloud.ServiceClient, host string) (map[string]any, error) {
	var resp map[string]any
	if err := getJSON(ctx, c, c.ServiceURL("capabilities", host), &resp); err != nil {
		return nil, wrap(err, "showing capabilities of backend %s", host)
	}
	return resp, nil
}

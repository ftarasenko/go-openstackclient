package dns

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gophercloud/gophercloud/v2"
)

// GetLimits reads designate's limits, keyed by name.
func GetLimits(ctx context.Context, c *gophercloud.ServiceClient) (map[string]any, error) {
	var limits map[string]any
	if err := getJSON(ctx, c, c.ServiceURL("limits"), &limits); err != nil {
		return nil, wrap(err, "listing DNS limits")
	}
	return limits, nil
}

// Pool is a designate pool.
type Pool struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	ProjectID   string            `json:"project_id"`
	Attributes  map[string]string `json:"attributes"`
	NS          []PoolNS          `json:"ns_records"`
	CreatedAt   string            `json:"created_at"`
	UpdatedAt   string            `json:"updated_at"`
}

// PoolNS is one nameserver of a pool or a zone.
type PoolNS struct {
	Hostname string `json:"hostname"`
	Priority int    `json:"priority"`
}

// listPools is the unwrapped listing FindPoolID shares.
func listPools(ctx context.Context, c *gophercloud.ServiceClient, name string, limit int) ([]Pool, error) {
	q, err := query(struct {
		Name string `q:"name"`
	}{name})
	if err != nil {
		return nil, err
	}
	return listAll(ctx, c, c.ServiceURL("pools")+q, limit,
		func(raw json.RawMessage) ([]Pool, string, error) {
			var page struct {
				Pools []Pool `json:"pools"`
				Links links  `json:"links"`
			}
			if err := json.Unmarshal(raw, &page); err != nil {
				return nil, "", fmt.Errorf("parsing pool list: %w", err)
			}
			return page.Pools, page.Links.Next, nil
		})
}

// ListPools lists pools, filtered by exact name when name is set, stopping
// after limit results (0 = no cap).
func ListPools(ctx context.Context, c *gophercloud.ServiceClient, name string, limit int) ([]Pool, error) {
	all, err := listPools(ctx, c, name, limit)
	return all, wrap(err, "listing DNS pools")
}

// FindPoolID resolves a pool name or ID to an ID.
func FindPoolID(ctx context.Context, c *gophercloud.ServiceClient, ref string) (string, error) {
	return findID(ctx, "DNS pool", ref, func(ctx context.Context) ([]Pool, error) {
		return listPools(ctx, c, ref, 0)
	}, func(p Pool) string { return p.ID })
}

// GetPool reads one pool.
func GetPool(ctx context.Context, c *gophercloud.ServiceClient, id string) (*Pool, error) {
	var p Pool
	if err := getJSON(ctx, c, c.ServiceURL("pools", id), &p); err != nil {
		return nil, wrap(err, "showing DNS pool %q", id)
	}
	return &p, nil
}

// PTRRecord is the reverse-DNS record of a floating IP. Its ID is
// "<region>:<floating-ip-id>".
type PTRRecord struct {
	ID          string `json:"id"`
	PTRDName    string `json:"ptrdname"`
	Description string `json:"description"`
	Address     string `json:"address"`
	TTL         *int   `json:"ttl"`
	Status      string `json:"status"`
	Action      string `json:"action"`
}

// ListPTRRecords lists floating-IP PTR records, stopping after limit results
// (0 = no cap).
func ListPTRRecords(ctx context.Context, c *gophercloud.ServiceClient, limit int) ([]PTRRecord, error) {
	all, err := listAll(ctx, c, c.ServiceURL("reverse", "floatingips"), limit,
		func(raw json.RawMessage) ([]PTRRecord, string, error) {
			var page struct {
				FloatingIPs []PTRRecord `json:"floatingips"`
				Links       links       `json:"links"`
			}
			if err := json.Unmarshal(raw, &page); err != nil {
				return nil, "", fmt.Errorf("parsing PTR record list: %w", err)
			}
			return page.FloatingIPs, page.Links.Next, nil
		})
	return all, wrap(err, "listing PTR records")
}

// GetPTRRecord reads one floating IP's PTR record.
func GetPTRRecord(ctx context.Context, c *gophercloud.ServiceClient, id string) (*PTRRecord, error) {
	var r PTRRecord
	if err := getJSON(ctx, c, c.ServiceURL("reverse", "floatingips", id), &r); err != nil {
		return nil, wrap(err, "showing PTR record for floating IP %s", id)
	}
	return &r, nil
}

// SetPTRRecord patches a floating IP's PTR record with body's keys
// (ptrdname, description, ttl); a nil value clears that key.
func SetPTRRecord(ctx context.Context, c *gophercloud.ServiceClient, id string, body map[string]any) (*PTRRecord, error) {
	var r PTRRecord
	if err := patchJSON(ctx, c, c.ServiceURL("reverse", "floatingips", id), body, &r); err != nil {
		return nil, wrap(err, "setting PTR record for floating IP %s", id)
	}
	return &r, nil
}

// UnsetPTRRecord removes a floating IP's PTR record.
func UnsetPTRRecord(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	err := patchJSON(ctx, c, c.ServiceURL("reverse", "floatingips", id), map[string]any{"ptrdname": nil}, nil)
	return wrap(err, "unsetting PTR record for floating IP %s", id)
}

// ServiceStatus is one designate service's heartbeat.
type ServiceStatus struct {
	ID            string         `json:"id"`
	Hostname      string         `json:"hostname"`
	ServiceName   string         `json:"service_name"`
	Status        string         `json:"status"`
	Stats         map[string]any `json:"stats"`
	Capabilities  map[string]any `json:"capabilities"`
	HeartbeatedAt string         `json:"heartbeated_at"`
}

// ServiceStatusListOpts filters ListServiceStatuses.
type ServiceStatusListOpts struct {
	Hostname    string `q:"hostname"`
	ServiceName string `q:"service_name"`
	Status      string `q:"status"`
}

// ListServiceStatuses lists designate's services, stopping after limit
// results (0 = no cap).
func ListServiceStatuses(ctx context.Context, c *gophercloud.ServiceClient, opts ServiceStatusListOpts,
	limit int,
) ([]ServiceStatus, error) {
	q, err := query(opts)
	if err != nil {
		return nil, err
	}
	all, err := listAll(ctx, c, c.ServiceURL("service_statuses")+q, limit,
		func(raw json.RawMessage) ([]ServiceStatus, string, error) {
			var page struct {
				Statuses []ServiceStatus `json:"service_statuses"`
				Links    links           `json:"links"`
			}
			if err := json.Unmarshal(raw, &page); err != nil {
				return nil, "", fmt.Errorf("parsing service status list: %w", err)
			}
			return page.Statuses, page.Links.Next, nil
		})
	return all, wrap(err, "listing DNS service statuses")
}

// GetServiceStatus reads one service's status.
func GetServiceStatus(ctx context.Context, c *gophercloud.ServiceClient, id string) (*ServiceStatus, error) {
	var s ServiceStatus
	if err := getJSON(ctx, c, c.ServiceURL("service_statuses", id), &s); err != nil {
		return nil, wrap(err, "showing DNS service status %s", id)
	}
	return &s, nil
}

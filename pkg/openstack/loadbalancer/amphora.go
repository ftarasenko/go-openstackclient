package loadbalancer

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/loadbalancer/v2/amphorae"
)

// Amphorae have no name, so they are addressed by ID. Configure, delete and
// stats have no typed call and are raw requests under /v2.0/octavia/.

// ListAmphorae lists every amphora matching opts.
func ListAmphorae(ctx context.Context, c *gophercloud.ServiceClient, opts amphorae.ListOptsBuilder) ([]amphorae.Amphora, error) {
	pages, err := amphorae.List(c, opts).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing amphorae")
	}
	all, err := amphorae.ExtractAmphorae(pages)
	return all, wrap(err, "parsing amphora list")
}

// GetAmphora reads one amphora.
func GetAmphora(ctx context.Context, c *gophercloud.ServiceClient, id string) (*amphorae.Amphora, error) {
	am, err := amphorae.Get(ctx, c, id).Extract()
	return am, wrap(err, "getting amphora %s", id)
}

// FailoverAmphora requests an amphora's failover.
func FailoverAmphora(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(amphorae.Failover(ctx, c, id).ExtractErr(), "failing over amphora %s", id)
}

// ConfigureAmphora asks octavia to push its current configuration to an
// amphora.
func ConfigureAmphora(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	resp, err := c.Put(ctx, c.ServiceURL("octavia", "amphorae", id, "config"), nil, nil,
		&gophercloud.RequestOpts{OkCodes: []int{202}})
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	_, _, err = gophercloud.ParseResponse(resp, err)
	return wrap(err, "configuring amphora %s", id)
}

// DeleteAmphora deletes an amphora.
func DeleteAmphora(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	resp, err := c.Delete(ctx, c.ServiceURL("octavia", "amphorae", id), &gophercloud.RequestOpts{
		OkCodes: []int{204},
	})
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	_, _, err = gophercloud.ParseResponse(resp, err)
	return wrap(err, "deleting amphora %s", id)
}

// AmphoraListenerStats is one listener's counters on an amphora.
type AmphoraListenerStats struct {
	ListenerID        string `json:"listener_id"`
	LoadbalancerID    string `json:"loadbalancer_id"`
	ID                string `json:"id"`
	ActiveConnections int    `json:"active_connections"`
	BytesIn           int    `json:"bytes_in"`
	BytesOut          int    `json:"bytes_out"`
	RequestErrors     int    `json:"request_errors"`
	TotalConnections  int    `json:"total_connections"`
}

// GetAmphoraStats reads an amphora's counters, one entry per listener.
func GetAmphoraStats(ctx context.Context, c *gophercloud.ServiceClient, id string) ([]AmphoraListenerStats, error) {
	var body struct {
		Stats []AmphoraListenerStats `json:"amphora_stats"`
	}
	resp, err := c.Get(ctx, c.ServiceURL("octavia", "amphorae", id, "stats"), &body, nil)
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if _, _, err = gophercloud.ParseResponse(resp, err); err != nil {
		return nil, wrap(err, "getting statistics for amphora %s", id)
	}
	return body.Stats, nil
}

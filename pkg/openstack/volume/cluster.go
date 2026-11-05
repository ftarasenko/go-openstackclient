package volume

import (
	"context"
	"net/url"
	"strconv"

	"github.com/gophercloud/gophercloud/v2"
)

// ClusterMicroversion is where cinder added clusters; below it /clusters
// does not exist.
const ClusterMicroversion = "3.7"

// Cluster is a group of cinder services serving the same backend. Fields
// after Status come only from the detail listing, show, enable and disable.
// Timestamps are cinder's own strings. gophercloud has no clusters package,
// so the calls on it are raw.
type Cluster struct {
	Name              string `json:"name"`
	Binary            string `json:"binary"`
	State             string `json:"state"`
	Status            string `json:"status"`
	DisabledReason    string `json:"disabled_reason"`
	NumHosts          int    `json:"num_hosts"`
	NumDownHosts      int    `json:"num_down_hosts"`
	LastHeartbeat     string `json:"last_heartbeat"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
	ReplicationStatus string `json:"replication_status"`
	Frozen            bool   `json:"frozen"`
	ActiveBackendID   string `json:"active_backend_id"`
}

// ClusterListOpts filters a cluster listing. A nil pointer leaves its
// filter off. Detail reads /clusters/detail, which fills every field.
type ClusterListOpts struct {
	Name         string
	Binary       string
	IsUp         *bool
	Disabled     *bool
	NumHosts     *int
	NumDownHosts *int
	Detail       bool
}

func (o ClusterListOpts) query() string {
	q := url.Values{}
	if o.Name != "" {
		q.Set("name", o.Name)
	}
	if o.Binary != "" {
		q.Set("binary", o.Binary)
	}
	if o.IsUp != nil {
		q.Set("is_up", strconv.FormatBool(*o.IsUp))
	}
	if o.Disabled != nil {
		q.Set("disabled", strconv.FormatBool(*o.Disabled))
	}
	if o.NumHosts != nil {
		q.Set("num_hosts", strconv.Itoa(*o.NumHosts))
	}
	if o.NumDownHosts != nil {
		q.Set("num_down_hosts", strconv.Itoa(*o.NumDownHosts))
	}
	if s := q.Encode(); s != "" {
		return "?" + s
	}
	return ""
}

// ListClusters lists the clusters opts selects (admin, ClusterMicroversion).
func ListClusters(ctx context.Context, c *gophercloud.ServiceClient, opts ClusterListOpts) ([]Cluster, error) {
	u := c.ServiceURL("clusters")
	if opts.Detail {
		u = c.ServiceURL("clusters", "detail")
	}
	var resp struct {
		Clusters []Cluster `json:"clusters"`
	}
	if err := getJSON(ctx, c, u+opts.query(), &resp); err != nil {
		return nil, wrap(err, "listing block storage clusters")
	}
	return resp.Clusters, nil
}

// GetCluster returns the named cluster; binary, when set, picks one of
// several that share the name (admin, ClusterMicroversion).
func GetCluster(ctx context.Context, c *gophercloud.ServiceClient, name, binary string) (*Cluster, error) {
	u := c.ServiceURL("clusters", name)
	if binary != "" {
		u += "?" + url.Values{"binary": {binary}}.Encode()
	}
	var resp struct {
		Cluster Cluster `json:"cluster"`
	}
	if err := getJSON(ctx, c, u, &resp); err != nil {
		return nil, wrap(err, "showing block storage cluster %q", name)
	}
	return &resp.Cluster, nil
}

// EnableCluster enables the cluster and returns it as updated (admin,
// ClusterMicroversion).
func EnableCluster(ctx context.Context, c *gophercloud.ServiceClient, name, binary string) (*Cluster, error) {
	cl, err := setCluster(ctx, c, "enable", name, binary, "")
	return cl, wrap(err, "enabling block storage cluster %q", name)
}

// DisableCluster disables the cluster, recording reason when set, and
// returns it as updated (admin, ClusterMicroversion).
func DisableCluster(ctx context.Context, c *gophercloud.ServiceClient, name, binary, reason string) (*Cluster, error) {
	cl, err := setCluster(ctx, c, "disable", name, binary, reason)
	return cl, wrap(err, "disabling block storage cluster %q", name)
}

func setCluster(ctx context.Context, c *gophercloud.ServiceClient, action, name, binary, reason string) (*Cluster, error) {
	body := map[string]any{"name": name}
	if binary != "" {
		body["binary"] = binary
	}
	if reason != "" {
		body["disabled_reason"] = reason
	}
	var resp struct {
		Cluster Cluster `json:"cluster"`
	}
	if err := putJSON(ctx, c, c.ServiceURL("clusters", action), body, &resp); err != nil {
		return nil, err
	}
	return &resp.Cluster, nil
}

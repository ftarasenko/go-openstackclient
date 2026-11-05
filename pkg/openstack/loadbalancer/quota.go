package loadbalancer

import (
	"context"
	"net/url"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/loadbalancer/v2/quotas"
)

// Quotas live at /v2.0/quotas, the prefix gophercloud's typed calls use;
// octavia also serves /v2.0/lbaas/quotas. The listing, the defaults and the
// per-key unset have no typed call and are raw requests on the same prefix.

// ProjectQuota is one project's row of the quota listing. Its fields are
// pointers, nil where the deployment omits the key, and carry both spellings
// octavia has used: quotas.Quota cannot be embedded, since its UnmarshalJSON
// would swallow project_id.
type ProjectQuota struct {
	ProjectID string `json:"project_id"`

	Loadbalancer *int `json:"loadbalancer"`
	LoadBalancer *int `json:"load_balancer"` // legacy spelling

	Listener      *int `json:"listener"`
	Pool          *int `json:"pool"`
	Member        *int `json:"member"`
	Healthmonitor *int `json:"healthmonitor"`
	HealthMonitor *int `json:"health_monitor"` // legacy spelling
	L7Policy      *int `json:"l7policy"`
	L7Rule        *int `json:"l7rule"`
}

// ListQuotas lists the projects' quotas, or one project's when projectID is
// set, following quotas_links to the last page.
func ListQuotas(ctx context.Context, c *gophercloud.ServiceClient, projectID string) ([]ProjectQuota, error) {
	next := c.ServiceURL("quotas")
	if projectID != "" {
		next += "?project_id=" + url.QueryEscape(projectID)
	}
	var all []ProjectQuota
	seen := map[string]bool{}
	for next != "" {
		var page struct {
			Quotas []ProjectQuota `json:"quotas"`
			Links  []struct {
				Rel  string `json:"rel"`
				HRef string `json:"href"`
			} `json:"quotas_links"`
		}
		resp, err := c.Get(ctx, next, &page, nil)
		if resp != nil {
			_ = resp.Body.Close()
		}
		if _, _, err = gophercloud.ParseResponse(resp, err); err != nil {
			return nil, wrap(err, "listing load balancer quotas")
		}
		all = append(all, page.Quotas...)
		seen[next] = true
		next = ""
		for _, l := range page.Links {
			// A server that echoes the current page as "next" would loop.
			if l.Rel == "next" && l.HRef != "" && !seen[l.HRef] {
				next = l.HRef
			}
		}
	}
	return all, nil
}

// GetQuota reads a project's quotas.
func GetQuota(ctx context.Context, c *gophercloud.ServiceClient, projectID string) (*quotas.Quota, error) {
	q, err := quotas.Get(ctx, c, projectID).Extract()
	return q, wrap(err, "getting load balancer quotas for project %s", projectID)
}

// UpdateQuota sets a project's quotas; -1 is unlimited.
func UpdateQuota(ctx context.Context, c *gophercloud.ServiceClient, projectID string, opts quotas.UpdateOptsBuilder) (*quotas.Quota, error) {
	q, err := quotas.Update(ctx, c, projectID, opts).Extract()
	return q, wrap(err, "setting load balancer quotas for project %s", projectID)
}

// UnsetQuotas reverts the named quotas ("loadbalancer", "listener", …) of a
// project to the deployment's defaults by sending each as null, which
// quotas.UpdateOpts cannot.
func UnsetQuotas(ctx context.Context, c *gophercloud.ServiceClient, projectID string, names []string) (*quotas.Quota, error) {
	quota := make(map[string]any, len(names))
	for _, n := range names {
		quota[n] = nil
	}
	var body struct {
		Quota quotas.Quota `json:"quota"`
	}
	resp, err := c.Put(ctx, c.ServiceURL("quotas", projectID), map[string]any{"quota": quota}, &body,
		&gophercloud.RequestOpts{OkCodes: []int{200, 202}})
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if _, _, err = gophercloud.ParseResponse(resp, err); err != nil {
		return nil, wrap(err, "unsetting load balancer quotas %v for project %s", names, projectID)
	}
	return &body.Quota, nil
}

// ResetQuota reverts all of a project's quotas to the defaults.
func ResetQuota(ctx context.Context, c *gophercloud.ServiceClient, projectID string) error {
	return wrap(quotas.Delete(ctx, c, projectID).ExtractErr(), "resetting load balancer quotas for project %s", projectID)
}

// GetDefaultQuota reads the deployment's default quotas.
func GetDefaultQuota(ctx context.Context, c *gophercloud.ServiceClient) (*quotas.Quota, error) {
	var body struct {
		Quota quotas.Quota `json:"quota"`
	}
	resp, err := c.Get(ctx, c.ServiceURL("quotas", "defaults"), &body, nil)
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if _, _, err = gophercloud.ParseResponse(resp, err); err != nil {
		return nil, wrap(err, "getting default load balancer quotas")
	}
	return &body.Quota, nil
}

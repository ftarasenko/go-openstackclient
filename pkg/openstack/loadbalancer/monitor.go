package loadbalancer

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/loadbalancer/v2/monitors"
)

// FindHealthMonitorID resolves a health monitor name or ID to an ID.
func FindHealthMonitorID(ctx context.Context, c *gophercloud.ServiceClient, ref string) (string, error) {
	return findID(ctx, "health monitor", ref, func(ctx context.Context) ([]monitors.Monitor, error) {
		pages, err := monitors.List(c, monitors.ListOpts{Name: ref}).AllPages(ctx)
		return extracted(pages, err, monitors.ExtractMonitors)
	}, func(m monitors.Monitor) string { return m.ID })
}

// ListHealthMonitors lists every health monitor matching opts.
func ListHealthMonitors(ctx context.Context, c *gophercloud.ServiceClient, opts monitors.ListOptsBuilder) ([]monitors.Monitor, error) {
	pages, err := monitors.List(c, opts).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing health monitors")
	}
	all, err := monitors.ExtractMonitors(pages)
	return all, wrap(err, "parsing health monitor list")
}

// GetHealthMonitor reads one health monitor.
func GetHealthMonitor(ctx context.Context, c *gophercloud.ServiceClient, id string) (*monitors.Monitor, error) {
	m, err := monitors.Get(ctx, c, id).Extract()
	return m, wrap(err, "getting health monitor %s", id)
}

// CreateHealthMonitor creates a pool's health monitor.
func CreateHealthMonitor(ctx context.Context, c *gophercloud.ServiceClient, opts monitors.CreateOptsBuilder) (*monitors.Monitor, error) {
	m, err := monitors.Create(ctx, c, opts).Extract()
	return m, wrap(err, "creating health monitor")
}

// UpdateHealthMonitor updates a health monitor.
func UpdateHealthMonitor(ctx context.Context, c *gophercloud.ServiceClient, id string, opts monitors.UpdateOptsBuilder) (*monitors.Monitor, error) {
	m, err := monitors.Update(ctx, c, id, opts).Extract()
	return m, wrap(err, "updating health monitor %s", id)
}

// DeleteHealthMonitor deletes a health monitor.
func DeleteHealthMonitor(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(monitors.Delete(ctx, c, id).ExtractErr(), "deleting health monitor %s", id)
}

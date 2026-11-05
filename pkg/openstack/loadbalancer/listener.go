package loadbalancer

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/loadbalancer/v2/listeners"
)

// FindListenerID resolves a listener name or ID to an ID.
func FindListenerID(ctx context.Context, c *gophercloud.ServiceClient, ref string) (string, error) {
	return findID(ctx, "listener", ref, func(ctx context.Context) ([]listeners.Listener, error) {
		pages, err := listeners.List(c, listeners.ListOpts{Name: ref}).AllPages(ctx)
		return extracted(pages, err, listeners.ExtractListeners)
	}, func(l listeners.Listener) string { return l.ID })
}

// ListListeners lists every listener matching opts.
func ListListeners(ctx context.Context, c *gophercloud.ServiceClient, opts listeners.ListOptsBuilder) ([]listeners.Listener, error) {
	pages, err := listeners.List(c, opts).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing listeners")
	}
	all, err := listeners.ExtractListeners(pages)
	return all, wrap(err, "parsing listener list")
}

// GetListener reads one listener.
func GetListener(ctx context.Context, c *gophercloud.ServiceClient, id string) (*listeners.Listener, error) {
	l, err := listeners.Get(ctx, c, id).Extract()
	return l, wrap(err, "getting listener %s", id)
}

// CreateListener creates a listener on a load balancer.
func CreateListener(ctx context.Context, c *gophercloud.ServiceClient, opts listeners.CreateOptsBuilder) (*listeners.Listener, error) {
	l, err := listeners.Create(ctx, c, opts).Extract()
	return l, wrap(err, "creating listener")
}

// UpdateListener updates a listener.
func UpdateListener(ctx context.Context, c *gophercloud.ServiceClient, id string, opts listeners.UpdateOptsBuilder) (*listeners.Listener, error) {
	l, err := listeners.Update(ctx, c, id, opts).Extract()
	return l, wrap(err, "updating listener %s", id)
}

// DeleteListener deletes a listener.
func DeleteListener(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(listeners.Delete(ctx, c, id).ExtractErr(), "deleting listener %s", id)
}

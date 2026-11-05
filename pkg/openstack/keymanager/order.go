package keymanager

import (
	"context"
	"fmt"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/keymanager/v1/orders"
)

// ListOrders lists one window of orders: opts.Limit entries from opts.Offset.
func ListOrders(ctx context.Context, c *gophercloud.ServiceClient, opts orders.ListOptsBuilder) ([]orders.Order, error) {
	page, err := firstPage(ctx, orders.List(c, opts))
	if err != nil || page == nil {
		return nil, wrap(err, "listing orders")
	}
	all, err := orders.ExtractOrders(page)
	return all, wrap(err, "parsing order list")
}

// GetOrder returns an order.
func GetOrder(ctx context.Context, c *gophercloud.ServiceClient, id string) (*orders.Order, error) {
	r, err := orders.Get(ctx, c, id).Extract()
	return r, wrap(err, "getting order %s", id)
}

// CreateOrder asks barbican to generate a secret (key) or key pair
// (asymmetric) and returns the order's UUID.
func CreateOrder(ctx context.Context, c *gophercloud.ServiceClient, opts orders.CreateOptsBuilder) (string, error) {
	created, err := orders.Create(ctx, c, opts).Extract()
	if err != nil {
		return "", wrap(err, "creating order")
	}
	id, err := RefUUID(created.OrderRef, "Order")
	if err != nil {
		return "", fmt.Errorf("creating order: barbican answered order_ref %q: %w", created.OrderRef, err)
	}
	return id, nil
}

// DeleteOrder deletes an order; what it generated stays.
func DeleteOrder(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(orders.Delete(ctx, c, id).ExtractErr(), "deleting order %s", id)
}

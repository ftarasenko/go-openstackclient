package baremetal

import (
	"context"
	"net/http"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/baremetalintrospection/v1/introspection"

	"github.com/ftarasenko/koc/pkg/openstack/paging"
)

// The introspection functions take the ironic-inspector client
// (openstack.Session.Introspection), not ironic's.

// StartIntrospection starts in-band inspection of a node. It does not use
// introspection.StartIntrospection, which builds the manage_boot query and
// then POSTs to the bare URL.
func StartIntrospection(ctx context.Context, c *gophercloud.ServiceClient, nodeID string,
	opts introspection.StartOpts,
) error {
	query, err := opts.ToStartIntrospectionQuery()
	if err != nil {
		return wrap(err, "starting introspection of node %s", nodeID)
	}
	resp, err := c.Post(ctx, c.ServiceURL("introspection", nodeID)+query, nil, nil,
		&gophercloud.RequestOpts{OkCodes: []int{http.StatusAccepted}})
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	_, _, err = gophercloud.ParseResponse(resp, err)
	return wrap(err, "starting introspection of node %s", nodeID)
}

// GetIntrospectionStatus reads a node's introspection status.
func GetIntrospectionStatus(ctx context.Context, c *gophercloud.ServiceClient, nodeID string) (*introspection.Introspection, error) {
	st, err := introspection.GetIntrospectionStatus(ctx, c, nodeID).Extract()
	return st, wrap(err, "getting introspection status of node %s", nodeID)
}

// ListIntrospections lists introspection statuses, at most limit (0: all).
func ListIntrospections(ctx context.Context, c *gophercloud.ServiceClient,
	opts introspection.ListIntrospectionsOptsBuilder, limit int,
) ([]introspection.Introspection, error) {
	all, err := paging.Collect(ctx, introspection.ListIntrospections(c, opts), limit, introspection.ExtractIntrospections)
	return all, wrap(err, "listing introspections")
}

// AbortIntrospection aborts a running introspection.
func AbortIntrospection(ctx context.Context, c *gophercloud.ServiceClient, nodeID string) error {
	return wrap(introspection.AbortIntrospection(ctx, c, nodeID).ExtractErr(), "aborting introspection of node %s", nodeID)
}

// GetIntrospectionData reads the data a finished introspection stored.
func GetIntrospectionData(ctx context.Context, c *gophercloud.ServiceClient, nodeID string) (*introspection.Data, error) {
	d, err := introspection.GetIntrospectionData(ctx, c, nodeID).Extract()
	return d, wrap(err, "getting introspection data for node %s", nodeID)
}

// GetIntrospectionDataRaw is GetIntrospectionData undecoded, so keys of
// inspector plugins the typed Data has no field for survive.
func GetIntrospectionDataRaw(ctx context.Context, c *gophercloud.ServiceClient, nodeID string) (any, error) {
	res := introspection.GetIntrospectionData(ctx, c, nodeID)
	if res.Err != nil {
		return nil, wrap(res.Err, "getting introspection data for node %s", nodeID)
	}
	return res.Body, nil
}

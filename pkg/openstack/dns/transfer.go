package dns

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/dns/v2/transfer/accept"
	"github.com/gophercloud/gophercloud/v2/openstack/dns/v2/transfer/request"
)

// CreateTransferRequest offers a zone to another project.
func CreateTransferRequest(ctx context.Context, c *gophercloud.ServiceClient, zoneID string,
	opts request.CreateOptsBuilder,
) (*request.TransferRequest, error) {
	tr, err := request.Create(ctx, c, zoneID, opts).Extract()
	return tr, wrap(err, "creating transfer request for zone %s", zoneID)
}

// ListTransferRequests lists zone transfer requests matching opts.
func ListTransferRequests(ctx context.Context, c *gophercloud.ServiceClient,
	opts request.ListOptsBuilder,
) ([]request.TransferRequest, error) {
	pages, err := request.List(c, opts).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing zone transfer requests")
	}
	all, err := request.ExtractTransferRequests(pages)
	return all, wrap(err, "parsing zone transfer request list")
}

// GetTransferRequest reads one zone transfer request.
func GetTransferRequest(ctx context.Context, c *gophercloud.ServiceClient, id string) (*request.TransferRequest, error) {
	tr, err := request.Get(ctx, c, id).Extract()
	return tr, wrap(err, "showing zone transfer request %s", id)
}

// UpdateTransferRequest updates a zone transfer request.
func UpdateTransferRequest(ctx context.Context, c *gophercloud.ServiceClient, id string,
	opts request.UpdateOptsBuilder,
) (*request.TransferRequest, error) {
	tr, err := request.Update(ctx, c, id, opts).Extract()
	return tr, wrap(err, "updating zone transfer request %s", id)
}

// DeleteTransferRequest deletes a zone transfer request.
func DeleteTransferRequest(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(request.Delete(ctx, c, id).ExtractErr(), "deleting zone transfer request %s", id)
}

// AcceptTransferRequest accepts a zone transfer request with its key.
func AcceptTransferRequest(ctx context.Context, c *gophercloud.ServiceClient, id, key string) (*accept.TransferAccept, error) {
	ac, err := accept.Create(ctx, c, accept.CreateOpts{ZoneTransferRequestID: id, Key: key}).Extract()
	return ac, wrap(err, "accepting zone transfer request %s", id)
}

// ListTransferAccepts lists zone transfer accepts matching opts.
func ListTransferAccepts(ctx context.Context, c *gophercloud.ServiceClient,
	opts accept.ListOptsBuilder,
) ([]accept.TransferAccept, error) {
	pages, err := accept.List(c, opts).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing zone transfer accepts")
	}
	all, err := accept.ExtractTransferAccepts(pages)
	return all, wrap(err, "parsing zone transfer accept list")
}

// GetTransferAccept reads one zone transfer accept.
func GetTransferAccept(ctx context.Context, c *gophercloud.ServiceClient, id string) (*accept.TransferAccept, error) {
	ac, err := accept.Get(ctx, c, id).Extract()
	return ac, wrap(err, "showing zone transfer accept %s", id)
}

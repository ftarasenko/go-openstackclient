package volume

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/transfers"

	"github.com/ftarasenko/go-openstackclient/pkg/openstack/paging"
)

// TransferSnapshotsMicroversion is where cinder added no_snapshots to the
// transfer request, on its /volume-transfers endpoint.
const TransferSnapshotsMicroversion = "3.55"

// ListTransfers lists the volume transfer requests opts selects, up to limit
// (0: all).
func ListTransfers(ctx context.Context, c *gophercloud.ServiceClient, opts transfers.ListOptsBuilder, limit int) ([]transfers.Transfer, error) {
	all, err := paging.Collect(ctx, transfers.List(c, opts), limit, transfers.ExtractTransfers)
	if err != nil {
		return nil, wrap(err, "listing volume transfer requests")
	}
	return all, nil
}

// GetTransfer returns the transfer request with the given ID. Cinder never
// returns its auth key again.
func GetTransfer(ctx context.Context, c *gophercloud.ServiceClient, id string) (*transfers.Transfer, error) {
	t, err := transfers.Get(ctx, c, id).Extract()
	return t, wrap(err, "showing volume transfer request %s", id)
}

// CreateTransfer offers a volume, with its snapshots, for transfer. The
// result carries the auth key, which only this call returns.
func CreateTransfer(ctx context.Context, c *gophercloud.ServiceClient, opts transfers.CreateOpts) (*transfers.Transfer, error) {
	t, err := transfers.Create(ctx, c, opts).Extract()
	return t, wrap(err, "creating a transfer request for volume %s", opts.VolumeID)
}

// CreateTransferWithoutSnapshots offers a volume for transfer and leaves its
// snapshots behind. It posts to /volume-transfers at
// TransferSnapshotsMicroversion, since gophercloud only drives the older
// /os-volume-transfer route, which has no such field.
func CreateTransferWithoutSnapshots(ctx context.Context, c *gophercloud.ServiceClient, volumeID, name string) (*transfers.Transfer, error) {
	transfer := map[string]any{"volume_id": volumeID, "no_snapshots": true}
	if name != "" {
		transfer["name"] = name
	}
	// On a copy: setMicroversionHeader rewrites the header on every request.
	pinned := *c
	pinned.Microversion = TransferSnapshotsMicroversion
	var result struct {
		Transfer transfers.Transfer `json:"transfer"`
	}
	resp, err := pinned.Post(ctx, pinned.ServiceURL("volume-transfers"), map[string]any{"transfer": transfer}, &result,
		&gophercloud.RequestOpts{OkCodes: []int{202}})
	closeBody(resp)
	if err != nil {
		return nil, wrap(err, "creating a transfer request for volume %s", volumeID)
	}
	return &result.Transfer, nil
}

// DeleteTransfer withdraws the transfer request with the given ID.
func DeleteTransfer(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(transfers.Delete(ctx, c, id).ExtractErr(), "deleting volume transfer request %s", id)
}

// AcceptTransfer accepts a transfer request into the caller's project.
func AcceptTransfer(ctx context.Context, c *gophercloud.ServiceClient, id, authKey string) (*transfers.Transfer, error) {
	t, err := transfers.Accept(ctx, c, id, transfers.AcceptOpts{AuthKey: authKey}).Extract()
	return t, wrap(err, "accepting volume transfer request %s", id)
}

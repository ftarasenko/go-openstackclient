package server

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
)

// newComputeClient authenticates once and derives the nova (compute v2) service
// client shared by every server/compute/hypervisor/quota subcommand. Mirrors
// baremetal.newBaremetalClient but for the compute service.
func newComputeClient(ctx context.Context, a *auth.Options) (*gophercloud.ServiceClient, error) {
	client, err := a.Authenticate(ctx)
	if err != nil {
		return nil, err
	}
	return client.Compute()
}

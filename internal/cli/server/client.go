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
	return a.NewServiceClient(ctx, (*auth.Client).Compute)
}

// newComputeSession authenticates once and returns both the compute client and
// the underlying authenticated bundle, so commands that need cross-service
// name→ID resolution (e.g. `server create --image`/`--network`) can lazily
// derive image/network clients from the same session.
func newComputeSession(ctx context.Context, a *auth.Options) (*gophercloud.ServiceClient, *auth.Client, error) {
	return a.NewServiceSession(ctx, (*auth.Client).Compute)
}

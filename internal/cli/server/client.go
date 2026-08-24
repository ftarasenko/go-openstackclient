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

// computeSession pairs the compute client with the authenticated bundle it came
// from. The two are always obtained and used together, so seams that need
// cross-service resolution take the pair rather than two parameters.
type computeSession struct {
	client *gophercloud.ServiceClient
	auth   *auth.Client
}

// newComputeSession authenticates once and returns both the compute client and
// the underlying authenticated bundle, so commands that need cross-service
// name→ID resolution (e.g. `server create --image`/`--network`) can lazily
// derive image/network clients from the same session.
func newComputeSession(ctx context.Context, a *auth.Options) (*computeSession, error) {
	client, ac, err := a.NewServiceSession(ctx, (*auth.Client).Compute)
	if err != nil {
		return nil, err
	}
	return &computeSession{client: client, auth: ac}, nil
}

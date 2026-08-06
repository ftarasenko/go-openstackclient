package compute

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
)

// newComputeClient authenticates once and derives the nova (compute v2) service
// client shared by every flavor and keypair subcommand.
func newComputeClient(ctx context.Context, a *auth.Options) (*gophercloud.ServiceClient, error) {
	return a.NewServiceClient(ctx, (*auth.Client).Compute)
}

// newComputeSession is newComputeClient for commands that also need the
// authenticated Client to lazily derive keystone, for the owner filters on
// "keypair list"/"keypair show" (--user/--project resolve to IDs).
func newComputeSession(ctx context.Context, a *auth.Options) (*gophercloud.ServiceClient, *auth.Client, error) {
	return a.NewServiceSession(ctx, (*auth.Client).Compute)
}

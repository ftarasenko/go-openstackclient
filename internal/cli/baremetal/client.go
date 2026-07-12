package baremetal

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
)

// newBaremetalClient authenticates once and derives the ironic service client
// shared by every baremetal subcommand.
func newBaremetalClient(ctx context.Context, a *auth.Options) (*gophercloud.ServiceClient, error) {
	return a.NewServiceClient(ctx, (*auth.Client).Baremetal)
}

package placement

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
)

// newPlacementClient authenticates once and derives the placement service
// client shared by every placement subcommand. The factory sets the placement
// microversion so gophercloud emits the generic
// "OpenStack-API-Version: placement <mv>" header.
func newPlacementClient(ctx context.Context, a *auth.Options) (*gophercloud.ServiceClient, error) {
	client, err := a.Authenticate(ctx)
	if err != nil {
		return nil, err
	}
	return client.Placement()
}

package image

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
)

// newImageClient authenticates once and derives the glance service client shared
// by every image subcommand. The image API has no microversion header, so the
// client's Microversion is left empty (Type == "image").
func newImageClient(ctx context.Context, a *auth.Options) (*gophercloud.ServiceClient, error) {
	client, err := a.Authenticate(ctx)
	if err != nil {
		return nil, err
	}
	return client.Image()
}

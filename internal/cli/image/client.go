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
	return a.NewServiceClient(ctx, (*auth.Client).Image)
}

// newImageSession returns the image client plus the authenticated bundle, so
// sharing commands can resolve a project name→ID via the identity service from
// the same session.
func newImageSession(ctx context.Context, a *auth.Options) (*gophercloud.ServiceClient, *auth.Client, error) {
	return a.NewServiceSession(ctx, (*auth.Client).Image)
}

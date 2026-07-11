package volume

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
)

// newVolumeClient authenticates once and derives the cinder (block-storage v3)
// service client shared by every volume subcommand. The client carries
// Type="block-storage", so gophercloud emits the volume microversion header
// (OpenStack-API-Version: volume <ver> and X-OpenStack-Volume-API-Version).
func newVolumeClient(ctx context.Context, a *auth.Options) (*gophercloud.ServiceClient, error) {
	client, err := a.Authenticate(ctx)
	if err != nil {
		return nil, err
	}
	return client.Volume()
}

// newVolumeSession returns the volume client plus the authenticated bundle, so
// `volume create --image <name>` can resolve the image name→ID via glance from
// the same session.
func newVolumeSession(ctx context.Context, a *auth.Options) (*gophercloud.ServiceClient, *auth.Client, error) {
	client, err := a.Authenticate(ctx)
	if err != nil {
		return nil, nil, err
	}
	vol, err := client.Volume()
	if err != nil {
		return nil, nil, err
	}
	return vol, client, nil
}

package identity

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
)

// newIdentityClient authenticates once and derives the keystone (identity v3)
// service client shared by every identity subcommand. The identity client uses
// no microversion header, so sc.Microversion is left empty (see auth.Identity).
func newIdentityClient(ctx context.Context, a *auth.Options) (*gophercloud.ServiceClient, error) {
	return a.NewServiceClient(ctx, (*auth.Client).Identity)
}

// newIdentityAuthClient authenticates and returns both the identity service
// client and the underlying auth.Client. "token issue" needs the provider off
// the auth.Client to read the current token that was minted during
// authentication, which the service client alone does not expose.
func newIdentityAuthClient(ctx context.Context, a *auth.Options) (*gophercloud.ServiceClient, *auth.Client, error) {
	return a.NewServiceSession(ctx, (*auth.Client).Identity)
}

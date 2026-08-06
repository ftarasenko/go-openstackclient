package dns

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
)

// newDNSClient authenticates once and derives the designate (dns v2) service
// client shared by every dns subcommand. The DNS client uses Type="dns" and no
// microversion header, so sc.Microversion is left empty by the factory.
func newDNSClient(ctx context.Context, a *auth.Options) (*gophercloud.ServiceClient, error) {
	return a.NewServiceClient(ctx, (*auth.Client).DNS)
}

// newDNSSession is newDNSClient for commands that also need the authenticated
// Client to lazily derive keystone — "zone share create" takes a target project
// by name, and designate stores only the ID.
func newDNSSession(ctx context.Context, a *auth.Options) (*gophercloud.ServiceClient, *auth.Client, error) {
	return a.NewServiceSession(ctx, (*auth.Client).DNS)
}

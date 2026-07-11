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
	client, err := a.Authenticate(ctx)
	if err != nil {
		return nil, err
	}
	return client.DNS()
}

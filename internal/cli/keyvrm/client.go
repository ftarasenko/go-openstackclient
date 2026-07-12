package keyvrm

import (
	"context"
	"fmt"
	"os"

	"github.com/gophercloud/gophercloud/v2"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
)

// newKeyVRMClient authenticates once and derives the KeyVRM service client
// (Keystone catalog type "keyvrm") shared by every keyvrm subcommand.
func newKeyVRMClient(ctx context.Context, a *auth.Options) (*gophercloud.ServiceClient, error) {
	client, err := a.Authenticate(ctx)
	if err != nil {
		return nil, err
	}
	return client.KeyVRM()
}

// writeTotal emits server-side pagination metadata to stderr, so it never
// pollutes the primary (piped) output — matching how the Python kvrm CLI
// surfaces list metadata.
func writeTotal(total, limit, offset int) {
	fmt.Fprintf(os.Stderr, "total=%d limit=%d offset=%d\n", total, limit, offset)
}

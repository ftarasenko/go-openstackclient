package keymanager

import (
	"context"
	"fmt"
	"time"

	"github.com/gophercloud/gophercloud/v2"

	"github.com/ftarasenko/koc/internal/auth"
)

// newKeyManagerClient authenticates once and derives the barbican client shared
// by every command in this package. The factory pins the key-manager
// microversion (openstack.KeyManagerMicroversion), so gophercloud emits
// "OpenStack-API-Version: key-manager 1.1".
func newKeyManagerClient(ctx context.Context, a *auth.Options) (*gophercloud.ServiceClient, error) {
	return a.NewServiceClient(ctx, (*auth.Client).KeyManager)
}

// parseExpiration reads an --expiration value. Upstream passes the string to
// barbican unparsed; gophercloud wants a time.Time, which it sends as a naive
// timestamp, so the value is converted to UTC — what barbican assumes of a
// naive one.
func parseExpiration(v string) (*time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.Parse(layout, v); err == nil {
			t = t.UTC()
			return &t, nil
		}
	}
	return nil, fmt.Errorf("--expiration %q: want an ISO 8601 time, e.g. 2030-01-02T15:04:05Z", v)
}

// nonZero renders 0 as an absent value, as upstream shows barbican's null
// bit_length.
func nonZero(n int) any {
	if n == 0 {
		return nil
	}
	return n
}

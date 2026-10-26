package keymanager

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/pagination"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
)

// newKeyManagerClient authenticates once and derives the barbican client shared
// by every command in this package. The factory pins the key-manager
// microversion (openstack.KeyManagerMicroversion), so gophercloud emits
// "OpenStack-API-Version: key-manager 1.1".
func newKeyManagerClient(ctx context.Context, a *auth.Options) (*gophercloud.ServiceClient, error) {
	return a.NewServiceClient(ctx, (*auth.Client).KeyManager)
}

// uuidRe matches a UUID in either spelling Python's uuid.UUID accepts from a
// barbican href: hyphenated or 32 bare hex digits.
var uuidRe = regexp.MustCompile(`^(?i:[0-9a-f]{8}-?[0-9a-f]{4}-?[0-9a-f]{4}-?[0-9a-f]{4}-?[0-9a-f]{12})$`)

// refUUID returns the UUID a barbican reference names: the last path element of
// an href, or the reference itself when it is a bare UUID. It mirrors
// barbicanclient's validate_ref_and_return_uuid, including its message.
func refUUID(ref, entity string) (string, error) {
	ref = strings.TrimRight(ref, "/")
	last := ref[strings.LastIndex(ref, "/")+1:]
	if !uuidRe.MatchString(last) {
		return "", fmt.Errorf("%s incorrectly specified", entity)
	}
	u := strings.ToLower(strings.ReplaceAll(last, "-", ""))
	return u[0:8] + "-" + u[8:12] + "-" + u[12:16] + "-" + u[16:20] + "-" + u[20:], nil
}

// firstPage fetches one page of a barbican listing. Upstream's list verbs ask
// for a single limit/offset window and never follow "next", so neither does
// koc. It returns nil when the listing is empty.
func firstPage(ctx context.Context, pager pagination.Pager) (pagination.Page, error) {
	var first pagination.Page
	err := pager.EachPage(ctx, func(_ context.Context, p pagination.Page) (bool, error) {
		first = p
		return false, nil
	})
	return first, err
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

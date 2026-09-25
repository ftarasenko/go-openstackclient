package network

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
)

// withQueryValues appends extra parameters to a query string built by a
// gophercloud ListOpts. It covers what ListOpts cannot express: a filter
// neutron accepts but gophercloud does not model (service_types, binding
// attributes), and a *repeated* filter — neutron ORs repeated values
// (?router_id=a&router_id=b), which is how upstream sends a repeatable
// --router, while every ListOpts field is a single string.
func withQueryValues(q string, err error, extra url.Values) (string, error) {
	if err != nil || len(extra) == 0 {
		return q, err
	}
	params, perr := url.ParseQuery(strings.TrimPrefix(q, "?"))
	if perr != nil {
		return "", fmt.Errorf("building list query: %w", perr)
	}
	for k, vs := range extra {
		for _, v := range vs {
			params.Add(k, v)
		}
	}
	return "?" + params.Encode(), nil
}

// resolveEach resolves every name-or-ID in refs with one of this package's
// resolvers, preserving order.
func resolveEach(ctx context.Context, client *gophercloud.ServiceClient, refs []string,
	resolver func(context.Context, *gophercloud.ServiceClient, string) (string, error),
) ([]string, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(refs))
	for _, ref := range refs {
		id, err := resolver(ctx, client, ref)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

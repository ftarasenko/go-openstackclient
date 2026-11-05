package network

import (
	"fmt"
	"net/url"
	"strings"
)

// withQueryValues appends extra parameters to a query string a gophercloud
// ListOpts built: a filter it does not model, or a repeated one (neutron ORs
// ?router_id=a&router_id=b, while every ListOpts field is a single string).
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

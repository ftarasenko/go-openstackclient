// Package identity is koc's keystone (identity v3) layer: domains, projects,
// users, groups, regions, services, endpoints, roles and their assignments,
// application credentials, the catalog and tokens.
//
// Every function takes the identity client openstack.Session.Identity
// returns and speaks gophercloud's types. Listings collect every page.
//
// Errors are wrapped with what the call was doing and the ID or name it was
// given ("deleting project <id>: …"); gophercloud's error stays reachable
// through errors.As, so gophercloud.ResponseCodeIs keeps working on it.
package identity

import (
	"context"
	"fmt"

	"github.com/gophercloud/gophercloud/v2/pagination"

	"github.com/ftarasenko/koc/pkg/openstack/paging"
)

// listAll collects every page of pager, wrapping a failure as "listing <what>".
func listAll[T any](ctx context.Context, pager pagination.Pager, extract func(pagination.Page) ([]T, error),
	format string, args ...any,
) ([]T, error) {
	all, err := paging.Collect(ctx, pager, 0, extract)
	if err != nil {
		return nil, wrap(err, format, args...)
	}
	return all, nil
}

// wrap prefixes err with what the call was doing; nil stays nil.
func wrap(err error, format string, args ...any) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf(format+": %w", append(args, err)...)
}

// Package paging collects paginated gophercloud results, stopping as soon as a
// --limit has been satisfied.
package paging

import (
	"context"

	"github.com/gophercloud/gophercloud/v2/pagination"
)

// Collect walks pager, extracting each page with extract, and returns at most
// limit items — or all of them when limit <= 0.
//
// The APIs koc talks to treat "limit" as a page size rather than a result cap,
// so the obvious AllPages-then-truncate spelling walks the whole collection no
// matter how small the cap: "server list --limit 1" issued 35 requests against
// a 34-server project to render one row. Stopping as soon as the cap is met
// bounds the request count by --limit instead of by collection size.
//
// Only use this where the cap applies to what the API returned. A command that
// filters or de-duplicates client-side between extraction and truncation must
// keep paging, or the filter runs against a short read and quietly returns
// fewer rows than were asked for.
func Collect[T any](ctx context.Context, pager pagination.Pager, limit int, extract func(pagination.Page) ([]T, error)) ([]T, error) {
	var all []T
	err := pager.EachPage(ctx, func(_ context.Context, page pagination.Page) (bool, error) {
		items, err := extract(page)
		if err != nil {
			return false, err
		}
		all = append(all, items...)
		return limit <= 0 || len(all) < limit, nil
	})
	if err != nil {
		return nil, err
	}
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

package resolve

import (
	"sync/atomic"
	"time"

	"github.com/ftarasenko/go-openstackclient/pkg/openstack/resolve"
)

// A resolver turns a name into an ID with an API call, and every command that
// takes a name pays for one. That is invisible in a one-shot invocation — it
// happens once — but --watch re-runs the whole command on a ticker, so a
// watched `server list --project payments --user alice` would put two extra
// Keystone lookups per second on the control plane for two answers that are the
// same every time.
//
// So a successful resolution is memoized for the life of the process, with a
// short expiry — but only once something has said it will resolve the same name
// repeatedly. Enable is what says so, and only the --watch wiring calls it.
//
// The memo itself is the public API's resolve.Cache, which keys every entry by
// the endpoint and token project as well as the name: the process holds one,
// and this package is its only owner. The public package keeps no state of
// its own — a long-running importer creates its own Cache per need.
//
// The gate is not timidity. A one-shot invocation resolves each name exactly
// once, so a cache buys it nothing, and leaving the memo permanently on would
// mean a process-global map silently changing what a second lookup returns —
// including across the tests of every command package that resolves a name.
// Paying for the memo only where it is worth something keeps every other path
// exactly as it was.

// cacheTTL bounds how stale a memoized mapping may be. A name→ID mapping does
// change — a project can be deleted and a new one created under the same name —
// so the cache expires rather than lasting forever; five minutes is long enough
// that an hours-long watch does almost no lookups and short enough that a
// rename is picked up while the operator is still looking at the screen.
const cacheTTL = 5 * time.Minute

var (
	// memo is the process's cache, nil until Enable. Atomic because the
	// resolvers may be reached from several goroutines of a fanned-out command.
	memo atomic.Pointer[resolve.Cache]
	// cacheNow is the clock, seamed for the expiry test.
	cacheNow = time.Now
)

// Enable turns the memo on for the rest of the process. It is idempotent, and
// there is deliberately no way to turn it off again: the only caller is the
// --watch wiring, and a watch runs until the process ends.
func Enable() {
	memo.CompareAndSwap(nil, resolve.NewCache(cacheTTL, resolve.WithClock(func() time.Time { return cacheNow() })))
}

// resolver is the public Resolver with the process's memo, if enabled.
func resolver() resolve.Resolver { return resolve.Resolver{Cache: memo.Load()} }

// ResetCacheForTest empties the memo and turns it off again. Test-only; the
// cache is process-global, so without it one test's resolutions would leak into
// the next.
func ResetCacheForTest() { memo.Store(nil) }

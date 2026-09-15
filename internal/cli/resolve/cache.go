package resolve

import (
	"sync"
	"sync/atomic"
	"time"
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

// cacheKey identifies one resolution. scope is what distinguishes two lookups
// of the same kind and name that do not have the same answer: the domain in
// ProjectIDInDomain / UserIDInDomain. Without it, `--project payments
// --project-domain a` and `--project payments --project-domain b` would share
// an entry and one of them would get the other's ID.
type cacheKey struct {
	kind  string
	scope string
	ref   string
}

type cacheEntry struct {
	id string
	at time.Time
}

var (
	cacheMu sync.RWMutex
	cache   = map[cacheKey]cacheEntry{}
	// cacheOn gates the memo. Atomic because it is read from the resolvers,
	// which a fanned-out command may reach from several goroutines.
	cacheOn atomic.Bool
	// cacheNow is the clock, seamed for the expiry test.
	cacheNow = time.Now
)

// Enable turns the memo on for the rest of the process. It is idempotent, and
// there is deliberately no way to turn it off again: the only caller is the
// --watch wiring, and a watch runs until the process ends.
func Enable() { cacheOn.Store(true) }

// cacheGet returns a memoized ID that has not yet expired.
func cacheGet(k cacheKey) (string, bool) {
	if !cacheOn.Load() {
		return "", false
	}
	cacheMu.RLock()
	e, ok := cache[k]
	cacheMu.RUnlock()
	if !ok || cacheNow().Sub(e.at) >= cacheTTL {
		return "", false
	}
	return e.id, true
}

// cachePut memoizes a resolution.
//
// Only a real match is ever stored. The zero-match case falls back to treating
// the reference as an opaque ID (see pick), and caching that would break the
// one thing a watch is for: `server list --project payments --watch` started
// before the project exists must start working when it does, not stay wrong for
// five minutes.
func cachePut(k cacheKey, id string) {
	if !cacheOn.Load() {
		return
	}
	cacheMu.Lock()
	cache[k] = cacheEntry{id: id, at: cacheNow()}
	cacheMu.Unlock()
}

// ResetCacheForTest empties the memo and turns it off again. Test-only; the
// cache is process-global, so without it one test's resolutions would leak into
// the next.
func ResetCacheForTest() {
	cacheMu.Lock()
	cache = map[cacheKey]cacheEntry{}
	cacheMu.Unlock()
	cacheOn.Store(false)
}

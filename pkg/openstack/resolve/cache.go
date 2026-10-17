package resolve

import (
	"sync"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/tokens"
)

// Cache memoizes successful name→ID resolutions for a while. It is for a
// caller that resolves the same names repeatedly — a refresh loop, a
// long-running service — and is never shared implicitly: a [Resolver] uses
// the Cache it is given, and the package-level resolvers use none.
//
// An entry is keyed by the service endpoint the lookup went to and the
// project the client's token is scoped to, as well as by the kind, scope and
// name looked up. So one Cache may serve several regions, and several
// credentials, without one's answer leaking into another's: the same project
// name in two regions — or two projects' images of the same name — resolve
// independently.
//
// Only a single match is stored. A failed lookup, an ambiguous name and a name
// that matches nothing are always asked again, so a resource created after
// the first attempt is found by the next one.
//
// A Cache is safe for concurrent use. The zero value is not usable; call
// [NewCache]. A nil *Cache caches nothing.
type Cache struct {
	ttl time.Duration
	now func() time.Time

	mu sync.RWMutex
	m  map[cacheKey]cacheEntry
}

// CacheOption configures a Cache.
type CacheOption func(*Cache)

// WithClock replaces the clock entries are aged by (time.Now by default).
func WithClock(now func() time.Time) CacheOption {
	return func(c *Cache) { c.now = now }
}

// NewCache returns an empty Cache whose entries expire ttl after they were
// stored. A name→ID mapping does change — a project can be deleted and a new
// one created under the same name — so pick a ttl no longer than a stale
// answer may live.
func NewCache(ttl time.Duration, opts ...CacheOption) *Cache {
	c := &Cache{ttl: ttl, now: time.Now, m: map[cacheKey]cacheEntry{}}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Purge empties the cache.
func (c *Cache) Purge() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.m = map[cacheKey]cacheEntry{}
	c.mu.Unlock()
}

// cacheKey identifies one resolution. endpoint and project are what make a
// Cache safe to share across regions and credentials; scope distinguishes two
// lookups of the same kind and name that need not have the same answer — the
// domain an in-domain resolver narrows to.
type cacheKey struct {
	endpoint string
	project  string
	kind     string
	scope    string
	ref      string
}

type cacheEntry struct {
	id string
	at time.Time
}

func newCacheKey(client *gophercloud.ServiceClient, kind, scope, ref string) cacheKey {
	return cacheKey{
		endpoint: client.ResourceBaseURL(),
		project:  tokenProject(client.ProviderClient),
		kind:     kind,
		scope:    scope,
		ref:      ref,
	}
}

// tokenProject is the project the client's token is scoped to, "" when it has
// none or did not come from Keystone.
func tokenProject(pc *gophercloud.ProviderClient) string {
	if pc == nil {
		return ""
	}
	r, ok := pc.GetAuthResult().(interface {
		ExtractProject() (*tokens.Project, error)
	})
	if !ok {
		return ""
	}
	p, err := r.ExtractProject()
	if err != nil || p == nil {
		return ""
	}
	return p.ID
}

func (c *Cache) get(k cacheKey) (string, bool) {
	if c == nil {
		return "", false
	}
	c.mu.RLock()
	e, ok := c.m[k]
	c.mu.RUnlock()
	if !ok || c.now().Sub(e.at) >= c.ttl {
		return "", false
	}
	return e.id, true
}

func (c *Cache) put(k cacheKey, id string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.m[k] = cacheEntry{id: id, at: c.now()}
	c.mu.Unlock()
}

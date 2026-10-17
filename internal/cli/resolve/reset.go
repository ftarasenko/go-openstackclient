package resolve

// Reset empties the resolver memo and turns it off again, so one `koc shell`
// line cannot see what an earlier line resolved: the memo is process-global and
// keyed by name alone, so a line run after a region or project switch would
// otherwise reuse the previous scope's IDs. The shell calls it around every
// line; a one-shot invocation never needs it.
//
// When the memo becomes explicit and endpoint-keyed, this becomes "a new cache
// per line" and goes away.
func Reset() { ResetCacheForTest() }

//go:build windows

package watch

// winchSignals has no Windows equivalent: there is no SIGWINCH, and the console
// reports a resize through an input-event stream koc does not read. The display
// size is measured once per frame (see writerSize), so a resize is still picked
// up — at the next refresh rather than the instant it happens.
func winchSignals() (<-chan struct{}, func()) {
	return nil, func() {}
}

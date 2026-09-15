package watch

import "strings"

// The status line has room for a reminder, not for a key map: `keys: q p r + -
// d` says which letters do something and nothing about what, and `+` in
// particular reads backwards — it lengthens the interval, so it refreshes
// *less* often. `?` is where the actual answer lives, which is the spelling
// less(1), top(1), k9s and viddy all use.
//
// The panel replaces the frame rather than floating over it: a table drawn at
// the terminal's width has no free rectangle to float in, and half a table
// under a box is worse than no table.

// helpLines renders the key map.
//
// Fourteen lines and none over 70 columns, which is deliberate: the painter
// clips an overlong frame and says how much it dropped, and a help panel that
// gets clipped is the one thing that must not. This fits a 20-row terminal with
// the status line, and leaves room on an 80-column one.
func (r *runner) helpLines() []string {
	return []string{
		"koc --watch — keys",
		"",
		"  q, Q        quit",
		"  space, r    refresh now — also while paused",
		"  p           pause / resume — resuming refreshes at once",
		"  +, =        slower: longer interval (+1s, or +250ms below 1s)",
		"  -           faster: shorter interval, floored at " + MinInterval.String(),
		"  d           toggle change highlighting — now " + r.diffState(),
		"  ?           close this help",
		"  Ctrl-C      interrupt (exit 130)",
		"",
		"Refreshes carry on while this is open; press p to hold the data.",
		"Highlighting: a changed cell is reverse-video, a new row green, and",
		"a row that has just left is held dimmed for one frame.",
	}
}

// diffState names whether change highlighting is on, for the places that have
// to say so. Nothing else on screen can: with highlighting off the frame looks
// exactly like a frame in which nothing happened to change, so pressing 'd'
// used to have no visible effect at all until something moved.
func (r *runner) diffState() string {
	if r.differ.Enabled() {
		return "on"
	}
	return "off"
}

// helpHint is the status line's pointer at the panel. It replaces the bare list
// of letters, which told an operator that six keys existed and nothing more.
func (r *runner) helpHint() string {
	if r.showHelp {
		return "? closes this help"
	}
	return "? for help"
}

// HelpFlagNote is appended to the --watch flag's usage string by the cli layer,
// so `koc <noun> list --help` answers "what are the keys" without sending the
// operator to the README. It lives here because this file owns the key map it
// points at.
const HelpFlagNote = "on a terminal, press ? for the keys"

// helpMentions reports whether s names every key the panel documents. It backs
// the test that keeps helpLines honest when a binding is added to keys.go.
func helpMentions(s string, keys ...string) bool {
	for _, k := range keys {
		if !strings.Contains(s, k) {
			return false
		}
	}
	return true
}

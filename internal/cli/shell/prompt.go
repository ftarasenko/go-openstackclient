package shell

import (
	"strconv"
	"unicode/utf8"
)

// The prompt is `koc [<region> · <project>] $ `, with the last command's exit
// status before the `$` when it failed. It has to leave room to type on an
// 80-column terminal — the default size of a browser terminal — so it never
// takes more than half the width, and the names give way first.

const (
	promptMaxShare  = 2  // the prompt takes at most 1/promptMaxShare of the width
	promptMinWidth  = 24 // ... but never budgets less than this
	promptMinRegion = 6
	ellipsis        = "…"
)

// promptScope is what the prompt shows of the current scope.
type promptScope struct {
	region  string
	project string // a name, or a short ID when no name is known
}

// formatPrompt renders the prompt for a terminal width columns wide.
func formatPrompt(s promptScope, status, width int) string {
	tail := "$ "
	if status != 0 {
		tail = strconv.Itoa(status) + " $ "
	}
	if s.region == "" && s.project == "" {
		return "koc " + tail
	}

	budget := max(width/promptMaxShare, promptMinWidth)
	// "koc [" + "] " + tail, plus " · " when both names are shown.
	fixed := len("koc [") + len("] ") + utf8.RuneCountInString(tail)
	region, project := s.region, s.project
	room := budget - fixed
	switch {
	case region == "":
		project = truncate(project, room)
	case project == "":
		region = truncate(region, room)
	default:
		room -= len(" · ") - 1 // the middle dot is one column
		// The project is the more useful of the two, so the region yields
		// first, down to a few characters; then the project does.
		rw := utf8.RuneCountInString(region)
		pw := utf8.RuneCountInString(project)
		if rw+pw > room {
			region = truncate(region, max(room-pw, min(rw, promptMinRegion)))
			project = truncate(project, room-utf8.RuneCountInString(region))
		}
	}

	inner := region
	switch {
	case region == "":
		inner = project
	case project != "":
		inner = region + " · " + project
	}
	return "koc [" + inner + "] " + tail
}

// truncate shortens s to at most n columns, marking the cut with an ellipsis.
func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	if n <= 1 {
		return ellipsis
	}
	r := []rune(s)
	return string(r[:n-1]) + ellipsis
}

// shortID shortens a UUID-like project ID for the prompt.
func shortID(id string) string {
	const n = 8
	if len(id) > n {
		return id[:n]
	}
	return id
}

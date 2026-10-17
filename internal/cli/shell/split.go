package shell

import (
	"errors"
	"fmt"
	"strings"
)

// A koc shell line is split into arguments the way a POSIX shell would split a
// simple command — single quotes, double quotes, backslash escapes — and
// nothing more. There are no pipes, redirects, globbing, variables, command
// lists or subshells: the shell runs koc commands, not a second Linux shell.
// Rather than passing such a character to koc as a literal, where it would
// fail in a confusing way (or, worse, succeed with a different meaning than
// the operator expected), the line is refused with the feature named.

// errUnterminated reports an open quote or a trailing backslash.
var errUnterminated = errors.New("unterminated quote or trailing backslash")

// unsupported names the shell feature an unquoted character would start.
func unsupported(r rune, next rune) string {
	switch r {
	case '|':
		if next == '|' {
			return "command lists (||)"
		}
		return "pipes (|)"
	case '>', '<':
		return "redirects (" + string(r) + ")"
	case ';':
		return "command lists (;)"
	case '&':
		if next == '&' {
			return "command lists (&&)"
		}
		return "background jobs (&)"
	case '$':
		if next == '(' {
			return "command substitution ($(...))"
		}
		return "variables ($)"
	case '`':
		return "command substitution (`...`)"
	case '*', '?':
		return "globbing (" + string(r) + ")"
	case '(', ')':
		return "subshells (" + string(r) + ")"
	}
	return ""
}

// unsupportedError is a line using a shell feature koc shell does not have.
type unsupportedError struct{ feature string }

func (e *unsupportedError) Error() string {
	return fmt.Sprintf("%s are not supported; quote the character to pass it literally", e.feature)
}

// splitLine splits a line into arguments. A word that starts with # starts a
// comment that runs to the end of the line, as in sh.
func splitLine(line string) ([]string, error) {
	args, _, err := split(line, false)
	return args, err
}

// splitPartial is splitLine for completion: an open quote is not an error, and
// it reports whether the line ends inside a word (so the last argument is the
// one being completed) or after whitespace.
func splitPartial(line string) (args []string, inWord bool, err error) {
	return split(line, true)
}

//nolint:gocognit // one state machine over the three quoting modes; splitting it scatters the state
func split(line string, partial bool) (args []string, inWord bool, err error) {
	var (
		cur     strings.Builder
		haveCur bool // distinguishes '' (an empty argument) from no argument
		quote   rune // 0, '\'' or '"'
	)
	rs := []rune(line)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		var next rune
		if i+1 < len(rs) {
			next = rs[i+1]
		}
		switch quote {
		case '\'':
			if r == '\'' {
				quote = 0
				continue
			}
			cur.WriteRune(r)
			continue
		case '"':
			switch r {
			case '"':
				quote = 0
			case '\\':
				// Inside double quotes a backslash escapes only these, as in sh.
				if strings.ContainsRune("\"\\$`", next) {
					cur.WriteRune(next)
					i++
				} else {
					cur.WriteRune(r)
				}
			case '$', '`':
				// sh expands these inside double quotes too; koc does not.
				return nil, false, &unsupportedError{feature: unsupported(r, next)}
			default:
				cur.WriteRune(r)
			}
			continue
		}

		switch {
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			if haveCur {
				args = append(args, cur.String())
				cur.Reset()
				haveCur = false
			}
		case r == '#' && !haveCur:
			// A comment: the rest of the line is ignored.
			return args, false, nil
		case r == '\'' || r == '"':
			quote = r
			haveCur = true
		case r == '\\':
			if i+1 >= len(rs) {
				if partial {
					continue
				}
				return nil, false, errUnterminated
			}
			cur.WriteRune(next)
			haveCur = true
			i++
		default:
			if f := unsupported(r, next); f != "" {
				return nil, false, &unsupportedError{feature: f}
			}
			cur.WriteRune(r)
			haveCur = true
		}
	}
	if quote != 0 && !partial {
		return nil, false, errUnterminated
	}
	if haveCur {
		args = append(args, cur.String())
		return args, true, nil
	}
	return args, false, nil
}

// quoteArg renders an argument so splitLine reads it back unchanged, quoting
// only when it has to.
func quoteArg(s string) string {
	if s != "" && !strings.ContainsFunc(s, needsQuote) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func needsQuote(r rune) bool {
	return r <= ' ' || strings.ContainsRune(`'"\#|&;<>()$`+"`*?", r)
}

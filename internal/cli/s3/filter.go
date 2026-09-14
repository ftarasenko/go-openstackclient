package s3cli

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/spf13/pflag"
)

// keyFilter is the --include/--exclude pair shared by every command that walks
// a listing. Both take glob patterns and both may be repeated:
//
//   - with no --include, every key is a candidate;
//   - with any --include, a key must match at least one of them;
//   - --exclude then removes a key whatever --include said, because "all the
//     dumps except the end-to-end ones" is the shape an operator actually
//     needs.
type keyFilter struct {
	include []string
	exclude []string

	compiled struct {
		include []*regexp.Regexp
		exclude []*regexp.Regexp
	}
}

// addTo registers the flags. Every command that filters a listing uses the same
// two names and the same help, so an operator learns them once.
func (f *keyFilter) addTo(fs *pflag.FlagSet) {
	fs.StringArrayVar(&f.include, "include", nil,
		`only keys matching this glob, repeatable (e.g. --include "*.sql.gz")`)
	fs.StringArrayVar(&f.exclude, "exclude", nil,
		`skip keys matching this glob, repeatable; applied after --include`)
}

// compile turns the patterns into matchers, rejecting a malformed one before any
// request is made rather than silently matching nothing.
func (f *keyFilter) compile() error {
	var err error
	if f.compiled.include, err = compileGlobs(f.include, "--include"); err != nil {
		return err
	}
	f.compiled.exclude, err = compileGlobs(f.exclude, "--exclude")
	return err
}

// active reports whether any pattern was given, so a caller can skip the whole
// filtering path (and its per-key cost) when none was.
func (f *keyFilter) active() bool { return len(f.include) > 0 || len(f.exclude) > 0 }

// match reports whether key survives the filter.
func (f *keyFilter) match(key string) bool {
	if len(f.compiled.include) > 0 && !matchesAny(f.compiled.include, key) {
		return false
	}
	return !matchesAny(f.compiled.exclude, key)
}

func matchesAny(pats []*regexp.Regexp, key string) bool {
	for _, p := range pats {
		if p.MatchString(key) {
			return true
		}
	}
	return false
}

func compileGlobs(pats []string, flag string) ([]*regexp.Regexp, error) {
	out := make([]*regexp.Regexp, 0, len(pats))
	for _, p := range pats {
		re, err := compileGlob(p)
		if err != nil {
			return nil, fmt.Errorf("%s %q: %w", flag, p, err)
		}
		out = append(out, re)
	}
	return out, nil
}

// compileGlob translates a glob to an anchored regexp.
//
// path.Match is not used: its "*" stops at a "/", which is right for a
// filesystem and wrong for an S3 keyspace, where the slashes in
// "2026/08/dump.sql.gz" are ordinary characters and --include "*.sql.gz" is
// expected to reach them. This matches s5cmd's wildcards, where "*" spans
// separators and "?" is one character.
//
// Only those two are special. Everything else — a dot, a bracket, a brace — is
// quoted to a literal, so a key containing one names itself rather than turning
// into a character class the operator did not write.
func compileGlob(pattern string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	for _, r := range pattern {
		switch r {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

// hasGlob reports whether ref carries wildcard characters, i.e. whether it
// names a set of keys rather than one.
func hasGlob(ref string) bool {
	return strings.ContainsAny(ref, "*?")
}

// globPrefix is the literal part of a glob before its first wildcard, which is
// what the server can filter on: "2026/08/*.gz" lists under "2026/08/" and the
// pattern then narrows the result locally.
func globPrefix(pattern string) string {
	if i := strings.IndexAny(pattern, "*?"); i >= 0 {
		return pattern[:i]
	}
	return pattern
}

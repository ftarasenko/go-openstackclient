package cli

import (
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// maxCommandWords bounds the traversal. koc's deepest command path is four words
// (`network trunk subport list`); the cap is a cheap guard against an unexpected
// tree shape spinning forever.
const maxCommandWords = 8

// ExpandCommandPrefixes rewrites unambiguous *command-name* prefixes in args to
// the full command name, so `koc server li --limit 1` runs `koc server list`.
//
// python-openstackclient does this through cliff, whose
// `_get_commands_by_partial_name` matches a command word-by-word, requires the
// same word count, and accepts the result only when exactly ONE command matches
// — `openstack server li --limit 1` works upstream today. Resolving one word at
// a time down the cobra tree, as below, is the same rule: each word must
// uniquely prefix a child of the command resolved so far, and a path only
// expands as far as there are command words to consume.
//
// It must run BEFORE ExpandFlagPrefixes. Flag expansion resolves abbreviations
// against the flags of the command the args point at, so with an unexpanded
// `li` it falls back to the `server` parent — whose flag set has no --limit —
// and `koc server li --lim 1` fails with "unknown flag" even though the leaf
// defines it.
//
// The rules are deliberately conservative, matching ExpandFlagPrefixes:
//
//   - An exact command name or alias always wins; only a word that resolves to
//     nothing is treated as a prefix.
//   - Expansion happens only when exactly one child has the word as a prefix.
//     Zero or several matches are left alone, so cobra still produces its normal
//     "unknown command" error rather than koc guessing.
//   - Aliases count as spellings of their command, so matching two aliases of
//     the same command is not ambiguous.
//   - Flags and their values are skipped, never rewritten, and everything after
//     a bare `--` terminator is positional and is not examined.
func ExpandCommandPrefixes(root *cobra.Command, args []string) []string {
	if len(args) == 0 {
		return args
	}
	out := make([]string, len(args))
	copy(out, args)

	cur := root
	from := 0
	for range maxCommandWords {
		if !cur.HasSubCommands() {
			return out
		}
		i := commandWordIndex(cur, out, from)
		if i < 0 {
			return out
		}
		next := exactChild(cur, out[i])
		if next == nil {
			full, ok := soleChildWithPrefix(cur, out[i])
			if !ok {
				return out
			}
			out[i] = full
			next = exactChild(cur, full)
			if next == nil {
				return out
			}
		}
		cur, from = next, i+1
	}
	return out
}

// exactChild returns the subcommand of cmd spelled exactly word, by name or
// alias, or nil.
func exactChild(cmd *cobra.Command, word string) *cobra.Command {
	for _, sub := range cmd.Commands() {
		if sub.Name() == word {
			return sub
		}
		for _, alias := range sub.Aliases {
			if alias == word {
				return sub
			}
		}
	}
	return nil
}

// soleChildWithPrefix reports the single subcommand of cmd whose name or one of
// whose aliases has word as a prefix. Zero matches, or matches on two different
// commands, yields ok=false.
func soleChildWithPrefix(cmd *cobra.Command, word string) (string, bool) {
	var match *cobra.Command
	for _, sub := range cmd.Commands() {
		hit := strings.HasPrefix(sub.Name(), word)
		for _, alias := range sub.Aliases {
			hit = hit || strings.HasPrefix(alias, word)
		}
		if !hit {
			continue
		}
		if match != nil && match != sub {
			return "", false // ambiguous
		}
		match = sub
	}
	if match == nil {
		return "", false
	}
	return match.Name(), true
}

// commandWordIndex returns the index in args, at or after from, of the first
// token that is a command word — neither a flag nor a flag's value — or -1.
//
// Skipping a flag's value matters: in `koc --os-cloud prod ser list`, "prod" is
// a value, and rewriting it to a command name would corrupt the invocation.
// Whether a flag consumes the following token is read off pflag's NoOptDefVal,
// which is set exactly for flags usable without one (bools).
//
// A `--long` token is resolved the same way ExpandFlagPrefixes resolves it: an
// exact name first, then — because this runs BEFORE flag expansion — its
// unambiguous prefix. Without the prefix fallback, an abbreviated flag such as
// `--os-clo` misses the exact lookup, so its value is treated as unclaimed and
// walks straight into soleChildWithPrefix, which happily rewrites a cloud name
// like "net" into the command "network". Resolving the same prefix here, before
// any rewriting happens, keeps the two expansion passes looking at the same
// flag.
// flagFinder resolves flag names against a command and its ancestors. It walks
// the ancestor chain rather than trusting cmd.InheritedFlags(): this runs before
// Execute, and a command's own Flags() does not yet carry the persistent flags
// declared on it (the root's --os-cloud, -f, --debug …).
type flagFinder struct {
	cmd *cobra.Command

	// names is every long flag name reachable from cmd, filled in at most once
	// and only if an exact lookup ever misses — most invocations use full flag
	// names and never need it.
	names map[string]bool
}

func (ff *flagFinder) find(pick func(*pflag.FlagSet) *pflag.Flag) *pflag.Flag {
	for c := ff.cmd; c != nil; c = c.Parent() {
		if f := pick(c.Flags()); f != nil {
			return f
		}
		if f := pick(c.PersistentFlags()); f != nil {
			return f
		}
	}
	return nil
}

func (ff *flagFinder) lookup(name string) *pflag.Flag {
	return ff.find(func(fs *pflag.FlagSet) *pflag.Flag { return fs.Lookup(name) })
}

func (ff *flagFinder) shorthand(sh string) *pflag.Flag {
	return ff.find(func(fs *pflag.FlagSet) *pflag.Flag { return fs.ShorthandLookup(sh) })
}

// prefixed resolves an abbreviated long name the way ExpandFlagPrefixes would.
func (ff *flagFinder) prefixed(name string) *pflag.Flag {
	if ff.names == nil {
		ff.names = make(map[string]bool)
		for c := ff.cmd; c != nil; c = c.Parent() {
			c.Flags().VisitAll(func(f *pflag.Flag) { ff.names[f.Name] = true })
			c.PersistentFlags().VisitAll(func(f *pflag.Flag) { ff.names[f.Name] = true })
		}
	}
	full, ok := soleFlagWithPrefix(ff.names, name)
	if !ok {
		return nil
	}
	return ff.lookup(full)
}

// consumesValue reports how many extra tokens the flag token tok takes: 1 when
// its value is the next argument, 0 when the value is inline ("--x=1") or the
// flag needs none.
func (ff *flagFinder) consumesValue(tok string) int {
	if strings.HasPrefix(tok, "--") {
		name, _, hasValue := strings.Cut(tok[2:], "=")
		if hasValue {
			return 0
		}
		f := ff.lookup(name)
		if f == nil {
			f = ff.prefixed(name)
		}
		if f != nil && f.NoOptDefVal == "" {
			return 1
		}
		return 0
	}
	// A shorthand cluster ("-cf"): only its last flag can take a separate
	// value, and only when nothing follows it inline.
	cluster := tok[1:]
	if f := ff.shorthand(cluster[len(cluster)-1:]); f != nil && f.NoOptDefVal == "" {
		return 1
	}
	return 0
}

func commandWordIndex(cmd *cobra.Command, args []string, from int) int {
	ff := &flagFinder{cmd: cmd}
	for i := from; i < len(args); i++ {
		tok := args[i]
		switch {
		case tok == "--":
			return -1 // the rest is positional
		case strings.HasPrefix(tok, "-") && len(tok) > 1:
			i += ff.consumesValue(tok)
		default:
			return i
		}
	}
	return -1
}

package cli

import (
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// ExpandFlagPrefixes rewrites unambiguous long-flag *prefixes* in args to the
// full flag name, so koc accepts the abbreviations `openstack` does.
//
// python-openstackclient is built on argparse, which resolves any unambiguous
// prefix of a long option. Muscle memory and scripts are full of the result —
// `--all` for `--all-projects`, `--fit` for `--fit-width` — and pflag does not
// abbreviate, so every one of those invocations fails against koc with "unknown
// flag". This closes that gap once, for every command, rather than sprinkling
// per-command aliases.
//
// The rules are deliberately conservative:
//
//   - Only `--long` forms are considered. Short flags and their clusters
//     (`-cf json`) are left exactly as they are.
//   - A token whose name is already a defined flag is never touched, so an
//     abbreviation can never shadow a real flag.
//   - Expansion happens only when exactly one defined flag has the token as a
//     prefix. Zero or several matches are left alone, so cobra still produces its
//     normal "unknown flag" / operator-facing error rather than koc guessing.
//   - Everything after a bare `--` terminator is positional and is not examined.
//
// The candidate set is the flags of the command the args actually resolve to,
// plus the persistent flags it inherits — so `--fit` expands against the root's
// --fit-width while `--all` expands against the leaf's own --all-projects.
func ExpandFlagPrefixes(root *cobra.Command, args []string) []string {
	if len(args) == 0 {
		return args
	}
	target, _, err := root.Find(args)
	if err != nil || target == nil {
		// An unresolvable command line still gets the root's global flags expanded;
		// cobra reports the unknown command either way.
		target = root
	}

	defined := definedFlagNames(target)
	if len(defined) == 0 {
		return args
	}

	out := make([]string, len(args))
	copy(out, args)
	for i, arg := range out {
		if arg == "--" {
			break // the rest is positional
		}
		if !strings.HasPrefix(arg, "--") || len(arg) == 2 {
			continue
		}
		name, value, hasValue := strings.Cut(arg[2:], "=")
		if name == "" || defined[name] {
			continue
		}
		full, ok := soleFlagWithPrefix(defined, name)
		if !ok {
			continue
		}
		if hasValue {
			out[i] = "--" + full + "=" + value
			continue
		}
		out[i] = "--" + full
	}
	return out
}

// definedFlagNames collects every long flag name valid on cmd: its own flags plus
// the persistent flags inherited from its ancestors.
func definedFlagNames(cmd *cobra.Command) map[string]bool {
	names := make(map[string]bool)
	collect := func(fs *pflag.FlagSet) {
		if fs == nil {
			return
		}
		fs.VisitAll(func(f *pflag.Flag) { names[f.Name] = true })
	}
	collect(cmd.Flags())
	collect(cmd.InheritedFlags())
	return names
}

// soleFlagWithPrefix reports the single defined flag having prefix as a prefix.
// Zero matches or more than one yields ok=false, leaving the token untouched.
func soleFlagWithPrefix(defined map[string]bool, prefix string) (string, bool) {
	match := ""
	for name := range defined {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		if match != "" {
			return "", false // ambiguous
		}
		match = name
	}
	return match, match != ""
}

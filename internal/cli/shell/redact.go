package shell

import (
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
)

// redacted replaces a secret flag's value wherever a line is recorded: the
// audit event and the history file.
const redacted = "***"

// redactArgs returns args with the value of every secret-carrying flag
// replaced by redacted. The flags are looked up on the command args resolve
// to, so a boolean flag never swallows the argument after it, and a secret is
// recognised by auth.IsSecretName — the list --debug redacts with.
//
//nolint:gocognit // one pass over the three spellings a flag value can take
func redactArgs(root *cobra.Command, args []string) []string {
	out := make([]string, len(args))
	copy(out, args)
	cmd := root
	if c, _, err := root.Find(args); err == nil && c != nil {
		cmd = c
	}
	lookup := func(long, short string) *pflag.Flag {
		for _, fs := range []*pflag.FlagSet{cmd.Flags(), cmd.InheritedFlags(), root.PersistentFlags()} {
			if long != "" {
				if f := fs.Lookup(long); f != nil {
					return f
				}
			} else if f := fs.ShorthandLookup(short); f != nil {
				return f
			}
		}
		return nil
	}
	for i := 0; i < len(out); i++ {
		a := out[i]
		if a == "--" {
			break
		}
		var name, short string
		switch {
		case strings.HasPrefix(a, "--"):
			name = a[2:]
		case strings.HasPrefix(a, "-") && len(a) == 2:
			short = a[1:]
		default:
			continue
		}
		name, _, hasValue := strings.Cut(name, "=")
		f := lookup(name, short)
		secret := auth.IsSecretName(name)
		if f != nil {
			secret = auth.IsSecretName(f.Name)
		}
		if !secret {
			// A flag that takes a value consumes the next argument, which is
			// then not looked at as a flag of its own.
			if f != nil && !hasValue && f.NoOptDefVal == "" {
				i++
			}
			continue
		}
		switch {
		case hasValue:
			out[i] = a[:strings.Index(a, "=")+1] + redacted
		case f != nil && f.NoOptDefVal != "":
			// A boolean such as --root-password: there is no value to hide.
		case i+1 < len(out):
			out[i+1] = redacted
			i++
		}
	}
	return out
}

// redactLine is redactArgs for a line of text, as the history file stores it.
// A line that does not split is stored as typed only if it names no secret
// flag at all; otherwise it is dropped to its first word.
func redactLine(newRoot func() *cobra.Command, line string) string {
	args, err := splitLine(line)
	if err != nil {
		for w := range strings.FieldsSeq(line) {
			if strings.HasPrefix(w, "-") && auth.IsSecretName(strings.SplitN(w, "=", 2)[0]) {
				first, _, _ := strings.Cut(strings.TrimSpace(line), " ")
				return first + " " + redacted
			}
		}
		return line
	}
	red := redactArgs(newRoot(), stripPrefix(args))
	quoted := make([]string, len(red))
	for i, a := range red {
		if a == redacted || strings.HasSuffix(a, "="+redacted) {
			quoted[i] = a
			continue
		}
		quoted[i] = quoteArg(a)
	}
	return strings.Join(quoted, " ")
}

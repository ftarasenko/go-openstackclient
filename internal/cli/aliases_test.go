package cli

import (
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Every command in this table is a real invocation from the shell-history parity
// pass that failed against koc because koc spells the noun differently from
// upstream. Resolving the path is the whole contract: if cobra cannot find it,
// the operator gets "unknown command" exactly as before.
func TestUpstreamSpellings_Resolve(t *testing.T) {
	paths := [][]string{
		// koc nests these under "server"; upstream has no prefix.
		{"console", "log", "show"},
		{"console", "url", "show"},
		{"migration", "list"},
		// The designate CLI spells "recordset set" as "recordset update".
		{"recordset", "update"},
		// The cinder CLI has "volume extend <vol> <size>" where koc has
		// "volume set --size".
		{"volume", "extend"},
		// Upstream ironic has "baremetal node reboot"; koc groups it under
		// "node power".
		{"baremetal", "node", "reboot"},
	}
	for _, path := range paths {
		t.Run(strings.Join(path, " "), func(t *testing.T) {
			root := NewRootCommand("test")
			found, rest, err := root.Find(path)
			if err != nil {
				t.Fatalf("Find(%v) error: %v", path, err)
			}
			if len(rest) != 0 {
				t.Fatalf("Find(%v) left %v unconsumed — the path resolved to %q, not the command", path, rest, found.Name())
			}
			if found.RunE == nil && found.Run == nil {
				t.Errorf("%v resolved to %q, which is a group rather than a runnable command", path, found.CommandPath())
			}
		})
	}
}

// The koc spellings must keep working: the aliases are additions, not renames.
func TestUpstreamSpellings_KocSpellingsStillWork(t *testing.T) {
	paths := [][]string{
		{"server", "console", "log", "show"},
		{"server", "console", "url", "show"},
		{"server", "migration", "list"},
		{"recordset", "set"},
		{"volume", "set"},
		{"baremetal", "node", "power", "reboot"},
	}
	for _, path := range paths {
		t.Run(strings.Join(path, " "), func(t *testing.T) {
			root := NewRootCommand("test")
			found, rest, err := root.Find(path)
			if err != nil || len(rest) != 0 {
				t.Fatalf("Find(%v) = %q, rest %v, err %v", path, found.CommandPath(), rest, err)
			}
			if found.RunE == nil && found.Run == nil {
				t.Errorf("%v resolved to a non-runnable %q", path, found.CommandPath())
			}
		})
	}
}

// A duplicate that shadows a visible sibling is hidden so `--help` does not list
// the same capability twice; the standalone upstream spellings stay visible.
func TestUpstreamSpellings_Visibility(t *testing.T) {
	root := NewRootCommand("test")
	hidden := map[string]bool{
		"migration":               true,
		"baremetal node reboot":   true,
		"console":                 false,
		"volume extend":           false,
		"server console log show": false,
	}
	for path, wantHidden := range hidden {
		found, _, err := root.Find(strings.Split(path, " "))
		if err != nil {
			t.Fatalf("Find(%q) error: %v", path, err)
		}
		if found.Hidden != wantHidden {
			t.Errorf("%q Hidden = %v, want %v", path, found.Hidden, wantHidden)
		}
	}
}

// The alias tree builds fresh command objects, since a cobra command has a single
// parent. Both spellings must nonetheless carry the same flags — that is the
// evidence they run the same code rather than having drifted apart.
func TestUpstreamSpellings_SameFlagsAsKocSpelling(t *testing.T) {
	pairs := []struct{ upstream, koc []string }{
		{[]string{"console", "log", "show"}, []string{"server", "console", "log", "show"}},
		{[]string{"console", "url", "show"}, []string{"server", "console", "url", "show"}},
		{[]string{"migration", "list"}, []string{"server", "migration", "list"}},
	}
	for _, pair := range pairs {
		t.Run(strings.Join(pair.upstream, " "), func(t *testing.T) {
			root := NewRootCommand("test")
			up, _, err := root.Find(pair.upstream)
			if err != nil {
				t.Fatalf("Find(%v): %v", pair.upstream, err)
			}
			kocCmd, _, err := root.Find(pair.koc)
			if err != nil {
				t.Fatalf("Find(%v): %v", pair.koc, err)
			}
			if got, want := flagNames(up), flagNames(kocCmd); got != want {
				t.Errorf("flags differ:\n  %v -> %s\n  %v -> %s", pair.upstream, got, pair.koc, want)
			}
		})
	}
}

func flagNames(cmd *cobra.Command) string {
	var names []string
	cmd.Flags().VisitAll(func(f *pflag.Flag) { names = append(names, f.Name) })
	sort.Strings(names)
	return strings.Join(names, ",")
}

package network

import (
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// The network surface used to be "covered" by command name while missing most
// of its flags: floating ip list took no filter at all, router create only
// --enable/--disable. This test walks the real command tree against every
// upstream option (upstream_flags_data_test.go) so a command can no longer be
// counted as covered while silently lacking what upstream accepts. There is no
// allow-list: every option of every implemented command, post-Zed ones
// included (they name their neutron extension instead, see extcheck.go).

func TestNetworkCommandsAcceptEveryUpstreamFlag(t *testing.T) {
	leaves := networkLeaves(t)
	var missing []string
	for cmdPath, options := range upstreamNetworkFlags {
		cmd, ok := leaves[cmdPath]
		if !ok {
			continue // a missing command is coverage.md's business, not this test's
		}
		for _, aliases := range options {
			if !hasAnyFlag(cmd, aliases) {
				missing = append(missing, cmdPath+" --"+strings.Join(aliases, "|--"))
			}
		}
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Errorf("koc %s: upstream option not registered", m)
	}
}

func hasAnyFlag(cmd *cobra.Command, aliases []string) bool {
	for _, a := range aliases {
		if cmd.Flags().Lookup(a) != nil {
			return true
		}
	}
	return false
}

// networkLeaves returns every runnable command of the network tree keyed by its
// path below the root ("floating ip list").
func networkLeaves(t *testing.T) map[string]*cobra.Command {
	t.Helper()
	root := &cobra.Command{Use: "koc"}
	root.AddCommand(NewCommand(&auth.Options{}, &output.Options{})...)
	out := map[string]*cobra.Command{}
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		if c.Runnable() && c != root {
			out[strings.TrimPrefix(c.CommandPath(), "koc ")] = c
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(root)
	return out
}

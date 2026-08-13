// Package allprojects registers the --all-projects flag shared by the compute and
// block-storage commands that reach across project boundaries.
//
// It exists for the flag's *default*: upstream OSC lets `ALL_PROJECTS` in the
// environment turn it on, so an operator who exports it once gets cross-project
// listings for the rest of the session (`openstackclient/compute/v2/server.py`
// passes `default=envvars.boolenv('ALL_PROJECTS')`). Without that, a script or
// shell profile written against `openstack` silently loses the setting under koc
// — no error, just a short list — which is the worst way for a parity gap to
// show up. Keeping the parsing in one place also keeps the two services agreeing
// on what counts as true.
package allprojects

import (
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// envVar is the environment variable upstream reads the default from.
const envVar = "ALL_PROJECTS"

// trueValues are the spellings upstream's bool_from_str accepts; anything else
// (including an unparseable value) is false, matching its non-strict mode.
var trueValues = []string{"1", "t", "true", "on", "y", "yes"}

// Default reports the value --all-projects takes when the flag is not given,
// read from ALL_PROJECTS.
func Default() bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(envVar)))
	if value == "" {
		return false
	}
	for _, t := range trueValues {
		if value == t {
			return true
		}
	}
	return false
}

// Bind registers --all-projects on cmd, storing into p and defaulting from the
// environment. help describes what the command does with it; the environment
// variable is appended so `--help` documents the override.
func Bind(cmd *cobra.Command, p *bool, help string) {
	cmd.Flags().BoolVar(p, "all-projects", Default(), help+" (can also be set with the ALL_PROJECTS envvar)")
}

package cli

import (
	"slices"
	"strings"
	"testing"
)

// The two abbreviations the history diff actually turned up (51 invocations
// between them) plus the boundary cases that decide whether expansion is safe.
func TestExpandFlagPrefixes(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			// 30 invocations: --all for --all-projects.
			name: "--all expands on a command that has --all-projects",
			args: []string{"volume", "list", "--all"},
			want: []string{"volume", "list", "--all-projects"},
		},
		{
			// 21 invocations: --fit for the global --fit-width.
			name: "--fit expands against an inherited global flag",
			args: []string{"server", "list", "--fit"},
			want: []string{"server", "list", "--fit-width"},
		},
		{
			name: "prefix with an inline value keeps the value",
			args: []string{"image", "list", "--form=json"},
			want: []string{"image", "list", "--format=json"},
		},
		{
			name: "prefix with a separate value leaves the value alone",
			args: []string{"image", "list", "--form", "json"},
			want: []string{"image", "list", "--format", "json"},
		},
		{
			// "server list" defines --all AND --all-projects, so --all is a real flag
			// and must never be rewritten to something else.
			name: "an exact flag name is never rewritten",
			args: []string{"server", "list", "--all"},
			want: []string{"server", "list", "--all"},
		},
		{
			// --os- prefixes many global flags; cobra should report it, not koc guess.
			name: "ambiguous prefix is left for cobra to reject",
			args: []string{"server", "list", "--os-"},
			want: []string{"server", "list", "--os-"},
		},
		{
			name: "unmatched prefix is left untouched",
			args: []string{"server", "list", "--nonesuch"},
			want: []string{"server", "list", "--nonesuch"},
		},
		{
			name: "short flags and clusters are untouched",
			args: []string{"server", "list", "-c", "ID", "-f", "value"},
			want: []string{"server", "list", "-c", "ID", "-f", "value"},
		},
		{
			name: "a bare -- stops expansion",
			args: []string{"server", "list", "--", "--form"},
			want: []string{"server", "list", "--", "--form"},
		},
		{
			name: "a lone -- is not treated as a flag",
			args: []string{"server", "list", "--"},
			want: []string{"server", "list", "--"},
		},
		{
			name: "no args",
			args: nil,
			want: nil,
		},
		{
			// The command is nonsense, but the global flags still expand and cobra
			// reports the unknown command as usual.
			name: "unknown command still expands global flags",
			args: []string{"nonesuch", "--form=json"},
			want: []string{"nonesuch", "--format=json"},
		},
		{
			name: "expansion happens per resolved command",
			args: []string{"baremetal", "node", "list", "--maint"},
			want: []string{"baremetal", "node", "list", "--maintenance"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := NewRootCommand("test")
			got := ExpandFlagPrefixes(root, tc.args)
			if !slices.Equal(got, tc.want) {
				t.Errorf("ExpandFlagPrefixes(%v)\n got %v\nwant %v", tc.args, got, tc.want)
			}
		})
	}
}

// Expansion must not mutate the caller's slice — main passes os.Args[1:].
func TestExpandFlagPrefixes_DoesNotMutateInput(t *testing.T) {
	root := NewRootCommand("test")
	args := []string{"volume", "list", "--all"}
	original := slices.Clone(args)
	_ = ExpandFlagPrefixes(root, args)
	if !slices.Equal(args, original) {
		t.Errorf("input slice was mutated: %v, want %v", args, original)
	}
}

// The expanded line must actually parse, which is the point of the exercise.
func TestExpandFlagPrefixes_ExpandedLineParses(t *testing.T) {
	root := NewRootCommand("test")
	args := ExpandFlagPrefixes(root, []string{"volume", "list", "--all", "--fit"})

	cmd, _, err := root.Find(args)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	// Parse the flags the way cobra will, against the resolved command.
	rest := args[len(strings.Fields(strings.TrimPrefix(cmd.CommandPath(), "koc "))):]
	if err := cmd.ParseFlags(rest); err != nil {
		t.Fatalf("the expanded line does not parse: %v (args %v)", err, args)
	}
	if !cmd.Flags().Changed("all-projects") {
		t.Error("--all did not reach --all-projects")
	}
	if !cmd.Flags().Changed("fit-width") && !cmd.InheritedFlags().Changed("fit-width") {
		t.Error("--fit did not reach --fit-width")
	}
}

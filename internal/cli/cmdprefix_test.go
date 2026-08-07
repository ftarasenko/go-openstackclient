package cli

import (
	"reflect"
	"strings"
	"testing"
)

func TestExpandCommandPrefixes(t *testing.T) {
	root := NewRootCommand("test")
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{
			// The reported case: `koc server li --lim 1` used to fail with
			// "unknown flag: --lim" because `li` did not resolve, so flag
			// expansion fell back to the `server` parent, which has no --limit.
			name: "unique verb prefix",
			in:   []string{"server", "li", "--lim", "1"},
			want: []string{"server", "list", "--lim", "1"},
		},
		{
			name: "unique noun and verb prefix",
			in:   []string{"zon", "li"},
			want: []string{"zone", "list"},
		},
		{
			name: "multi-word noun",
			in:   []string{"network", "trunk", "subp", "li"},
			want: []string{"network", "trunk", "subport", "list"},
		},
		{
			// "sh" prefixes both `zone share` and `zone show`; cliff accepts a
			// partial name only when exactly one command matches.
			name: "ambiguous verb prefix is left alone",
			in:   []string{"zone", "sh"},
			want: []string{"zone", "sh"},
		},
		{
			// "ser" prefixes security, server and service.
			name: "ambiguous noun prefix is left alone",
			in:   []string{"ser", "li"},
			want: []string{"ser", "li"},
		},
		{
			name: "no match is left alone",
			in:   []string{"server", "zzz"},
			want: []string{"server", "zzz"},
		},
		{
			name: "exact names are untouched",
			in:   []string{"server", "list", "--limit", "1"},
			want: []string{"server", "list", "--limit", "1"},
		},
		{
			// A positional argument after a resolved leaf must never be rewritten.
			name: "positional after a leaf is not a command word",
			in:   []string{"zone", "delete", "list"},
			want: []string{"zone", "delete", "list"},
		},
		{
			name: "flags before the noun are skipped",
			in:   []string{"--debug", "server", "li"},
			want: []string{"--debug", "server", "list"},
		},
		{
			// "li" here is --os-cloud's VALUE; rewriting it would corrupt the
			// invocation.
			name: "a flag value is never rewritten",
			in:   []string{"--os-cloud", "li", "server", "li"},
			want: []string{"--os-cloud", "li", "server", "list"},
		},
		{
			name: "inline flag value form",
			in:   []string{"--os-cloud=li", "server", "li"},
			want: []string{"--os-cloud=li", "server", "list"},
		},
		{
			name: "shorthand with a separate value is skipped",
			in:   []string{"-f", "json", "server", "li"},
			want: []string{"-f", "json", "server", "list"},
		},
		{
			name: "nothing after a bare terminator is examined",
			in:   []string{"--", "server", "li"},
			want: []string{"--", "server", "li"},
		},
		{
			name: "empty args",
			in:   nil,
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExpandCommandPrefixes(root, tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ExpandCommandPrefixes(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// The input slice must not be modified in place — main passes os.Args[1:].
func TestExpandCommandPrefixes_DoesNotMutateInput(t *testing.T) {
	root := NewRootCommand("test")
	in := []string{"server", "li"}
	_ = ExpandCommandPrefixes(root, in)
	if in[1] != "li" {
		t.Errorf("input was mutated: %v", in)
	}
}

// Command expansion must run before flag expansion, or an abbreviated flag is
// resolved against the wrong command's flag set. This is the end-to-end order
// main.go uses.
func TestCommandPrefixThenFlagPrefix(t *testing.T) {
	root := NewRootCommand("test")
	args := ExpandFlagPrefixes(root, ExpandCommandPrefixes(root, []string{"server", "li", "--lim", "1"}))
	want := []string{"server", "list", "--limit", "1"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("combined expansion = %v, want %v", args, want)
	}
	// And the reverse order does not work, which is why the order matters.
	reversed := ExpandCommandPrefixes(root, ExpandFlagPrefixes(root, []string{"server", "li", "--lim", "1"}))
	if reflect.DeepEqual(reversed, want) {
		t.Error("flag-first expansion unexpectedly resolved --lim; the ordering comment is stale")
	}
}

// An abbreviated command must actually run, not just rewrite.
func TestAbbreviatedCommandExecutes(t *testing.T) {
	root := NewRootCommand("test")
	out, err := execRootArgs(t, root, ExpandCommandPrefixes(root, []string{"server", "li", "--help"}))
	if err != nil {
		t.Fatalf("koc server li --help returned an error: %v", err)
	}
	if !strings.Contains(out, "List compute servers") {
		t.Errorf("koc server li --help did not reach `server list`:\n%s", out)
	}
}

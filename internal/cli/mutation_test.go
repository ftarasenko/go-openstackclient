package cli

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// A write verb that fails only while rendering says the write went through,
// and names the resource; a read verb and a pre-flight failure are untouched.
func TestGuardRenderErrors(t *testing.T) {
	o := &output.Options{Format: output.FormatValue, Columns: []string{"bogus"}}
	render := func(*cobra.Command, []string) error {
		return o.WriteSingle(io.Discard, []string{"id"}, []any{"abc"})
	}
	preflight := func(*cobra.Command, []string) error { return o.CheckColumns("id") }

	root := &cobra.Command{Use: "koc"}
	noun := &cobra.Command{Use: "thing"}
	root.AddCommand(noun)
	for _, c := range []*cobra.Command{
		{Use: "create", RunE: render},
		{Use: "show", RunE: render},
		{Use: "set", RunE: preflight},
	} {
		noun.AddCommand(c)
	}
	guardRenderErrors(root)

	for _, tc := range []struct {
		verb, want string
		wrapped    bool
	}{
		{"create", "koc thing create succeeded (id abc), but its output could not be rendered", true},
		{"show", "unknown column(s): bogus", false},
		{"set", "unknown column(s): bogus", false},
	} {
		root.SetArgs([]string{"thing", tc.verb})
		root.SetOut(io.Discard)
		root.SetErr(io.Discard)
		err := root.Execute()
		var ce *output.ColumnError
		if !errors.As(err, &ce) || !strings.HasPrefix(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want prefix %q", tc.verb, err, tc.want)
		}
		if got := strings.Contains(err.Error(), "succeeded"); got != tc.wrapped {
			t.Errorf("%s: wrapped = %v, want %v", tc.verb, got, tc.wrapped)
		}
	}
}

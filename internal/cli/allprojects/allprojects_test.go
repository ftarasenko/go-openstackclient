package allprojects

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestDefault_FromEnvironment(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{value: "", want: false},
		{value: "1", want: true},
		{value: "true", want: true},
		{value: "TRUE", want: true},
		{value: " yes ", want: true},
		{value: "on", want: true},
		{value: "0", want: false},
		{value: "no", want: false},
		// Upstream's bool_from_str is non-strict by default: an unrecognised value
		// is false rather than an error.
		{value: "banana", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.value, func(t *testing.T) {
			t.Setenv(envVar, tc.value)
			if got := Default(); got != tc.want {
				t.Errorf("Default() with %s=%q = %v, want %v", envVar, tc.value, got, tc.want)
			}
		})
	}
}

func TestBind_DefaultsFromEnvironment(t *testing.T) {
	t.Setenv(envVar, "yes")
	var got bool
	cmd := &cobra.Command{Use: "list"}
	Bind(cmd, &got, "list across all projects")
	if err := cmd.Flags().Parse(nil); err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if !got {
		t.Error("--all-projects should default to true when ALL_PROJECTS is set")
	}
	// An explicit flag still wins over the environment, in both directions.
	if err := cmd.Flags().Parse([]string{"--all-projects=false"}); err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if got {
		t.Error("--all-projects=false should override ALL_PROJECTS")
	}
}

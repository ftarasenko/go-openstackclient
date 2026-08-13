package nameflag

import "testing"

func TestResolve(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		flag     string
		required bool
		want     string
		wantErr  bool
	}{
		{name: "positional only", args: []string{"lb1"}, want: "lb1"},
		{name: "flag only", flag: "lb1", want: "lb1"},
		{name: "both agree", args: []string{"lb1"}, flag: "lb1", want: "lb1"},
		{name: "both differ", args: []string{"lb1"}, flag: "lb2", wantErr: true},
		{name: "neither, optional", want: ""},
		{name: "neither, required", required: true, wantErr: true},
		// An empty positional slice is the normal case for the flag form; an empty
		// string in it (e.g. `create ""`) must not shadow --name.
		{name: "empty positional falls back to flag", args: []string{""}, flag: "lb1", want: "lb1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Resolve(tc.args, tc.flag, tc.required)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Resolve(%q, %q, %v) = %q, want error", tc.args, tc.flag, tc.required, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve(%q, %q, %v) error: %v", tc.args, tc.flag, tc.required, err)
			}
			if got != tc.want {
				t.Errorf("Resolve(%q, %q, %v) = %q, want %q", tc.args, tc.flag, tc.required, got, tc.want)
			}
		})
	}
}

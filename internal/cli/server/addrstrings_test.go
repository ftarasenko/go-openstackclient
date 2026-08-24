package server

import (
	"reflect"
	"testing"
)

// addrStrings reads one network's entry out of a server's address map, which is
// decoded as map[string]any. Nova's shape is a list of objects with an "addr"
// key; anything else has to be skipped rather than panic in an operator's
// terminal, so each level of the check earns a row here.
func TestAddrStrings(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want []string
	}{
		{
			name: "a list of address objects",
			in: []any{
				map[string]any{"addr": "192.0.2.10", "version": float64(4)},
				map[string]any{"addr": "2001:db8::1", "version": float64(6)},
			},
			want: []string{"192.0.2.10", "2001:db8::1"},
		},
		{name: "an empty list", in: []any{}},
		{name: "a nil value", in: nil},
		{name: "not a list at all", in: "192.0.2.10"},
		{name: "a list of non-objects", in: []any{"192.0.2.10", 42}},
		{name: "an object with no addr key", in: []any{map[string]any{"version": float64(4)}}},
		{name: "an addr that is not a string", in: []any{map[string]any{"addr": float64(42)}}},
		{
			name: "the usable entries survive the unusable ones",
			in: []any{
				"junk",
				map[string]any{"addr": "192.0.2.10"},
				map[string]any{"version": float64(4)},
				map[string]any{"addr": "192.0.2.11"},
			},
			want: []string{"192.0.2.10", "192.0.2.11"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := addrStrings(tt.in)
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("addrStrings() = %v, want %v", got, tt.want)
			}
		})
	}
}

// formatNetworks composes addrStrings across networks: names sorted, "net=a, b"
// joined with "; ", matching the Networks column of `openstack server list`.
func TestFormatNetworks_Composition(t *testing.T) {
	got := formatNetworks(map[string]any{
		"private": []any{map[string]any{"addr": "10.0.0.5"}, map[string]any{"addr": "10.0.0.6"}},
		"public":  []any{map[string]any{"addr": "192.0.2.10"}},
		// A network whose value nova rendered in a shape koc does not know still
		// gets its name printed, with no addresses.
		"broken": "unexpected",
	})
	want := "broken=; private=10.0.0.5, 10.0.0.6; public=192.0.2.10"
	if got != want {
		t.Errorf("formatNetworks() = %q, want %q", got, want)
	}
	if got := formatNetworks(nil); got != "" {
		t.Errorf("formatNetworks(nil) = %q, want empty", got)
	}
}

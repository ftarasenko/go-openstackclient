package server

import "testing"

// TestFormatServerAddresses_NonMapFallsBackToScalar guards the forcetypeassert
// fix in formatServerAddresses: nova's "addresses" field is normally a map,
// but if a future response shape sends something else (a bare scalar here),
// the two-value assertion must degrade to scalarString instead of panicking
// on the old `flattenServerValue(v).(string)` cast.
func TestFormatServerAddresses_NonMapFallsBackToScalar(t *testing.T) {
	got := formatServerAddresses(float64(42))
	if want := "42"; got != want {
		t.Errorf("formatServerAddresses(42) = %q, want %q", got, want)
	}
}

// TestFormatServerAddresses_NilAndList exercise the two cases that still go
// through flattenServerValue after the fix, to make sure that path is
// unchanged.
func TestFormatServerAddresses_NilAndList(t *testing.T) {
	if got := formatServerAddresses(nil); got != "" {
		t.Errorf("formatServerAddresses(nil) = %q, want empty string", got)
	}
	if got := formatServerAddresses(map[string]any{
		"private": []any{map[string]any{"addr": "10.0.0.5"}},
	}); got != "private=10.0.0.5" {
		t.Errorf("formatServerAddresses(map) = %q, want %q", got, "private=10.0.0.5")
	}
}

// TestFormatListFlat_MixedElementsSkipsNonMap guards the forcetypeassert fix
// in formatListFlat's allMaps branch: it currently can't be reached with a
// non-map element (allMaps is checked first), but exercises the defensive
// two-value assertion doesn't regress if that invariant ever loosens.
func TestFormatListFlat_MixedElementsSkipsNonMap(t *testing.T) {
	got := formatListFlat([]any{map[string]any{"a": "1"}})
	if want := "a='1'"; got != want {
		t.Errorf("formatListFlat = %q, want %q", got, want)
	}
}

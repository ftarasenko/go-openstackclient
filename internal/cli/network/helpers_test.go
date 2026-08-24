package network

import (
	"context"
	"strings"
	"testing"
)

// parsePortRange is pure: no HTTP involved.
func TestParsePortRange(t *testing.T) {
	tests := []struct {
		name    string
		spec    string
		wantLo  int
		wantHi  int
		wantErr string
	}{
		{name: "single port", spec: "80", wantLo: 80, wantHi: 80},
		{name: "range", spec: "80:90", wantLo: 80, wantHi: 90},
		{name: "range with surrounding spaces", spec: " 80 : 90 ", wantLo: 80, wantHi: 90},
		{name: "zero port", spec: "0", wantLo: 0, wantHi: 0},
		{name: "non-numeric single", spec: "abc", wantErr: `parsing --dst-port "abc"`},
		{name: "non-numeric high end", spec: "80:abc", wantErr: `parsing --dst-port "80:abc"`},
		{name: "non-numeric low end", spec: "abc:90", wantErr: `parsing --dst-port "abc:90"`},
		{name: "empty", spec: "", wantErr: `parsing --dst-port ""`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lo, hi, err := parsePortRange(tc.spec)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("parsePortRange(%q) error = %v, want one containing %q", tc.spec, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parsePortRange(%q) unexpected error: %v", tc.spec, err)
			}
			if lo != tc.wantLo || hi != tc.wantHi {
				t.Errorf("parsePortRange(%q) = (%d, %d), want (%d, %d)", tc.spec, lo, hi, tc.wantLo, tc.wantHi)
			}
		})
	}
}

// resolveProjectRef's session-dependent branch (a non-UUID, non-empty ref)
// needs a real *auth.Client wired to a Keystone endpoint to exercise, which is
// outside this package's fake-client seam; the empty-ref and UUID short
// circuits below are the part of the function reachable without one, and
// they cover the two paths that make the "cost no keystone call" comment in
// helpers.go true.
func TestResolveProjectRef_ShortCircuitsWithoutASession(t *testing.T) {
	const uuid = "12345678-1234-1234-1234-123456789abc"
	tests := []struct {
		name string
		ref  string
		want string
	}{
		{name: "empty ref", ref: "", want: ""},
		{name: "UUID ref passes through untouched", ref: uuid, want: uuid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// A nil *auth.Client is safe here only because both branches return
			// before ever touching session; resolveProjectRef would panic on nil
			// if given a name that needed resolving.
			got, err := resolveProjectRef(context.Background(), nil, tc.ref, "")
			if err != nil {
				t.Fatalf("resolveProjectRef(%q) unexpected error: %v", tc.ref, err)
			}
			if got != tc.want {
				t.Errorf("resolveProjectRef(%q) = %q, want %q", tc.ref, got, tc.want)
			}
		})
	}
}

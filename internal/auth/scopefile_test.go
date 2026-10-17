package auth

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

const scopeFileJSON = `{
  "auth_url": "https://keystone.example.com/v3",
  "token": "gAAAAAsecret",
  "region": "RegionOne",
  "project_id": "0b1c2d3e4f5a6b7c8d9e0f1a2b3c4d5e",
  "project_name": "demo",
  "user_name": "alice",
  "expires_at": "2099-01-01T00:00:00Z",
  "cacert": "/etc/ssl/ca.pem"
}`

func TestParseScopeFile(t *testing.T) {
	sf, err := ParseScopeFile([]byte(scopeFileJSON))
	if err != nil {
		t.Fatal(err)
	}
	v := sf.FlagValues()
	for flag, want := range map[string]string{
		"os-auth-type":   "token",
		"os-token":       "gAAAAAsecret",
		"os-auth-url":    "https://keystone.example.com/v3",
		"os-region-name": "RegionOne",
		"os-project-id":  "0b1c2d3e4f5a6b7c8d9e0f1a2b3c4d5e",
		"os-cacert":      "/etc/ssl/ca.pem",
		"os-password":    "",
		"os-cloud":       "",
		"os-username":    "",
	} {
		if got, ok := v[flag]; !ok || got != want {
			t.Errorf("FlagValues()[%q] = %q (set %v), want %q", flag, got, ok, want)
		}
	}
	// Every name it sets must be a real global flag, or the scope silently
	// fails to apply.
	fs := envOptions(t).fs
	for flag := range v {
		if fs.Lookup(flag) == nil {
			t.Errorf("FlagValues sets %q, which is not a global flag", flag)
		}
	}
	if sf.Expired(time.Date(2098, 1, 1, 0, 0, 0, 0, time.UTC)) || !sf.Expired(time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Error("Expired is wrong around expires_at")
	}
	for _, format := range []string{"%v", "%+v", "%#v", "%s"} {
		if out := fmt.Sprintf(format, sf); strings.Contains(out, "secret") {
			t.Errorf("Sprintf(%q) leaks the token: %s", format, out)
		}
	}
}

func TestParseScopeFile_Errors(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`{"auth_url":"u","token":"gAAAAAsecret","project_id":"p"}`, "missing expires_at"},
		{`{"token":"gAAAAAsecret","expires_at":"2099-01-01T00:00:00Z"}`, "missing auth_url, project_id"},
		{`{"token":"gAAAAAsecret",`, "not valid JSON"},
		{`{"token":42}`, `"token" has the wrong type`},
		{`{"token":"gAAAAAsecret","password":"x"}`, "unknown field"},
	} {
		_, err := ParseScopeFile([]byte(tc.in))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("ParseScopeFile(%s) = %v, want %q", tc.in, err, tc.want)
			continue
		}
		if strings.Contains(err.Error(), "secret") {
			t.Errorf("the error quotes the token: %v", err)
		}
	}
}

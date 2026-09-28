package auth

import (
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/spf13/pflag"
)

// userDomain returns identity.password.user.domain from the token request
// gophercloud would send.
func userDomain(t *testing.T, ao *gophercloud.AuthOptions) map[string]any {
	t.Helper()
	m, err := ao.ToTokenV3CreateMap(nil)
	if err != nil {
		t.Fatalf("ToTokenV3CreateMap: %v", err)
	}
	auth, _ := m["auth"].(map[string]any)
	identity, _ := auth["identity"].(map[string]any)
	password, _ := identity["password"].(map[string]any)
	user, _ := password["user"].(map[string]any)
	dom, ok := user["domain"].(map[string]any)
	if !ok {
		t.Fatalf("token request has no user domain: %#v", m)
	}
	return dom
}

// projectScopeDomain returns scope.project.domain from the token request.
func projectScopeDomain(t *testing.T, ao *gophercloud.AuthOptions) map[string]any {
	t.Helper()
	proj, ok := scopeJSON(t, ao)["project"].(map[string]any)
	if !ok {
		t.Fatalf("expected a project scope, got %#v", scopeJSON(t, ao))
	}
	dom, ok := proj["domain"].(map[string]any)
	if !ok {
		t.Fatalf("project scope has no domain: %#v", proj)
	}
	return dom
}

// devstack's openrc names both domains by ID only. Without the -id spellings
// the project-by-name scope went out unqualified and gophercloud refused it.
func TestResolveAuth_EnvDomainIDsOnly(t *testing.T) {
	o := &Options{
		AuthURL:         "https://keystone.example/identity",
		Username:        "admin",
		Password:        "secret",
		ProjectName:     "admin",
		UserDomainID:    "default",
		ProjectDomainID: "default",
	}
	ao, _, _, err := o.resolveAuth()
	if err != nil {
		t.Fatalf("resolveAuth: %v", err)
	}
	if got := userDomain(t, &ao); got["id"] != "default" || got["name"] != nil {
		t.Errorf("user domain = %#v, want {id: default}", got)
	}
	if got := projectScopeDomain(t, &ao); got["id"] != "default" || got["name"] != nil {
		t.Errorf("project scope domain = %#v, want {id: default}", got)
	}
}

// The failure the devstack smoke run found: a --creds-from-vault openrc that
// carries only OS_{USER,PROJECT}_DOMAIN_ID used to be dropped key by key, and
// the token request then failed with "You must provide exactly one of DomainID
// or DomainName in a Scope with ProjectName".
func TestApplyOpenrcVars_DomainIDsOnly(t *testing.T) {
	o := &Options{}
	fs := pflag.NewFlagSet("koc", pflag.ContinueOnError)
	o.AddFlags(fs)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	o.applyOpenrcVars(map[string]string{
		"OS_AUTH_URL":          "https://keystone.example/identity",
		"OS_USERNAME":          "admin",
		"OS_PASSWORD":          "secret",
		"OS_PROJECT_NAME":      "admin",
		"OS_USER_DOMAIN_ID":    "default",
		"OS_PROJECT_DOMAIN_ID": "default",
	})

	ao, _, _, err := o.resolveAuth()
	if err != nil {
		t.Fatalf("resolveAuth: %v", err)
	}
	if got := userDomain(t, &ao); got["id"] != "default" {
		t.Errorf("user domain = %#v, want {id: default}", got)
	}
	if got := projectScopeDomain(t, &ao); got["id"] != "default" {
		t.Errorf("project scope domain = %#v, want {id: default}", got)
	}
}

// Each pair is one domain, so an ID and a name for it must not both reach
// gophercloud. The ID wins, except over a name typed on the command line when
// the ID only came from a sourced openrc.
func TestApplyDomainScope_IDAndNameForSameDomain(t *testing.T) {
	for _, tc := range []struct {
		name     string
		envID    string
		args     []string
		wantID   string
		wantName string
	}{
		{"env id only", "default", nil, "default", ""},
		{"explicit name beats env id", "default", []string{"--os-user-domain-name", "Other"}, "", "Other"},
		{"explicit id beats explicit name", "", []string{"--os-user-domain-id", "d1", "--os-user-domain-name", "Other"}, "d1", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OS_USER_DOMAIN_ID", tc.envID)
			o := &Options{}
			fs := pflag.NewFlagSet("koc", pflag.ContinueOnError)
			o.AddFlags(fs)
			if err := fs.Parse(append([]string{"--os-auth-url", "https://keystone.example", "--os-username", "u", "--os-password", "p", "--os-project-name", "proj"}, tc.args...)); err != nil {
				t.Fatal(err)
			}
			ao, _, _, err := o.resolveAuth()
			if err != nil {
				t.Fatalf("resolveAuth: %v", err)
			}
			if ao.DomainID != tc.wantID || ao.DomainName != tc.wantName {
				t.Errorf("user domain = {id %q, name %q}, want {id %q, name %q}", ao.DomainID, ao.DomainName, tc.wantID, tc.wantName)
			}
			// The project scope falls back to the user's domain and must be
			// just as unambiguous.
			if _, err := ao.ToTokenV3ScopeMap(); err != nil {
				t.Errorf("scope map: %v", err)
			}
		})
	}
}

func TestApplyDomainScope_DomainScopedTokenByID(t *testing.T) {
	o := &Options{Username: "admin", DomainID: "d1"}
	ao := gophercloud.AuthOptions{Username: "admin"}
	o.applyAuthOverrides(&ao)

	dom, ok := scopeJSON(t, &ao)["domain"].(map[string]any)
	if !ok || dom["id"] != "d1" || dom["name"] != nil {
		t.Errorf("domain scope = %#v, want {id: d1}", scopeJSON(t, &ao))
	}
}

func TestValidateSystemScope_ConflictsWithDomainID(t *testing.T) {
	o := &Options{}
	fs := pflag.NewFlagSet("koc", pflag.ContinueOnError)
	o.AddFlags(fs)
	if err := fs.Parse([]string{"--os-system-scope", "all", "--os-domain-id", "d1"}); err != nil {
		t.Fatal(err)
	}
	err := o.validateSystemScope()
	if err == nil || !strings.Contains(err.Error(), "--os-domain-id") {
		t.Errorf("validateSystemScope = %v, want a conflict naming --os-domain-id", err)
	}
}

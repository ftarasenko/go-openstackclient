package auth

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/spf13/pflag"
)

// scopeJSON runs the resolved AuthOptions through gophercloud's scope map and
// returns it as generic JSON for assertions.
func scopeJSON(t *testing.T, ao *gophercloud.AuthOptions) map[string]any {
	t.Helper()
	m, err := ao.ToTokenV3ScopeMap()
	if err != nil {
		t.Fatalf("ToTokenV3ScopeMap: %v", err)
	}
	// Round-trip through JSON so pointer values compare as plain strings.
	b, _ := json.Marshal(m)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

func TestApplyDomainScope_DistinctUserAndProjectDomains(t *testing.T) {
	o := &Options{
		Username:          "alice",
		ProjectName:       "proj",
		UserDomainName:    "UserDom",
		ProjectDomainName: "ProjDom",
	}
	ao := gophercloud.AuthOptions{Username: "alice"}
	o.applyAuthOverrides(&ao)

	// User identity domain must be the user's domain, not the project's.
	if ao.DomainName != "UserDom" {
		t.Errorf("user identity domain = %q, want UserDom", ao.DomainName)
	}
	// Scope must carry the PROJECT's domain.
	scope := scopeJSON(t, &ao)
	proj, ok := scope["project"].(map[string]any)
	if !ok {
		t.Fatalf("scope has no project: %#v", scope)
	}
	if proj["name"] != "proj" {
		t.Errorf("scope project name = %v, want proj", proj["name"])
	}
	dom, _ := proj["domain"].(map[string]any)
	if dom["name"] != "ProjDom" {
		t.Errorf("scope project domain = %v, want ProjDom", dom["name"])
	}
}

func TestApplyDomainScope_SingleDomainCommonCase(t *testing.T) {
	o := &Options{
		Username:          "admin",
		ProjectName:       "admin",
		UserDomainName:    "Default",
		ProjectDomainName: "Default",
	}
	ao := gophercloud.AuthOptions{Username: "admin"}
	o.applyAuthOverrides(&ao)

	if ao.DomainName != "Default" {
		t.Errorf("user domain = %q, want Default", ao.DomainName)
	}
	scope := scopeJSON(t, &ao)
	proj := scope["project"].(map[string]any)
	dom := proj["domain"].(map[string]any)
	if proj["name"] != "admin" || dom["name"] != "Default" {
		t.Errorf("unexpected project scope: %#v", proj)
	}
}

func TestApplyDomainScope_ProjectByID(t *testing.T) {
	o := &Options{ProjectID: "pid-123", UserDomainName: "Default"}
	ao := gophercloud.AuthOptions{}
	o.applyAuthOverrides(&ao)

	scope := scopeJSON(t, &ao)
	proj := scope["project"].(map[string]any)
	if proj["id"] != "pid-123" {
		t.Errorf("scope project id = %v, want pid-123", proj["id"])
	}
	if _, hasDomain := proj["domain"]; hasDomain {
		t.Errorf("project-by-id scope must not carry a domain: %#v", proj)
	}
}

func TestApplyDomainScope_DomainScopedToken(t *testing.T) {
	o := &Options{Username: "alice", UserDomainName: "UserDom", DomainName: "Target"}
	ao := gophercloud.AuthOptions{Username: "alice"}
	o.applyAuthOverrides(&ao)

	if ao.DomainName != "UserDom" {
		t.Errorf("user domain = %q, want UserDom", ao.DomainName)
	}
	scope := scopeJSON(t, &ao)
	dom, ok := scope["domain"].(map[string]any)
	if !ok {
		t.Fatalf("expected a domain scope, got %#v", scope)
	}
	if dom["name"] != "Target" {
		t.Errorf("domain scope = %v, want Target", dom["name"])
	}
}

func TestResolveAuth_EnvSplitDomainNoProjectID(t *testing.T) {
	// The standard v3 openrc: project name + project domain, no OS_PROJECT_ID.
	// gophercloud's AuthOptionsFromEnv would reject this; koc must not.
	o := &Options{
		AuthURL:           "https://keystone.example/v3",
		Username:          "admin",
		Password:          "secret",
		ProjectName:       "admin",
		ProjectDomainName: "Default",
		UserDomainName:    "Default",
	}
	ao, _, _, err := o.resolveAuth()
	if err != nil {
		t.Fatalf("resolveAuth should succeed without OS_PROJECT_ID: %v", err)
	}
	if ao.IdentityEndpoint != "https://keystone.example/v3" || ao.Username != "admin" || ao.Password != "secret" {
		t.Errorf("auth options not populated from env fields: %+v", ao)
	}
	scope := scopeJSON(t, &ao)
	proj, ok := scope["project"].(map[string]any)
	if !ok {
		t.Fatalf("expected a project scope, got %#v", scope)
	}
	dom := proj["domain"].(map[string]any)
	if proj["name"] != "admin" || dom["name"] != "Default" {
		t.Errorf("unexpected project scope: %#v", proj)
	}
}

func TestResolveAuth_EnvMissingAuthURL(t *testing.T) {
	o := &Options{Username: "admin", Password: "x"}
	if _, _, _, err := o.resolveAuth(); err == nil {
		t.Error("expected an error when no cloud and no OS_AUTH_URL are set")
	}
}

func TestApplyDomainScope_NoDomainFlagsLeavesScopeUntouched(t *testing.T) {
	// Mirrors the clouds.yaml path: gophercloud already set TenantName and a
	// DomainName; with no koc domain flags we must not clobber that scoping.
	o := &Options{}
	ao := gophercloud.AuthOptions{TenantName: "proj", DomainName: "cloudsDom"}
	o.applyAuthOverrides(&ao)

	if ao.Scope != nil {
		t.Errorf("scope should be left for gophercloud to derive, got %#v", ao.Scope)
	}
	if ao.DomainName != "cloudsDom" {
		t.Errorf("clouds.yaml domain must be preserved, got %q", ao.DomainName)
	}
}

func TestApplySystemScope_ProducesSystemAllScope(t *testing.T) {
	o := &Options{
		Username:       "admin",
		UserDomainName: "Default",
		SystemScope:    "all",
	}
	ao := gophercloud.AuthOptions{Username: "admin"}
	o.applyAuthOverrides(&ao)

	scope := scopeJSON(t, &ao)
	sys, ok := scope["system"].(map[string]any)
	if !ok {
		t.Fatalf("scope has no system key: %#v", scope)
	}
	if sys["all"] != true {
		t.Errorf(`scope system = %#v, want {"all": true}`, sys)
	}
	// The user's identity domain still qualifies the username.
	if ao.DomainName != "Default" {
		t.Errorf("user identity domain = %q, want Default", ao.DomainName)
	}
}

// A system-scoped token carries no project, so a project inherited from the
// environment / clouds.yaml / a Vault openrc must be dropped rather than
// producing a project scope.
func TestApplySystemScope_OverridesInheritedProjectScope(t *testing.T) {
	o := &Options{
		Username:          "admin",
		ProjectName:       "admin",
		UserDomainName:    "Default",
		ProjectDomainName: "Default",
		SystemScope:       "all",
	}
	ao := gophercloud.AuthOptions{Username: "admin", TenantName: "admin"}
	o.applyAuthOverrides(&ao)

	if ao.TenantName != "" || ao.TenantID != "" {
		t.Errorf("tenant fields not cleared: name=%q id=%q", ao.TenantName, ao.TenantID)
	}
	scope := scopeJSON(t, &ao)
	if _, ok := scope["project"]; ok {
		t.Errorf("system-scoped token must not carry a project scope: %#v", scope)
	}
	if _, ok := scope["system"]; !ok {
		t.Errorf("scope is not system-scoped: %#v", scope)
	}
}

func TestValidateSystemScope(t *testing.T) {
	tests := []struct {
		name        string
		scope       string
		alsoChanged []string
		wantErr     string
	}{
		{name: "unset", scope: ""},
		{name: "all", scope: "all"},
		{name: "bad value", scope: "everything", wantErr: "unsupported value"},
		{
			name:  "conflicts with explicit project name",
			scope: "all", alsoChanged: []string{"os-project-name"},
			wantErr: "mutually exclusive",
		},
		{
			name:  "conflicts with explicit domain name",
			scope: "all", alsoChanged: []string{"os-domain-name"},
			wantErr: "mutually exclusive",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := &Options{}
			fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
			o.AddFlags(fs)
			args := []string{}
			if tc.scope != "" {
				args = append(args, "--os-system-scope="+tc.scope)
			}
			for _, name := range tc.alsoChanged {
				args = append(args, "--"+name+"=x")
			}
			if err := fs.Parse(args); err != nil {
				t.Fatalf("parsing %v: %v", args, err)
			}

			err := o.validateSystemScope()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("validateSystemScope() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("validateSystemScope() = %v, want an error containing %q", err, tc.wantErr)
			}
		})
	}
}

// An OS_PROJECT_NAME left in the environment is background configuration, not a
// conflicting request: --os-system-scope overrides it silently.
func TestValidateSystemScope_EnvProjectIsNotAConflict(t *testing.T) {
	t.Setenv("OS_PROJECT_NAME", "admin")
	o := &Options{}
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	o.AddFlags(fs)
	if err := fs.Parse([]string{"--os-system-scope=all"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := o.validateSystemScope(); err != nil {
		t.Fatalf("validateSystemScope() = %v, want nil", err)
	}
}

// envOpenrc exports the variables a sourced openrc would set, so every auth
// flag AddFlags registers picks up a non-empty default.
func envOpenrc(t *testing.T) {
	t.Helper()
	t.Setenv("OS_AUTH_URL", "https://env-cloud.example:5000")
	t.Setenv("OS_USERNAME", "envuser")
	t.Setenv("OS_PASSWORD", "envsecret")
	t.Setenv("OS_PROJECT_NAME", "envproject")
	t.Setenv("OS_PROJECT_DOMAIN_NAME", "EnvDom")
	t.Setenv("OS_USER_DOMAIN_NAME", "EnvDom")
	t.Setenv("OS_REGION_NAME", "envregion")
	t.Setenv("OS_INTERFACE", "admin")
}

// cloudsYAMLResult is what clouds.Parse would hand back for the named cloud.
func cloudsYAMLResult() (gophercloud.AuthOptions, gophercloud.EndpointOpts) {
	return gophercloud.AuthOptions{
			IdentityEndpoint: "https://named-cloud.example:5000",
			Username:         "cloudsuser",
			Password:         "cloudssecret",
			TenantName:       "cloudsproject",
			DomainName:       "CloudsDom",
		}, gophercloud.EndpointOpts{
			Region:       "cloudsregion",
			Availability: gophercloud.AvailabilityPublic,
		}
}

// A named cloud must be used wholesale. Every auth flag defaults to its OS_*
// variable, so before the fix a sourced openrc silently redirected
// "--os-cloud other" at the environment's cloud, credentials included.
func TestOverride_NamedCloudIsNotClobberedByEnvDefaults(t *testing.T) {
	envOpenrc(t)
	o := &Options{}
	fs := pflag.NewFlagSet("koc", pflag.ContinueOnError)
	o.AddFlags(fs)
	if err := fs.Parse([]string{"--os-cloud", "named"}); err != nil {
		t.Fatalf("parse: %v", err)
	}

	ao, eo := cloudsYAMLResult()
	o.applyAuthOverrides(&ao)
	o.applyEndpointOverrides(&eo)

	for _, tc := range []struct{ field, got, want string }{
		{"auth URL", ao.IdentityEndpoint, "https://named-cloud.example:5000"},
		{"username", ao.Username, "cloudsuser"},
		{"password", ao.Password, "cloudssecret"},
		{"project", ao.TenantName, "cloudsproject"},
		{"user domain", ao.DomainName, "CloudsDom"},
		{"region", eo.Region, "cloudsregion"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q (the named cloud's value)", tc.field, tc.got, tc.want)
		}
	}
	if eo.Availability != gophercloud.AvailabilityPublic {
		t.Errorf("interface = %q, want public (OS_INTERFACE must not win)", eo.Availability)
	}
	// The env's project domain must not have rewritten the scope either.
	if ao.Scope != nil {
		t.Errorf("scope = %#v, want nil (no domain flag was given)", ao.Scope)
	}
}

// The flags the operator actually types still outrank the named cloud.
func TestOverride_ExplicitFlagsStillBeatNamedCloud(t *testing.T) {
	envOpenrc(t)
	o := &Options{}
	fs := pflag.NewFlagSet("koc", pflag.ContinueOnError)
	o.AddFlags(fs)
	err := fs.Parse([]string{
		"--os-cloud", "named",
		"--os-username", "flaguser",
		"--os-project-name", "flagproject",
		"--os-region-name", "flagregion",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	ao, eo := cloudsYAMLResult()
	o.applyAuthOverrides(&ao)
	o.applyEndpointOverrides(&eo)

	if ao.Username != "flaguser" {
		t.Errorf("username = %q, want flaguser", ao.Username)
	}
	if ao.TenantName != "flagproject" {
		t.Errorf("project = %q, want flagproject", ao.TenantName)
	}
	if eo.Region != "flagregion" {
		t.Errorf("region = %q, want flagregion", eo.Region)
	}
	// Untouched flags keep the cloud's values.
	if ao.IdentityEndpoint != "https://named-cloud.example:5000" {
		t.Errorf("auth URL = %q, want the named cloud's", ao.IdentityEndpoint)
	}
}

// Without --os-cloud the OS_* variables ARE the configuration, so gating them
// must not break the plain-openrc path.
func TestOverride_EnvPathStillAppliesWithoutACloud(t *testing.T) {
	envOpenrc(t)
	o := &Options{}
	fs := pflag.NewFlagSet("koc", pflag.ContinueOnError)
	o.AddFlags(fs)
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("parse: %v", err)
	}

	ao, _, _, err := o.resolveAuth()
	if err != nil {
		t.Fatalf("resolveAuth: %v", err)
	}
	if ao.IdentityEndpoint != "https://env-cloud.example:5000" || ao.Username != "envuser" {
		t.Errorf("env auth options not applied: %+v", ao)
	}
	scope := scopeJSON(t, &ao)
	proj, ok := scope["project"].(map[string]any)
	if !ok {
		t.Fatalf("expected a project scope, got %#v", scope)
	}
	if proj["name"] != "envproject" {
		t.Errorf("scope project = %v, want envproject", proj["name"])
	}
}

// A --creds-from-vault openrc outranks clouds.yaml even though pflag never saw
// those values on the command line.
func TestOverride_VaultOpenrcOutranksNamedCloud(t *testing.T) {
	envOpenrc(t)
	o := &Options{}
	fs := pflag.NewFlagSet("koc", pflag.ContinueOnError)
	o.AddFlags(fs)
	if err := fs.Parse([]string{"--os-cloud", "named"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	o.applyOpenrcVars(map[string]string{
		"OS_AUTH_URL": "https://vault-cloud.example:5000",
		"OS_USERNAME": "vaultuser",
	})

	ao, _ := cloudsYAMLResult()
	o.applyAuthOverrides(&ao)

	if ao.IdentityEndpoint != "https://vault-cloud.example:5000" {
		t.Errorf("auth URL = %q, want the vault openrc's", ao.IdentityEndpoint)
	}
	if ao.Username != "vaultuser" {
		t.Errorf("username = %q, want vaultuser", ao.Username)
	}
	// A field the openrc did not carry still comes from the named cloud.
	if ao.Password != "cloudssecret" {
		t.Errorf("password = %q, want the named cloud's", ao.Password)
	}
}

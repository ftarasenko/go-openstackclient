package auth

import (
	"encoding/json"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
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

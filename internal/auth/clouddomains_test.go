package auth

import (
	"testing"

	"github.com/gophercloud/gophercloud/v2"
)

// gophercloud v2.15.0 split the user domain from the project domain in
// clouds.Parse and stopped coalescing them. These cases are written against the
// AuthOptions shape Parse returns, not against Parse itself, so they hold
// whichever version is vendored.
func TestReconcileCloudDomains(t *testing.T) {
	tests := []struct {
		name string
		in   gophercloud.AuthOptions
		// want* are the user domain and the scope's project domain after
		// reconciliation.
		wantUserDomain    string
		wantProjectDomain string
	}{
		{
			name: "only project_domain_name: the user borrows it",
			in: gophercloud.AuthOptions{
				Username: "u", TenantName: "proj",
				Scope: &gophercloud.AuthScope{ProjectName: "proj", DomainName: "ProjDom"},
			},
			wantUserDomain: "ProjDom", wantProjectDomain: "ProjDom",
		},
		{
			name: "only user_domain_name: the project scope borrows it",
			in: gophercloud.AuthOptions{
				Username: "u", DomainName: "UserDom", TenantName: "proj",
				Scope: &gophercloud.AuthScope{ProjectName: "proj"},
			},
			wantUserDomain: "UserDom", wantProjectDomain: "UserDom",
		},
		{
			name: "both named: neither is touched",
			in: gophercloud.AuthOptions{
				Username: "u", DomainName: "UserDom", TenantName: "proj",
				Scope: &gophercloud.AuthScope{ProjectName: "proj", DomainName: "ProjDom"},
			},
			wantUserDomain: "UserDom", wantProjectDomain: "ProjDom",
		},
		{
			name: "project by ID needs no project domain, user still borrows nothing",
			in: gophercloud.AuthOptions{
				Username: "u", DomainName: "UserDom",
				Scope: &gophercloud.AuthScope{ProjectID: "p-1"},
			},
			wantUserDomain: "UserDom", wantProjectDomain: "",
		},
		{
			name: "gophercloud < 2.15.0 returns no scope: nothing to do",
			in: gophercloud.AuthOptions{
				Username: "u", DomainName: "CloudsDom", TenantName: "proj",
			},
			wantUserDomain: "CloudsDom", wantProjectDomain: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ao := tc.in
			reconcileCloudDomains(&ao)
			if ao.DomainName != tc.wantUserDomain {
				t.Errorf("user domain = %q, want %q", ao.DomainName, tc.wantUserDomain)
			}
			var gotProjectDomain string
			if ao.Scope != nil {
				gotProjectDomain = ao.Scope.DomainName
			}
			if gotProjectDomain != tc.wantProjectDomain {
				t.Errorf("scope domain = %q, want %q", gotProjectDomain, tc.wantProjectDomain)
			}
		})
	}
}

// The ID spellings coalesce the same way the name spellings do.
func TestReconcileCloudDomains_IDs(t *testing.T) {
	ao := gophercloud.AuthOptions{
		Username: "u", TenantName: "proj",
		Scope: &gophercloud.AuthScope{ProjectName: "proj", DomainID: "dom-1"},
	}
	reconcileCloudDomains(&ao)
	if ao.DomainID != "dom-1" {
		t.Errorf("user DomainID = %q, want dom-1", ao.DomainID)
	}
}

// A system- or trust-scoped token is not domain-qualified, so reconciliation
// must leave it exactly as gophercloud built it.
func TestReconcileCloudDomains_SystemAndTrustUntouched(t *testing.T) {
	for _, tc := range []struct {
		name  string
		scope *gophercloud.AuthScope
	}{
		{"system", &gophercloud.AuthScope{System: true}},
		{"trust", &gophercloud.AuthScope{TrustID: "t-1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ao := gophercloud.AuthOptions{Username: "u", Scope: tc.scope}
			reconcileCloudDomains(&ao)
			if ao.DomainName != "" || ao.DomainID != "" {
				t.Errorf("%s scope must not gain a user domain, got %q/%q", tc.name, ao.DomainID, ao.DomainName)
			}
		})
	}
}

// An explicit koc flag still outranks the reconciled clouds.yaml value: the
// flag path runs after this one and overwrites both halves.
func TestReconcileCloudDomains_FlagsStillWin(t *testing.T) {
	ao := gophercloud.AuthOptions{
		Username: "u", TenantName: "proj",
		Scope: &gophercloud.AuthScope{ProjectName: "proj", DomainName: "ProjDom"},
	}
	reconcileCloudDomains(&ao)

	o := &Options{UserDomainName: "FlagDom", ProjectName: "proj"}
	o.applyAuthOverrides(&ao)

	if ao.DomainName != "FlagDom" {
		t.Errorf("user domain = %q, want the flag to win with FlagDom", ao.DomainName)
	}
	if ao.Scope == nil || ao.Scope.DomainName != "FlagDom" {
		t.Errorf("scope = %#v, want the flag's domain", ao.Scope)
	}
}

package identity

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/tokens"
	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// --- runRegionShow -----------------------------------------------------------

func TestRunRegionShow_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotPath string
	fakeServer.Mux.HandleFunc("/regions/RegionOne", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"region": {"id": "RegionOne", "parent_region_id": "", "description": "primary region"}}`))
	})

	client := identityClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}
	var out bytes.Buffer
	if err := runRegionShow(context.Background(), client, o, "RegionOne", &out); err != nil {
		t.Fatalf("runRegionShow returned error: %v", err)
	}
	th.AssertEquals(t, http.MethodGet, gotMethod)
	th.AssertEquals(t, "/regions/RegionOne", gotPath)
	for _, want := range []string{"RegionOne", "primary region"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestRunRegionShow_NotFoundErrors(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/regions/missing", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	client := identityClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}
	var out bytes.Buffer
	err := runRegionShow(context.Background(), client, o, "missing", &out)
	if err == nil {
		t.Fatal("expected an error for a 404 response")
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Errorf("error should name the region ref, got: %v", err)
	}
}

// --- runServiceDelete ---------------------------------------------------------

func TestRunServiceDelete_ResolvesAndDeletesEachRef(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/services", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"services": [{"id": "svc-nova", "name": "nova", "type": "compute"}]}`))
	})
	var deleted []string
	fakeServer.Mux.HandleFunc("/services/svc-nova", func(w http.ResponseWriter, r *http.Request) {
		deleted = append(deleted, r.URL.Path)
		th.AssertEquals(t, http.MethodDelete, r.Method)
		w.WriteHeader(http.StatusNoContent)
	})
	// A ref with zero name matches falls back to the literal, per resolve.go.
	fakeServer.Mux.HandleFunc("/services/svc-literal", func(w http.ResponseWriter, r *http.Request) {
		deleted = append(deleted, r.URL.Path)
		th.AssertEquals(t, http.MethodDelete, r.Method)
		w.WriteHeader(http.StatusNoContent)
	})

	client := identityClient(fakeServer)
	if err := runServiceDelete(context.Background(), client, []string{"nova", "svc-literal"}); err != nil {
		t.Fatalf("runServiceDelete returned error: %v", err)
	}
	if len(deleted) != 2 {
		t.Fatalf("expected both refs to be deleted, got %v", deleted)
	}
}

func TestRunServiceDelete_PropagatesPerRefErrors(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/services", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"services": []}`))
	})
	var attempted []string
	fakeServer.Mux.HandleFunc("/services/bad", func(w http.ResponseWriter, r *http.Request) {
		attempted = append(attempted, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	})
	fakeServer.Mux.HandleFunc("/services/good", func(w http.ResponseWriter, r *http.Request) {
		attempted = append(attempted, r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	})

	client := identityClient(fakeServer)
	err := runServiceDelete(context.Background(), client, []string{"bad", "good"})
	if err == nil {
		t.Fatal("expected the failing ref to produce an error")
	}
	if !strings.Contains(err.Error(), "bad") {
		t.Errorf("error should name the failing ref, got: %v", err)
	}
	// batchdelete.Each never stops at the first failure.
	if len(attempted) != 2 {
		t.Fatalf("expected both refs to be attempted despite the first failing, got %v", attempted)
	}
}

// --- runImpliedRoleDelete ------------------------------------------------------

func TestRunImpliedRoleDelete_ResolvesBothThenDeletes(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/roles", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.RawQuery, "member") {
			_, _ = w.Write([]byte(`{"roles": [{"id": "member-id", "name": "member"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"roles": [{"id": "admin-id", "name": "admin"}]}`))
	})
	var gotMethod, gotPath string
	fakeServer.Mux.HandleFunc("/roles/admin-id/implies/member-id", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})

	client := identityClient(fakeServer)
	if err := runImpliedRoleDelete(context.Background(), client, "admin", "member"); err != nil {
		t.Fatalf("runImpliedRoleDelete returned error: %v", err)
	}
	th.AssertEquals(t, http.MethodDelete, gotMethod)
	th.AssertEquals(t, "/roles/admin-id/implies/member-id", gotPath)
}

func TestRunImpliedRoleDelete_DeleteFails(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/roles", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"roles": [{"id": "role-id", "name": "reader"}]}`))
	})
	fakeServer.Mux.HandleFunc("/roles/role-id/implies/role-id", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	client := identityClient(fakeServer)
	err := runImpliedRoleDelete(context.Background(), client, "reader", "reader")
	if err == nil {
		t.Fatal("expected a 404 on the delete to produce an error")
	}
	if !strings.Contains(err.Error(), "reader") {
		t.Errorf("error should name the role refs, got: %v", err)
	}
}

// --- resolveAppCredUser / currentUserID ---------------------------------------

func TestResolveAppCredUser_EmptyFlagUsesCurrentUser(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	// No /users endpoint is registered: an empty --user flag must short-circuit
	// to currentUserID and never hit the resolver's list call.
	client := identityClient(fakeServer)

	ac := &auth.Client{Provider: &gophercloud.ProviderClient{}}
	if err := ac.Provider.SetTokenAndAuthResult(authResultWithUserID(t, "u-current")); err != nil {
		t.Fatalf("SetTokenAndAuthResult: %v", err)
	}

	id, err := resolveAppCredUser(context.Background(), client, ac, "")
	if err != nil {
		t.Fatalf("resolveAppCredUser returned error: %v", err)
	}
	th.AssertEquals(t, "u-current", id)
}

func TestResolveAppCredUser_NonEmptyFlagResolvesUser(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/users", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"users": [{"id": "u-alice", "name": "alice"}]}`))
	})

	client := identityClient(fakeServer)
	// A nil Provider is never touched when --user is non-empty.
	ac := &auth.Client{}
	id, err := resolveAppCredUser(context.Background(), client, ac, "alice")
	if err != nil {
		t.Fatalf("resolveAppCredUser returned error: %v", err)
	}
	th.AssertEquals(t, "u-alice", id)
}

// authResultWithUserID builds a tokens.CreateResult carrying the given user ID,
// suitable for gophercloud.ProviderClient.SetTokenAndAuthResult.
func authResultWithUserID(t *testing.T, userID string) tokens.CreateResult {
	t.Helper()
	var r tokens.CreateResult
	r.Header = http.Header{"X-Subject-Token": []string{"tok-1"}}
	r.Body = map[string]any{
		"token": map[string]any{
			"user": map[string]any{"id": userID, "name": "whoever"},
		},
	}
	return r
}

func TestCurrentUserID_Success(t *testing.T) {
	ac := &auth.Client{Provider: &gophercloud.ProviderClient{}}
	if err := ac.Provider.SetTokenAndAuthResult(authResultWithUserID(t, "u-99")); err != nil {
		t.Fatalf("SetTokenAndAuthResult: %v", err)
	}
	id, err := currentUserID(ac)
	if err != nil {
		t.Fatalf("currentUserID returned error: %v", err)
	}
	th.AssertEquals(t, "u-99", id)
}

func TestCurrentUserID_ErrorsWithoutAuthResult(t *testing.T) {
	// A ProviderClient that never authenticated has no stored AuthResult, so the
	// type assertion to tokens.CreateResult fails.
	ac := &auth.Client{Provider: &gophercloud.ProviderClient{}}
	if _, err := currentUserID(ac); err == nil {
		t.Fatal("expected an error when no auth result is stored")
	}
}

func TestCurrentUserID_ErrorsOnMalformedBody(t *testing.T) {
	ac := &auth.Client{Provider: &gophercloud.ProviderClient{}}
	var r tokens.CreateResult
	r.Header = http.Header{"X-Subject-Token": []string{"tok-2"}}
	// A non-object body cannot be unmarshalled into the token's user struct.
	r.Body = "not-an-object"
	if err := ac.Provider.SetTokenAndAuthResult(r); err != nil {
		t.Fatalf("SetTokenAndAuthResult: %v", err)
	}
	if _, err := currentUserID(ac); err == nil {
		t.Fatal("expected an error extracting the user from a malformed body")
	}
}

// --- authenticatedUserID -------------------------------------------------------

func TestAuthenticatedUserID_Success(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const token = "subject-token-1"
	var gotSubject string
	fakeServer.Mux.HandleFunc("/auth/tokens", func(w http.ResponseWriter, r *http.Request) {
		gotSubject = r.Header.Get("X-Subject-Token")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"token": {"user": {"id": "u-token", "name": "bob"}}}`))
	})

	client := identityClient(fakeServer)
	id, err := authenticatedUserID(context.Background(), client, token)
	if err != nil {
		t.Fatalf("authenticatedUserID returned error: %v", err)
	}
	th.AssertEquals(t, "u-token", id)
	th.AssertEquals(t, token, gotSubject)
}

func TestAuthenticatedUserID_NotFoundErrors(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/auth/tokens", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	client := identityClient(fakeServer)
	if _, err := authenticatedUserID(context.Background(), client, "expired"); err == nil {
		t.Fatal("expected a 404 on token introspection to produce an error")
	}
}

// --- checkEnableDisable --------------------------------------------------------

func TestCheckEnableDisable(t *testing.T) {
	tests := []struct {
		name            string
		enable, disable bool
		wantErr         bool
	}{
		{"neither set", false, false, false},
		{"enable only", true, false, false},
		{"disable only", false, true, false},
		{"both set is rejected", true, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkEnableDisable(tt.enable, tt.disable)
			if tt.wantErr && err == nil {
				t.Fatal("expected an error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
		})
	}
}

// --- name→ID ambiguity ---------------------------------------------------------

func TestResolveServiceID_AmbiguousErrors(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/services", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"services": [
			{"id": "svc-1", "name": "dup", "type": "compute"},
			{"id": "svc-2", "name": "dup", "type": "volume"}
		]}`))
	})

	client := identityClient(fakeServer)
	_, err := resolveServiceID(context.Background(), client, "dup")
	if err == nil {
		t.Fatal("expected an ambiguity error for two same-named services")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("error should mention ambiguity, got: %v", err)
	}
}

func TestResolveRoleID_AmbiguousErrors(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/roles", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"roles": [
			{"id": "role-1", "name": "dup"},
			{"id": "role-2", "name": "dup"}
		]}`))
	})

	client := identityClient(fakeServer)
	_, err := resolveRoleID(context.Background(), client, "dup", "")
	if err == nil {
		t.Fatal("expected an ambiguity error for two same-named roles")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("error should mention ambiguity, got: %v", err)
	}
}

// --- --domain given vs omitted --------------------------------------------------

// TestRunRoleDelete_WithDomainScopesLookup complements
// TestRunRoleDelete_ResolvesNameThenDeletes (which passes an empty domain): with
// --domain given, the domain must be resolved to an ID first and that ID must be
// forwarded as the role list's domain filter.
func TestRunRoleDelete_WithDomainScopesLookup(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/domains", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"domains": [{"id": "dom-1", "name": "eng"}]}`))
	})
	var gotDomainFilter string
	fakeServer.Mux.HandleFunc("/roles", func(w http.ResponseWriter, r *http.Request) {
		gotDomainFilter = r.URL.Query().Get("domain_id")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"roles": [{"id": "role-id", "name": "reader"}]}`))
	})
	fakeServer.Mux.HandleFunc("/roles/role-id", func(w http.ResponseWriter, r *http.Request) {
		th.AssertEquals(t, http.MethodDelete, r.Method)
		w.WriteHeader(http.StatusNoContent)
	})

	client := identityClient(fakeServer)
	if err := runRoleDelete(context.Background(), client, []string{"reader"}, "eng"); err != nil {
		t.Fatalf("runRoleDelete returned error: %v", err)
	}
	th.AssertEquals(t, "dom-1", gotDomainFilter)
}

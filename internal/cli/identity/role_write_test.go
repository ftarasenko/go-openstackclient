package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

func TestRunRoleCreate_PostsNameAndDescription(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	var body map[string]any
	fakeServer.Mux.HandleFunc("/roles", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"role": {"id": "role-id", "name": "reader", "description": "read-only"}}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	if err := runRoleCreate(context.Background(), identityClient(fakeServer), o, "reader",
		&roleCreateFlags{description: "read-only"}, &out); err != nil {
		t.Fatalf("runRoleCreate returned error: %v", err)
	}

	th.AssertEquals(t, "POST", gotMethod)
	role := body["role"].(map[string]any)
	th.AssertEquals(t, "reader", role["name"])
	th.AssertEquals(t, "read-only", role["description"])
	if !strings.Contains(out.String(), "role-id") {
		t.Errorf("output missing the created role:\n%s", out.String())
	}
}

func TestRunRoleCreate_OrShowFallsBackOnConflict(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/roles", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			// Keystone rejects a duplicate name with 409; --or-show turns that into
			// a show so a provisioning script stays idempotent.
			w.WriteHeader(http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"roles": [{"id": "existing-id", "name": "reader"}]}`))
	})
	fakeServer.Mux.HandleFunc("/roles/existing-id", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"role": {"id": "existing-id", "name": "reader"}}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	if err := runRoleCreate(context.Background(), identityClient(fakeServer), o, "reader",
		&roleCreateFlags{orShow: true}, &out); err != nil {
		t.Fatalf("runRoleCreate --or-show returned error: %v", err)
	}
	if !strings.Contains(out.String(), "existing-id") {
		t.Errorf("--or-show did not fall back to the existing role:\n%s", out.String())
	}
}

func TestRunRoleCreate_ConflictWithoutOrShowFails(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/roles", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	err := runRoleCreate(context.Background(), identityClient(fakeServer), o, "reader",
		&roleCreateFlags{}, &out)
	if err == nil {
		t.Fatal("expected a conflict to fail without --or-show")
	}
}

func TestRunRoleSet_OmittedDescriptionIsNotSent(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/roles", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"roles": [{"id": "role-id", "name": "reader"}]}`))
	})
	var body map[string]any
	var gotMethod string
	fakeServer.Mux.HandleFunc("/roles/role-id", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"role": {"id": "role-id", "name": "auditor"}}`))
	})

	if err := runRoleSet(context.Background(), identityClient(fakeServer), "reader", "auditor", "", "", false); err != nil {
		t.Fatalf("runRoleSet returned error: %v", err)
	}

	th.AssertEquals(t, "PATCH", gotMethod)
	role := body["role"].(map[string]any)
	th.AssertEquals(t, "auditor", role["name"])
	// An omitted --description must not clear the field.
	if _, present := role["description"]; present {
		t.Errorf("description sent although the flag was not set: %#v", role)
	}
}

func TestRunRoleSet_EmptyDescriptionClearsIt(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/roles", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"roles": [{"id": "role-id", "name": "reader"}]}`))
	})
	var body map[string]any
	fakeServer.Mux.HandleFunc("/roles/role-id", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"role": {"id": "role-id", "name": "reader"}}`))
	})

	if err := runRoleSet(context.Background(), identityClient(fakeServer), "reader", "", "", "", true); err != nil {
		t.Fatalf("runRoleSet returned error: %v", err)
	}
	role := body["role"].(map[string]any)
	if got, present := role["description"]; !present || got != "" {
		t.Errorf("an explicit empty --description must clear the field, got %#v", role)
	}
}

func TestRunRoleDelete_ResolvesNameThenDeletes(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/roles", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"roles": [{"id": "role-id", "name": "reader"}]}`))
	})
	var gotMethod, gotPath string
	fakeServer.Mux.HandleFunc("/roles/role-id", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})

	if err := runRoleDelete(context.Background(), identityClient(fakeServer), []string{"reader"}, ""); err != nil {
		t.Fatalf("runRoleDelete returned error: %v", err)
	}
	th.AssertEquals(t, "DELETE", gotMethod)
	th.AssertEquals(t, "/roles/role-id", gotPath)
}

func TestRunImpliedRoleCreate_PutsInferenceRule(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/roles", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Both refs resolve through the same list endpoint, filtered by name.
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
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"role_inference": {
		  "prior_role": {"id": "admin-id", "name": "admin"},
		  "implies":    {"id": "member-id", "name": "member"}
		}}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	err := runImpliedRoleCreate(context.Background(), identityClient(fakeServer), o, "admin", "member", &out)
	if err != nil {
		t.Fatalf("runImpliedRoleCreate returned error: %v", err)
	}

	th.AssertEquals(t, "PUT", gotMethod)
	th.AssertEquals(t, "/roles/admin-id/implies/member-id", gotPath)
	for _, want := range []string{"admin-id", "member-id"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestRunImpliedRoleList_FlattensNestedRules(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/role_inferences", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Keystone nests: one object per prior role, each carrying every role it
		// implies. Upstream renders one row per pair.
		_, _ = w.Write([]byte(`{"role_inferences": [
		  {"prior_role": {"id": "admin-id", "name": "admin"},
		   "implies": [{"id": "member-id", "name": "member"},
		               {"id": "reader-id", "name": "reader"}]}
		]}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	if err := runImpliedRoleList(context.Background(), identityClient(fakeServer), o, &out); err != nil {
		t.Fatalf("runImpliedRoleList returned error: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	th.AssertEquals(t, 2, len(lines))
	if !strings.Contains(lines[0], "member-id") || !strings.Contains(lines[1], "reader-id") {
		t.Errorf("nested rules were not flattened per pair:\n%s", out.String())
	}
}

package identity

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"
	fakeclient "github.com/gophercloud/gophercloud/v2/testhelper/client"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// stubEmptyCollections registers keystone list endpoints that return an empty
// collection, so the name→ID resolvers fall through to treating the reference as
// a literal ID (resolve.go's documented zero-match behavior). Each test then only
// has to mock the endpoint it actually asserts on.
func stubEmptyCollections(fakeServer th.FakeServer, paths map[string]string) {
	for path, key := range paths {
		key := key
		fakeServer.Mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"` + key + `":[]}`))
		})
	}
}

func TestRunGroupShow_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubEmptyCollections(fakeServer, map[string]string{"/groups": "groups"})
	var gotMethod string
	fakeServer.Mux.HandleFunc("/groups/g1", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"group":{"id":"g1","name":"admins","domain_id":"default","description":"admin group"}}`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runGroupShow(context.Background(), identityClient(fakeServer), o, "g1", "", &buf); err != nil {
		t.Fatalf("runGroupShow error: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	for _, want := range []string{"g1", "admins", "default", "admin group"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q\n---\n%s", want, buf.String())
		}
	}
}

func TestRunGroupCreate_RequestBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubEmptyCollections(fakeServer, map[string]string{"/domains": "domains"})
	var gotMethod string
	fakeServer.Mux.HandleFunc("/groups", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		th.TestJSONRequest(t, r, `{"group":{"name":"ops","description":"operators","domain_id":"default"}}`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"group":{"id":"g9","name":"ops","domain_id":"default","description":"operators"}}`))
	})

	o := &output.Options{Format: output.FormatTable}
	f := &groupWriteFlags{description: "operators", domain: "default"}
	var buf bytes.Buffer
	if err := runGroupCreate(context.Background(), identityClient(fakeServer), o, "ops", f, &buf); err != nil {
		t.Fatalf("runGroupCreate error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if !strings.Contains(buf.String(), "g9") {
		t.Errorf("output missing the new group ID\n---\n%s", buf.String())
	}
}

// runGroupSet must distinguish "--description ”" (send an empty description) from
// "--description not given" (omit the key entirely).
func TestRunGroupSet_DescriptionTriState(t *testing.T) {
	tests := []struct {
		name           string
		flags          groupWriteFlags
		descriptionSet bool
		wantBody       string
	}{
		{
			name:     "name only omits description",
			flags:    groupWriteFlags{name: "renamed"},
			wantBody: `{"group":{"name":"renamed"}}`,
		},
		{
			name:           "empty description is sent",
			flags:          groupWriteFlags{description: ""},
			descriptionSet: true,
			wantBody:       `{"group":{"description":""}}`,
		},
		{
			name:           "both",
			flags:          groupWriteFlags{name: "renamed", description: "new text"},
			descriptionSet: true,
			wantBody:       `{"group":{"name":"renamed","description":"new text"}}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()

			stubEmptyCollections(fakeServer, map[string]string{"/groups": "groups"})
			var gotMethod string
			fakeServer.Mux.HandleFunc("/groups/g1", func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				th.TestJSONRequest(t, r, tc.wantBody)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"group":{"id":"g1","name":"renamed","domain_id":"default"}}`))
			})

			o := &output.Options{Format: output.FormatTable}
			var buf bytes.Buffer
			err := runGroupSet(context.Background(), identityClient(fakeServer), o, "g1", &tc.flags, tc.descriptionSet, &buf)
			if err != nil {
				t.Fatalf("runGroupSet error: %v", err)
			}
			if gotMethod != http.MethodPatch {
				t.Errorf("method = %q, want PATCH", gotMethod)
			}
		})
	}
}

func TestGroupSet_RequiresAFlag(t *testing.T) {
	cmd := newGroupSetCommand(nil, &output.Options{Format: output.FormatTable})
	cmd.SetArgs([]string{"g1"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "nothing to set") {
		t.Fatalf("expected a 'nothing to set' error, got %v", err)
	}
}

func TestRunGroupDelete_Request(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubEmptyCollections(fakeServer, map[string]string{"/groups": "groups"})
	var gotMethod string
	fakeServer.Mux.HandleFunc("/groups/g1", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		w.WriteHeader(http.StatusNoContent)
	})

	var buf bytes.Buffer
	if err := runGroupDelete(context.Background(), identityClient(fakeServer), []string{"g1"}, "", &buf); err != nil {
		t.Fatalf("runGroupDelete error: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", gotMethod)
	}
	if !strings.Contains(buf.String(), "Deleted group g1") {
		t.Errorf("unexpected output %q", buf.String())
	}
}

func TestRunGroupMembership_AddAndRemove(t *testing.T) {
	tests := []struct {
		name       string
		verb       membershipVerb
		wantMethod string
		wantOut    string
	}{
		{"add", membershipAdd, http.MethodPut, "Added user u1 to group g1"},
		{"remove", membershipRemove, http.MethodDelete, "Removed user u1 from group g1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()

			stubEmptyCollections(fakeServer, map[string]string{"/groups": "groups", "/users": "users"})
			var gotMethod, gotPath string
			fakeServer.Mux.HandleFunc("/groups/g1/users/u1", func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				gotPath = r.URL.Path
				w.WriteHeader(http.StatusNoContent)
			})

			var buf bytes.Buffer
			err := runGroupMembership(context.Background(), identityClient(fakeServer), tc.verb,
				"g1", []string{"u1"}, &groupMembershipFlags{}, &buf)
			if err != nil {
				t.Fatalf("runGroupMembership error: %v", err)
			}
			if gotMethod != tc.wantMethod {
				t.Errorf("method = %q, want %s", gotMethod, tc.wantMethod)
			}
			if gotPath != "/groups/g1/users/u1" {
				t.Errorf("path = %q, want /groups/g1/users/u1", gotPath)
			}
			if !strings.Contains(buf.String(), tc.wantOut) {
				t.Errorf("output %q, want it to contain %q", buf.String(), tc.wantOut)
			}
		})
	}
}

// group list --user reads the membership endpoint under /users, not a filter on
// /groups, and --domain then narrows the result client-side.
func TestRunGroupList_ByUserUsesMembershipEndpoint(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotPath string
	fakeServer.Mux.HandleFunc("/users/u1/groups", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		th.TestMethod(t, r, http.MethodGet)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"groups":[
			{"id":"g1","name":"admins","domain_id":"default","description":"admin group"},
			{"id":"g2","name":"others","domain_id":"otherdom","description":"other domain"}
		]}`))
	})
	stubEmptyCollections(fakeServer, map[string]string{"/users": "users", "/domains": "domains"})
	// A request to /groups would mean --user was implemented as a filter on the
	// collection endpoint instead of a switch to the membership endpoint.
	fakeServer.Mux.HandleFunc("/groups", func(w http.ResponseWriter, _ *http.Request) {
		t.Error("group list --user must not hit /groups")
		w.WriteHeader(http.StatusInternalServerError)
	})

	o := &output.Options{Format: output.FormatTable}
	client := identityClient(fakeServer)

	var buf bytes.Buffer
	if err := runGroupList(context.Background(), client, o, &groupListFlags{user: "u1"}, &buf); err != nil {
		t.Fatalf("runGroupList error: %v", err)
	}
	if gotPath != "/users/u1/groups" {
		t.Errorf("path = %q, want /users/u1/groups", gotPath)
	}
	for _, want := range []string{"admins", "others"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q\n---\n%s", want, buf.String())
		}
	}

	// --domain filters the membership list client-side.
	var narrowed bytes.Buffer
	f := &groupListFlags{user: "u1", domain: "otherdom"}
	if err := runGroupList(context.Background(), client, o, f, &narrowed); err != nil {
		t.Fatalf("runGroupList --domain error: %v", err)
	}
	if !strings.Contains(narrowed.String(), "others") {
		t.Errorf("--domain output missing the matching group\n---\n%s", narrowed.String())
	}
	if strings.Contains(narrowed.String(), "admins") {
		t.Errorf("--domain should have filtered out the other-domain group\n---\n%s", narrowed.String())
	}
}

func TestRunGroupContains_MemberAndNonMember(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubEmptyCollections(fakeServer, map[string]string{"/groups": "groups", "/users": "users"})
	member := true
	fakeServer.Mux.HandleFunc("/groups/g1/users/u1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodHead)
		if member {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})

	client := identityClient(fakeServer)

	var buf bytes.Buffer
	if err := runGroupContains(context.Background(), client, "g1", "u1", &groupMembershipFlags{}, &buf); err != nil {
		t.Fatalf("runGroupContains error: %v", err)
	}
	if !strings.Contains(buf.String(), "User u1 is in group g1") {
		t.Errorf("unexpected output %q", buf.String())
	}

	member = false
	var missing bytes.Buffer
	err := runGroupContains(context.Background(), client, "g1", "u1", &groupMembershipFlags{}, &missing)
	if err == nil || !strings.Contains(err.Error(), "is not in group") {
		t.Fatalf("expected a non-membership error, got %v", err)
	}
}

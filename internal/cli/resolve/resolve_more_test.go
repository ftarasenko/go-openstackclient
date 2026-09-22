package resolve

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	th "github.com/gophercloud/gophercloud/v2/testhelper"
)

// TestResolvers_ListByName covers the resolvers that share byName: each must
// send the reference as the ?name= filter of its own service and return the one
// match's ID. The filter is asserted rather than assumed because a resolver that
// forgot it would still pass a one-result fixture while listing the whole cloud
// against a real one.
func TestResolvers_ListByName(t *testing.T) {
	tests := []struct {
		kind   string
		path   string
		body   string
		client func(th.FakeServer) *gophercloud.ServiceClient
		call   func(context.Context, *gophercloud.ServiceClient, string) (string, error)
		ref    string
		wantID string
	}{
		{
			kind: "domain", path: "/domains", client: projectFakeClient, call: DomainID,
			body: `{"domains":[{"id":"dom-1","name":"prod"}]}`, ref: "prod", wantID: "dom-1",
		},
		{
			kind: "subnet", path: "/v2.0/subnets", client: netFakeClient, call: SubnetID,
			body: `{"subnets":[{"id":"sub-1","name":"internal"}]}`, ref: "internal", wantID: "sub-1",
		},
		{
			kind: "port", path: "/v2.0/ports", client: netFakeClient, call: PortID,
			body: `{"ports":[{"id":"port-1","name":"mgmt"}]}`, ref: "mgmt", wantID: "port-1",
		},
		{
			kind: "user", path: "/users", client: projectFakeClient, call: UserID,
			body: `{"users":[{"id":"user-1","name":"alice"}]}`, ref: "alice", wantID: "user-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()

			var gotName string
			fakeServer.Mux.HandleFunc(tt.path, func(w http.ResponseWriter, r *http.Request) {
				gotName = r.URL.Query().Get("name")
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.body))
			})

			id, err := tt.call(context.Background(), tt.client(fakeServer), tt.ref)
			if err != nil {
				t.Fatal(err)
			}
			if gotName != tt.ref {
				t.Errorf("name filter = %q, want %q", gotName, tt.ref)
			}
			if id != tt.wantID {
				t.Errorf("resolved id = %q, want %q", id, tt.wantID)
			}
		})
	}
}

// TestResolvers_MatchPolicy pins the policy the package doc states, on a
// resolver other than the image one the existing tests use: zero matches pass
// the reference through as an opaque ID, and two are ambiguous rather than
// arbitrary.
func TestResolvers_MatchPolicy(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantID  string
		wantErr string
	}{
		{name: "no match falls back to the reference", body: `{"users":[]}`, wantID: "ghost"},
		{
			name:    "two matches are ambiguous",
			body:    `{"users":[{"id":"a","name":"ghost"},{"id":"b","name":"ghost"}]}`,
			wantErr: `multiple users named "ghost"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()

			fakeServer.Mux.HandleFunc("/users", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.body))
			})

			id, err := UserID(context.Background(), projectFakeClient(fakeServer), "ghost")
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want one containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if id != tt.wantID {
				t.Errorf("resolved id = %q, want %q", id, tt.wantID)
			}
		})
	}
}

// TestUserIDInDomain_NarrowsTheListing is the wiring assertion that matters for
// the --user/--user-domain pair: the domain reference is resolved first and its
// ID must reach the user listing as domain_id. Without it two users of the same
// name in different domains would resolve to whichever keystone returned first.
func TestUserIDInDomain_NarrowsTheListing(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotDomainName, gotUserName, gotDomainID string
	fakeServer.Mux.HandleFunc("/domains", func(w http.ResponseWriter, r *http.Request) {
		gotDomainName = r.URL.Query().Get("name")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"domains":[{"id":"dom-9","name":"staff"}]}`))
	})
	fakeServer.Mux.HandleFunc("/users", func(w http.ResponseWriter, r *http.Request) {
		gotUserName, gotDomainID = r.URL.Query().Get("name"), r.URL.Query().Get("domain_id")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"users":[{"id":"user-42","name":"alice","domain_id":"dom-9"}]}`))
	})

	id, err := UserIDInDomain(context.Background(), projectFakeClient(fakeServer), "alice", "staff")
	if err != nil {
		t.Fatal(err)
	}
	if gotDomainName != "staff" {
		t.Errorf("domain filter = %q, want staff", gotDomainName)
	}
	if gotUserName != "alice" || gotDomainID != "dom-9" {
		t.Errorf("user listing filtered by name=%q domain_id=%q, want alice / dom-9", gotUserName, gotDomainID)
	}
	if id != "user-42" {
		t.Errorf("resolved id = %q, want user-42", id)
	}
}

// TestProjectIDInDomain_NarrowsTheListing is the same wiring assertion for
// --project/--project-domain.
func TestProjectIDInDomain_NarrowsTheListing(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotProjectName, gotDomainID string
	fakeServer.Mux.HandleFunc("/domains", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"domains":[{"id":"dom-3","name":"ops"}]}`))
	})
	fakeServer.Mux.HandleFunc("/projects", func(w http.ResponseWriter, r *http.Request) {
		gotProjectName, gotDomainID = r.URL.Query().Get("name"), r.URL.Query().Get("domain_id")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"projects":[{"id":"proj-3","name":"payments"}]}`))
	})

	id, err := ProjectIDInDomain(context.Background(), projectFakeClient(fakeServer), "payments", "ops")
	if err != nil {
		t.Fatal(err)
	}
	if gotProjectName != "payments" || gotDomainID != "dom-3" {
		t.Errorf("project listing filtered by name=%q domain_id=%q, want payments / dom-3", gotProjectName, gotDomainID)
	}
	if id != "proj-3" {
		t.Errorf("resolved id = %q, want proj-3", id)
	}
}

// An empty domain reference must behave exactly like the undomained resolver,
// and a UUID must still short-circuit before either listing — the in-domain
// wrappers are the one place where the passthrough could be lost to the extra
// domain lookup.
func TestInDomainResolvers_ShortCircuits(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	domainsCalled := false
	fakeServer.Mux.HandleFunc("/domains", func(w http.ResponseWriter, _ *http.Request) {
		domainsCalled = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"domains":[{"id":"dom-1","name":"d"}]}`))
	})
	fakeServer.Mux.HandleFunc("/users", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("domain_id"); got != "" {
			t.Errorf("an empty --user-domain must not send domain_id, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"users":[{"id":"user-7","name":"bob"}]}`))
	})

	client := projectFakeClient(fakeServer)
	id, err := UserIDInDomain(context.Background(), client, "bob", "")
	if err != nil {
		t.Fatal(err)
	}
	if id != "user-7" {
		t.Errorf("resolved id = %q, want user-7", id)
	}
	if domainsCalled {
		t.Error("an empty domain reference must not trigger a domain lookup")
	}

	const uuid = "5f0f658d5bc34e1fb3fb0862a79489a4"
	got, err := ProjectIDInDomain(context.Background(), client, uuid, "ops")
	if err != nil {
		t.Fatal(err)
	}
	if got != uuid {
		t.Errorf("a UUID must pass through unchanged, got %q", got)
	}
	if domainsCalled {
		t.Error("a UUID reference must not trigger a domain lookup")
	}
}

// A failed listing has to name what was being looked up: the resolvers run
// inside another service's command, so a bare transport error gives no clue
// which reference the operator has to fix.
func TestResolvers_ErrorNamesTheLookup(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/v2.0/subnets", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	_, err := SubnetID(context.Background(), netFakeClient(fakeServer), "internal")
	if err == nil || !strings.Contains(err.Error(), `looking up subnet "internal"`) {
		t.Errorf("err = %v, want one naming the subnet being looked up", err)
	}
}

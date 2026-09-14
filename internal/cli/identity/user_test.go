package identity

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

func TestRunUserList_RequestAndTableOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotPath string
	fakeServer.Mux.HandleFunc("/users", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"users":[
			{"id":"u1","name":"admin","domain_id":"default","enabled":true},
			{"id":"u2","name":"bob","domain_id":"example","enabled":false}
		]}`))
	})

	client := identityClient(fakeServer)
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	// Empty domain avoids the resolve lookup.
	if err := runUserList(context.Background(), client, o, "", &buf); err != nil {
		t.Fatalf("runUserList error: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if gotPath != "/users" {
		t.Errorf("path = %q, want /users", gotPath)
	}
	for _, want := range []string{"u1", "admin", "u2", "bob", "example"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q\n---\n%s", want, buf.String())
		}
	}
}

func TestRunUserShow_ResolvesNameAndGets(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var listName, getPath string
	fakeServer.Mux.HandleFunc("/users", func(w http.ResponseWriter, r *http.Request) {
		listName = r.URL.Query().Get("name")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"users":[{"id":"u1","name":"admin"}]}`))
	})
	fakeServer.Mux.HandleFunc("/users/u1", func(w http.ResponseWriter, r *http.Request) {
		getPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"user":{"id":"u1","name":"admin","domain_id":"default","enabled":true,"description":"the admin"}}`))
	})

	client := identityClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	if err := runUserShow(context.Background(), client, o, "admin", "", &buf); err != nil {
		t.Fatalf("runUserShow error: %v", err)
	}
	if listName != "admin" {
		t.Errorf("resolve name = %q, want admin", listName)
	}
	if getPath != "/users/u1" {
		t.Errorf("get path = %q, want /users/u1", getPath)
	}
	for _, want := range []string{"u1", "admin", "the admin"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q\n---\n%s", want, buf.String())
		}
	}
}

func TestRunUserCreate_ResolvesDomainAndBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var domListName, gotMethod string
	fakeServer.Mux.HandleFunc("/domains", func(w http.ResponseWriter, r *http.Request) {
		domListName = r.URL.Query().Get("name")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"domains":[{"id":"dd","name":"default"}]}`))
	})
	fakeServer.Mux.HandleFunc("/users", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		th.TestJSONRequest(t, r, `{"user":{"name":"bob","domain_id":"dd","password":"s3cr3t"}}`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"user":{"id":"u-new","name":"bob","domain_id":"dd","enabled":true}}`))
	})

	client := identityClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}
	// --project left empty so resolveProjectID short-circuits without a lookup.
	f := &userWriteFlags{domain: "default", password: "s3cr3t"}
	var buf bytes.Buffer
	if err := runUserCreate(context.Background(), client, o, "bob", f, &buf); err != nil {
		t.Fatalf("runUserCreate error: %v", err)
	}
	if domListName != "default" {
		t.Errorf("domain resolve name = %q, want default", domListName)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if !strings.Contains(buf.String(), "u-new") {
		t.Errorf("output missing u-new\n---\n%s", buf.String())
	}
}

func TestRunUserDelete_ResolvesNameThenDeletes(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var delMethod, delPath string
	fakeServer.Mux.HandleFunc("/users", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"users":[{"id":"u1","name":"bob"}]}`))
	})
	fakeServer.Mux.HandleFunc("/users/u1", func(w http.ResponseWriter, r *http.Request) {
		delMethod = r.Method
		delPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})

	client := identityClient(fakeServer)
	if err := runUserDelete(context.Background(), client, "bob", ""); err != nil {
		t.Fatalf("runUserDelete error: %v", err)
	}
	if delMethod != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", delMethod)
	}
	if delPath != "/users/u1" {
		t.Errorf("path = %q, want /users/u1", delPath)
	}
}

func TestRunUserSet_ResolvesNameAndPatches(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var patchMethod string
	fakeServer.Mux.HandleFunc("/users", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"users":[{"id":"u1","name":"bob"}]}`))
	})
	fakeServer.Mux.HandleFunc("/users/u1", func(w http.ResponseWriter, r *http.Request) {
		patchMethod = r.Method
		th.TestJSONRequest(t, r, `{"user":{"name":"bobby","description":"updated"}}`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"user":{"id":"u1","name":"bobby","enabled":true,"description":"updated"}}`))
	})

	client := identityClient(fakeServer)
	f := &userWriteFlags{name: "bobby", description: "updated"}
	if err := runUserSet(context.Background(), client, "bob", f, true); err != nil {
		t.Fatalf("runUserSet error: %v", err)
	}
	if patchMethod != http.MethodPatch {
		t.Errorf("method = %q, want PATCH", patchMethod)
	}
}

// `openstack user set --project <project> <user>` is upstream's way of moving a
// user's default project; koc carried the flag on `create` only until this
// test's subject was wired up.
func TestRunUserSet_ProjectSetsDefaultProjectID(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var projQuery url.Values
	fakeServer.Mux.HandleFunc("/users", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"users":[{"id":"u1","name":"admin"}]}`))
	})
	fakeServer.Mux.HandleFunc("/projects", func(w http.ResponseWriter, r *http.Request) {
		projQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"projects":[{"id":"p1","name":"admin"}]}`))
	})
	fakeServer.Mux.HandleFunc("/users/u1", func(w http.ResponseWriter, r *http.Request) {
		th.TestJSONRequest(t, r, `{"user":{"default_project_id":"p1"}}`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"user":{"id":"u1","name":"admin","default_project_id":"p1"}}`))
	})

	client := identityClient(fakeServer)
	f := &userWriteFlags{project: "admin"}
	if err := runUserSet(context.Background(), client, "admin", f, false); err != nil {
		t.Fatalf("runUserSet error: %v", err)
	}
	if got := projQuery.Get("name"); got != "admin" {
		t.Errorf("project resolve name = %q, want admin", got)
	}
	if got := projQuery.Get("domain_id"); got != "" {
		t.Errorf("project resolve domain_id = %q, want empty", got)
	}
}

// The default project may live outside the user's domain, so --project-domain
// qualifies the lookup; without it koc falls back to the user's own domain
// (where upstream leaves the lookup unscoped) — see docs/coverage.md.
func TestRunUserSet_ProjectDomainQualifiesLookup(t *testing.T) {
	for _, tc := range []struct {
		name          string
		projectDomain string
		wantDomainID  string
	}{
		{name: "falls back to the user domain", wantDomainID: "d-user"},
		{name: "--project-domain wins", projectDomain: "other", wantDomainID: "d-other"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()

			var projDomainID string
			fakeServer.Mux.HandleFunc("/domains", func(w http.ResponseWriter, r *http.Request) {
				id := "d-user"
				if r.URL.Query().Get("name") == "other" {
					id = "d-other"
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"domains":[{"id":"` + id + `","name":"x"}]}`))
			})
			fakeServer.Mux.HandleFunc("/users", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"users":[{"id":"u1","name":"admin"}]}`))
			})
			fakeServer.Mux.HandleFunc("/projects", func(w http.ResponseWriter, r *http.Request) {
				projDomainID = r.URL.Query().Get("domain_id")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"projects":[{"id":"p1","name":"admin"}]}`))
			})
			fakeServer.Mux.HandleFunc("/users/u1", func(w http.ResponseWriter, r *http.Request) {
				th.TestJSONRequest(t, r, `{"user":{"default_project_id":"p1"}}`)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"user":{"id":"u1","name":"admin","default_project_id":"p1"}}`))
			})

			client := identityClient(fakeServer)
			f := &userWriteFlags{domain: "default", project: "admin", projectDomain: tc.projectDomain}
			if err := runUserSet(context.Background(), client, "admin", f, false); err != nil {
				t.Fatalf("runUserSet error: %v", err)
			}
			if projDomainID != tc.wantDomainID {
				t.Errorf("project resolve domain_id = %q, want %q", projDomainID, tc.wantDomainID)
			}
		})
	}
}

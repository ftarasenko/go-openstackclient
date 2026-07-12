package identity

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"
	fakeclient "github.com/gophercloud/gophercloud/v2/testhelper/client"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

const projectListBody = `{
  "projects": [
    {"id": "p1", "name": "demo", "domain_id": "itkey", "enabled": true, "description": "demo project"},
    {"id": "p2", "name": "admin", "domain_id": "default", "enabled": true, "description": ""}
  ]
}`

func TestRunProjectList_RequestAndTableOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotPath string
	fakeServer.Mux.HandleFunc("/projects", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(projectListBody))
	})

	client := identityClient(fakeServer)
	o := &output.Options{Format: output.FormatTable}

	var buf bytes.Buffer
	// Empty domain avoids the resolve lookup so the list request stands alone.
	if err := runProjectList(context.Background(), client, o, "", &buf); err != nil {
		t.Fatalf("runProjectList returned error: %v", err)
	}

	if gotMethod != http.MethodGet {
		t.Errorf("request method = %q, want GET", gotMethod)
	}
	if gotPath != "/projects" {
		t.Errorf("request path = %q, want /projects", gotPath)
	}

	out := buf.String()
	for _, want := range []string{"ID", "Name", "Domain ID", "demo", "admin", "p1", "p2", "itkey"} {
		if !strings.Contains(out, want) {
			t.Errorf("table output missing %q\n---\n%s", want, out)
		}
	}
}

func TestRunProjectCreate_RequestBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotBody string
	fakeServer.Mux.HandleFunc("/projects", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"project": {"id": "new-id", "name": "demo", "domain_id": "dom-itkey", "enabled": true}}`))
	})

	client := identityClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}

	// Pass a domain ID directly and empty properties so the create body maps 1:1
	// to the flags without extra resolve lookups (domainID passed as-is).
	f := &projectWriteFlags{domain: "dom-itkey", description: "demo project", properties: []string{"foo=bar"}}
	// domain "dom-itkey" would normally be resolved; register a stub that returns
	// no match so it falls through to the literal ID.
	fakeServer.Mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"domains": []}`))
	})

	var buf bytes.Buffer
	if err := runProjectCreate(context.Background(), client, o, "demo", f, &buf); err != nil {
		t.Fatalf("runProjectCreate returned error: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("request method = %q, want POST", gotMethod)
	}
	for _, want := range []string{`"name":"demo"`, `"domain_id":"dom-itkey"`, `"description":"demo project"`, `"foo":"bar"`} {
		if !strings.Contains(gotBody, want) {
			t.Errorf("create body missing %q\n---\n%s", want, gotBody)
		}
	}
}

func TestRunProjectShow_ResolvesNameAndGets(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var listName, getPath string
	fakeServer.Mux.HandleFunc("/projects", func(w http.ResponseWriter, r *http.Request) {
		listName = r.URL.Query().Get("name")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"projects":[{"id":"p1","name":"demo"}]}`))
	})
	fakeServer.Mux.HandleFunc("/projects/p1", func(w http.ResponseWriter, r *http.Request) {
		getPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"project":{"id":"p1","name":"demo","domain_id":"default","enabled":true,"description":"demo project","parent_id":"pp"}}`))
	})

	client := identityClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	if err := runProjectShow(context.Background(), client, o, "demo", "", &buf); err != nil {
		t.Fatalf("runProjectShow error: %v", err)
	}
	if listName != "demo" {
		t.Errorf("resolve name = %q, want demo", listName)
	}
	if getPath != "/projects/p1" {
		t.Errorf("get path = %q, want /projects/p1", getPath)
	}
	for _, want := range []string{"p1", "demo", "demo project"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q\n---\n%s", want, buf.String())
		}
	}
}

func TestRunProjectDelete_ResolvesNameThenDeletes(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var delMethod, delPath string
	fakeServer.Mux.HandleFunc("/projects", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"projects":[{"id":"p1","name":"demo"}]}`))
	})
	fakeServer.Mux.HandleFunc("/projects/p1", func(w http.ResponseWriter, r *http.Request) {
		delMethod = r.Method
		delPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})

	client := identityClient(fakeServer)
	if err := runProjectDelete(context.Background(), client, "demo", ""); err != nil {
		t.Fatalf("runProjectDelete error: %v", err)
	}
	if delMethod != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", delMethod)
	}
	if delPath != "/projects/p1" {
		t.Errorf("path = %q, want /projects/p1", delPath)
	}
}

func TestRunProjectSet_ResolvesNameAndPatches(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var patchMethod string
	fakeServer.Mux.HandleFunc("/projects", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"projects":[{"id":"p1","name":"demo"}]}`))
	})
	fakeServer.Mux.HandleFunc("/projects/p1", func(w http.ResponseWriter, r *http.Request) {
		patchMethod = r.Method
		th.TestJSONRequest(t, r, `{"project":{"name":"renamed","description":"updated"}}`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"project":{"id":"p1","name":"renamed","enabled":true,"description":"updated"}}`))
	})

	client := identityClient(fakeServer)
	f := &projectWriteFlags{name: "renamed", description: "updated"}
	if err := runProjectSet(context.Background(), client, "demo", f, true); err != nil {
		t.Fatalf("runProjectSet error: %v", err)
	}
	if patchMethod != http.MethodPatch {
		t.Errorf("method = %q, want PATCH", patchMethod)
	}
}

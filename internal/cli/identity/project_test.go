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

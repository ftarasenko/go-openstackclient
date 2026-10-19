package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path"
	"strings"
	"sync"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	th "github.com/gophercloud/gophercloud/v2/testhelper"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// The tests in this file execute commands through cobra — Execute() → RunE →
// newIdentityClient → runXxx — rather than calling a seam directly, so the
// glue the seam tests skip by construction runs, and a flag bound to the wrong
// field fails here. See internal/cli/network/rune_exec_test.go.

const execID = "0123456789abcdef0123456789abcdef"

type execCall struct {
	Method, Path string
	Body         map[string]any
}

// execKeystone answers a GET of a collection with one match under its key, so
// each name resolves to execID; a write or a single GET gets one resource under
// every singular key, a POST answers 201, and DELETE and PUT answer 204.
type execKeystone struct {
	mu    sync.Mutex
	calls []execCall
}

func (f *execKeystone) handle(w http.ResponseWriter, r *http.Request) {
	call := execCall{Method: r.Method, Path: r.URL.EscapedPath()}
	if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
		_ = json.Unmarshal(raw, &call.Body)
	}
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.mu.Unlock()
	if r.Method == http.MethodDelete || r.Method == http.MethodPut {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	one := map[string]any{"id": execID, "name": "x", "domain_id": execID, "enabled": true}
	if r.Method == http.MethodGet && !strings.HasSuffix(call.Path, execID) {
		// AllPages keeps one list key per page, so answer under the collection's own.
		key := path.Base(call.Path)
		assignment := map[string]any{
			"role":  map[string]any{"id": execID, "name": "member", "domain": map[string]any{"name": "Default"}},
			"user":  map[string]any{"id": execID, "name": "bob", "domain": map[string]any{"name": "Default"}},
			"scope": map[string]any{"project": map[string]any{"id": execID, "name": "demo"}, "OS-INHERIT:inherited_to": "projects"},
		}
		item := any(one)
		if key == "role_assignments" {
			item = assignment
		}
		_ = json.NewEncoder(w).Encode(map[string]any{key: []any{item}, "links": map[string]any{}})
		return
	}
	if r.Method == http.MethodPost {
		w.WriteHeader(http.StatusCreated)
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"role": one, "user": one})
}

// find returns the one request matching "METHOD /suffix".
func (f *execKeystone) find(t *testing.T, method, suffix string) execCall {
	t.Helper()
	for _, c := range f.calls {
		if c.Method == method && strings.HasSuffix(c.Path, suffix) {
			return c
		}
	}
	t.Fatalf("no %s %s request (calls: %v)", method, suffix, f.calls)
	return execCall{}
}

// execIdentity builds the real identity tree against a fake keystone and runs
// argv. It returns the command's output, the fake and the error Execute produced.
func execIdentity(t *testing.T, argv ...string) (string, *execKeystone, error) {
	t.Helper()
	fakeServer := th.SetupHTTP()
	t.Cleanup(fakeServer.Teardown)
	f := &execKeystone{}
	fakeServer.Mux.HandleFunc("/", f.handle)
	provider := &gophercloud.ProviderClient{
		TokenID:      "fake-token",
		IdentityBase: fakeServer.Server.URL + "/",
		EndpointLocator: func(gophercloud.EndpointOpts) (string, error) {
			return fakeServer.Server.URL + "/", nil
		},
	}
	a := &auth.Options{}
	a.SetAuthenticatorForTest(func(context.Context) (*auth.Client, error) {
		return a.NewClientForTest(provider, gophercloud.EndpointOpts{}), nil
	})
	root := &cobra.Command{Use: "koc"}
	root.AddCommand(NewCommand(a, &output.Options{Format: "json"})...)
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetIn(strings.NewReader(""))
	root.SetArgs(argv)
	root.SilenceUsage, root.SilenceErrors = true, true
	err := root.ExecuteContext(context.Background())
	return buf.String(), f, err
}

func TestRoleRunE_Exec(t *testing.T) {
	for _, tc := range []struct {
		argv         []string
		method, path string
		want         string
	}{
		{[]string{"role", "list", "--domain", "Default"}, http.MethodGet, "/roles", `"Name": "x"`},
		{[]string{"role", "show", "member"}, http.MethodGet, "/roles/" + execID, `"Name": "x"`},
		{
			[]string{"role", "add", "member", "--user", "bob", "--user-domain", "Default", "--project", "demo", "--project-domain", "Default", "--role-domain", "Default"},
			http.MethodPut, "/projects/" + execID + "/users/" + execID + "/roles/" + execID, "",
		},
		{
			[]string{"role", "remove", "member", "--group", "admins", "--group-domain", "Default", "--domain", "Default"},
			http.MethodDelete, "/domains/" + execID + "/groups/" + execID + "/roles/" + execID, "",
		},
		{
			[]string{"role", "assignment", "list", "--user", "bob", "--user-domain", "Default", "--group-domain", "Default", "--role", "member", "--role-domain", "Default", "--project", "demo", "--names"},
			http.MethodGet, "/role_assignments", `"Role": "member@Default"`,
		},
	} {
		t.Run(strings.Join(tc.argv[:2], " "), func(t *testing.T) {
			out, f, err := execIdentity(t, tc.argv...)
			if err != nil {
				t.Fatalf("%v: %v\n%s", tc.argv, err, out)
			}
			f.find(t, tc.method, tc.path)
			if !strings.Contains(out, tc.want) {
				t.Errorf("output lacks %q:\n%s", tc.want, out)
			}
		})
	}
}

func TestRoleRunE_ExecRejectsBadGrant(t *testing.T) {
	for _, argv := range [][]string{
		{"role", "add", "member", "--project", "demo"},
		{"role", "add", "member", "--user", "bob", "--project", "demo", "--system", "all"},
		{"role", "remove", "member", "--user", "bob", "--system", "nope"},
		{"role", "assignment", "list", "--project", "demo", "--domain", "Default"},
	} {
		if _, f, err := execIdentity(t, argv...); err == nil {
			t.Errorf("%v: want an error", argv)
		} else if len(f.calls) != 0 {
			t.Errorf("%v: sent %v before refusing", argv, f.calls)
		}
	}
}

// --password-prompt's answer is the password the request carries.
func TestUserRunE_ExecPasswordPrompt(t *testing.T) {
	saved := promptUserPassword
	t.Cleanup(func() { promptUserPassword = saved })
	promptUserPassword = func(io.Writer) (string, error) { return "s3cret", nil }

	for _, tc := range []struct {
		argv         []string
		method, path string
	}{
		{[]string{"user", "create", "bob", "--password-prompt", "--domain", "Default", "--project", "demo", "--project-domain", "Default"}, http.MethodPost, "/users"},
		{[]string{"user", "set", "bob", "--password-prompt", "--domain", "Default", "--disable"}, http.MethodPatch, "/users/" + execID},
	} {
		t.Run(tc.argv[1], func(t *testing.T) {
			out, f, err := execIdentity(t, tc.argv...)
			if err != nil {
				t.Fatalf("%v: %v\n%s", tc.argv, err, out)
			}
			user, _ := f.find(t, tc.method, tc.path).Body["user"].(map[string]any)
			if user["password"] != "s3cret" {
				t.Errorf("request user = %v, want the prompted password", user)
			}
		})
	}
}

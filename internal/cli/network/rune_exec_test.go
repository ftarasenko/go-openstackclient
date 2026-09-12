package network

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	th "github.com/gophercloud/gophercloud/v2/testhelper"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// The tests in this file execute commands through cobra — Execute() → RunE →
// newNetworkClient → runXxx — rather than calling a runXxx seam directly. That
// covers the glue the seam tests skip by construction, and it is the only layer
// that proves a flag registered by newXxxCommand actually reaches the seam that
// reads it. A wiring typo (flag bound to the wrong field, a verb wired to the
// wrong seam) is invisible to a seam test and fails here.

// execNetwork builds the real `network` command tree, points its auth at
// fakeServer, and runs argv against it. It returns the command's stdout and the
// error Execute produced.
func execNetwork(t *testing.T, fakeServer th.FakeServer, argv ...string) (string, error) {
	t.Helper()

	a := &auth.Options{}
	provider := &gophercloud.ProviderClient{
		TokenID: "fake-token",
		EndpointLocator: func(gophercloud.EndpointOpts) (string, error) {
			return fakeServer.Server.URL + "/", nil
		},
	}
	a.SetAuthenticatorForTest(func(context.Context) (*auth.Client, error) {
		return a.NewClientForTest(provider, gophercloud.EndpointOpts{}), nil
	})

	root := &cobra.Command{Use: "koc"}
	root.AddCommand(NewCommand(a, &output.Options{Format: "table"})...)

	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs(argv)
	root.SilenceUsage = true
	root.SilenceErrors = true
	err := root.ExecuteContext(context.Background())
	return buf.String(), err
}

func TestExec_NetworkList_RendersThroughRunE(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/v2.0/networks", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, "GET")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"networks":[
			{"id":"11111111-1111-1111-1111-111111111111","name":"public","status":"ACTIVE","subnets":[]}
		]}`))
	})

	out, err := execNetwork(t, fakeServer, "network", "list")
	if err != nil {
		t.Fatalf("network list: %v (output %q)", err, out)
	}
	if !strings.Contains(out, "public") || !strings.Contains(out, "11111111-1111-1111-1111-111111111111") {
		t.Fatalf("network list output missing the row:\n%s", out)
	}
}

// A flag registered on the command must reach the seam that turns it into a
// query parameter. Exercised end-to-end this catches a mis-bound flag; the
// seam test cannot, because it is handed the parsed value.
func TestExec_NetworkList_FlagReachesTheQuery(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotQuery string
	fakeServer.Mux.HandleFunc("/v2.0/networks", func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"networks":[]}`))
	})

	if _, err := execNetwork(t, fakeServer, "network", "list", "--name", "public"); err != nil {
		t.Fatalf("network list --name: %v", err)
	}
	if !strings.Contains(gotQuery, "name=public") {
		t.Fatalf("--name did not reach the request; query was %q", gotQuery)
	}
}

// An API failure must surface as a non-zero exit, not a rendered empty table.
func TestExec_NetworkList_APIErrorIsAnError(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/v2.0/networks", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})

	if _, err := execNetwork(t, fakeServer, "network", "list"); err == nil {
		t.Fatal("a 403 from the API exited 0; want an error")
	}
}

// o.Validate() is the first statement of every RunE, so a bad --format must
// fail before any request is made.
func TestExec_InvalidFormatFailsBeforeRequest(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	called := false
	fakeServer.Mux.HandleFunc("/v2.0/networks", func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"networks":[]}`))
	})

	a := &auth.Options{}
	root := &cobra.Command{Use: "koc"}
	root.AddCommand(NewCommand(a, &output.Options{Format: "bogus"})...)
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"network", "list"})
	root.SilenceUsage, root.SilenceErrors = true, true

	err := root.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("an invalid --format exited 0; want an error")
	}
	if called {
		t.Fatal("an invalid --format still issued the API request")
	}
}

// `port list --all-projects` is koc-native — upstream OSC has no such flag
// because neutron needs none — so nothing upstream pins its wiring. This does:
// the flag must parse, reach the seam, and turn into the Project ID column
// without adding a filter neutron's filter validation would reject.
func TestExec_PortList_AllProjects(t *testing.T) {
	t.Setenv("ALL_PROJECTS", "")
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotQuery string
	fakeServer.Mux.HandleFunc("/v2.0/ports", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, "GET")
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ports":[
			{"id":"22222222-2222-2222-2222-222222222222","name":"p1","status":"ACTIVE",
			 "project_id":"33333333-3333-3333-3333-333333333333"}
		]}`))
	})

	out, err := execNetwork(t, fakeServer, "port", "list", "--all-projects")
	if err != nil {
		t.Fatalf("port list --all-projects: %v (output %q)", err, out)
	}
	if gotQuery != "" {
		t.Errorf("--all-projects put %q on the wire; neutron takes no such filter", gotQuery)
	}
	for _, want := range []string{"Project ID", "33333333-3333-3333-3333-333333333333"} {
		if !strings.Contains(out, want) {
			t.Errorf("port list --all-projects output missing %q:\n%s", want, out)
		}
	}
}

// --project narrows to one project, which is the opposite of what
// --all-projects asks for; cobra must reject the pair rather than silently
// letting one win.
func TestExec_PortList_AllProjectsExcludesProject(t *testing.T) {
	t.Setenv("ALL_PROJECTS", "")
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/v2.0/ports", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ports":[]}`))
	})

	_, err := execNetwork(t, fakeServer, "port", "list", "--all-projects", "--project", "p1")
	if err == nil {
		t.Error("--project with --all-projects was accepted; want a mutual-exclusion error")
	}
}

// The ALL_PROJECTS envvar defaults the flag, the same as on the compute and
// block-storage verbs upstream reads it for.
func TestPortList_AllProjectsDefaultsFromEnvironment(t *testing.T) {
	t.Setenv("ALL_PROJECTS", "1")
	root := &cobra.Command{Use: "koc"}
	root.AddCommand(NewCommand(&auth.Options{}, &output.Options{})...)
	leaf, _, err := root.Find([]string{"port", "list"})
	if err != nil || leaf == nil {
		t.Fatalf("port list: not found: %v", err)
	}
	flag := leaf.Flags().Lookup("all-projects")
	if flag == nil {
		t.Fatal("koc port list: missing --all-projects")
	}
	if flag.DefValue != "true" {
		t.Errorf("--all-projects default = %q, want true from ALL_PROJECTS", flag.DefValue)
	}
}

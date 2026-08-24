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

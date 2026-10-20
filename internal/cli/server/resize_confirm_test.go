package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	th "github.com/gophercloud/gophercloud/v2/testhelper"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Upstream's confirm/revert spellings — under "server resize", "server
// migration" and the deprecated "server migrate" — all post nova's
// confirmResize/revertResize action, run here through cobra.
func TestResizeActionSpellings(t *testing.T) {
	const id = "66666666-6666-4666-8666-666666666666"
	cases := []struct {
		build      func(*auth.Options, *output.Options) *cobra.Command
		verb       string
		action     string
		deprecated bool
	}{
		{newServerResizeCommand, "confirm", "confirmResize", false},
		{newServerResizeCommand, "revert", "revertResize", false},
		{newServerMigrationCommand, "confirm", "confirmResize", false},
		{newServerMigrationCommand, "revert", "revertResize", false},
		{newServerMigrateCommand, "confirm", "confirmResize", true},
		{newServerMigrateCommand, "revert", "revertResize", true},
	}
	for _, tc := range cases {
		fakeServer := th.SetupHTTP()
		var body map[string]any
		fakeServer.Mux.HandleFunc("/servers/"+id+"/action", func(w http.ResponseWriter, r *http.Request) {
			th.TestMethod(t, r, http.MethodPost)
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &body)
			// nova answers confirmResize with 204 and revertResize with 202.
			if _, ok := body["revertResize"]; ok {
				w.WriteHeader(http.StatusAccepted)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		})
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
		parent := &cobra.Command{Use: "server"}
		cmd := tc.build(a, &output.Options{Format: output.FormatTable})
		parent.AddCommand(cmd)
		root.AddCommand(parent)
		var stdout, stderr bytes.Buffer
		root.SetOut(&stdout)
		root.SetErr(&stderr)
		root.SetArgs([]string{"server", cmd.Name(), tc.verb, id})
		root.SilenceUsage, root.SilenceErrors = true, true
		if err := root.ExecuteContext(context.Background()); err != nil {
			t.Fatalf("server %s %s: %v", cmd.Name(), tc.verb, err)
		}
		if _, ok := body[tc.action]; !ok || len(body) != 1 {
			t.Errorf("server %s %s posted %v, want {%q: null}", cmd.Name(), tc.verb, body, tc.action)
		}
		warned := strings.Contains(stderr.String(), "deprecated in favour of the 'server migration "+tc.verb+"' command")
		if warned != tc.deprecated {
			t.Errorf("server %s %s: stderr %q, want deprecation warning = %v", cmd.Name(), tc.verb, stderr.String(), tc.deprecated)
		}
		fakeServer.Teardown()
	}
}

// The parent verbs keep their own positional: "server resize <server>" is
// still a resize, not a lookup of a subcommand named after the server.
func TestResizeStillTakesAServer(t *testing.T) {
	cmd := newServerResizeCommand(&auth.Options{}, &output.Options{})
	found, args, err := cmd.Find([]string{"web-1", "--flavor", "m1.small"})
	if err != nil || found != cmd || len(args) == 0 || args[0] != "web-1" {
		t.Errorf("Find = %v %v %v, want the resize verb itself", found.Name(), args, err)
	}
}

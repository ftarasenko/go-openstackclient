package baremetal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
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
// newBaremetalClient → runXxx — rather than calling a seam directly, so the
// glue the seam tests skip by construction runs, and a flag bound to the wrong
// field fails here. See internal/cli/network/rune_exec_test.go.

const (
	execUUID         = "eeeeeeee-0000-0000-0000-00000000000e"
	execDeploySteps  = `[{"interface":"deploy","step":"erase_devices","args":{},"priority":10}]`
	execRunbookSteps = `[{"interface":"deploy","step":"erase_devices","args":{},"order":1}]`
)

// execIronic answers every request with a body each verb can decode: a GET of
// a collection gets every collection key as an empty list, anything else one
// generic resource, and DELETE and PUT answer 204.
type execIronic struct {
	mu    sync.Mutex
	calls []fakeCall
}

func (f *execIronic) handle(w http.ResponseWriter, r *http.Request) {
	call := fakeCall{Method: r.Method, Path: r.URL.EscapedPath(), Query: r.URL.RawQuery}
	if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
		_ = json.Unmarshal(raw, &call.Body)
	}
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.mu.Unlock()
	w.Header().Set(ironicMaxVersionHeader, "1.112")
	if r.Method == http.MethodDelete || r.Method == http.MethodPut {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodGet && !strings.HasSuffix(call.Path, execUUID) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"deploy_templates": []any{}, "inspection_rules": []any{}, "portgroups": []any{}, "ports": []any{},
			"connectors": []any{}, "targets": []any{}, "runbooks": []any{}, "traits": []any{}, "chassis": []any{},
		})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"uuid": execUUID, "name": "x", "steps": []any{}, "actions": []any{}, "extra": map[string]any{},
	})
}

// body returns the JSON body of the one request matching "METHOD /suffix".
func (f *execIronic) body(t *testing.T, method, suffix string) any {
	t.Helper()
	for _, c := range f.calls {
		if c.Method == method && strings.HasSuffix(c.Path, suffix) {
			return c.Body
		}
	}
	t.Fatalf("no %s %s request (calls: %v)", method, suffix, f.calls)
	return nil
}

// execBaremetal builds the real `baremetal` tree with auth from authn and runs
// argv. It returns the command's output and the error Execute produced.
func execBaremetal(t *testing.T, authn func(*auth.Options) (*auth.Client, error), argv ...string) (string, error) {
	t.Helper()
	a := &auth.Options{BaremetalAPIVersion: "latest"}
	a.SetAuthenticatorForTest(func(context.Context) (*auth.Client, error) { return authn(a) })
	root := &cobra.Command{Use: "koc"}
	root.AddCommand(NewCommand(a, &output.Options{Format: "json"}))
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetIn(strings.NewReader(""))
	root.SetArgs(append([]string{"baremetal"}, argv...))
	root.SilenceUsage, root.SilenceErrors = true, true
	err := root.ExecuteContext(context.Background())
	return buf.String(), err
}

func fakeIronicAuth(t *testing.T) (func(*auth.Options) (*auth.Client, error), *execIronic) {
	t.Helper()
	fakeServer := th.SetupHTTP()
	t.Cleanup(fakeServer.Teardown)
	f := &execIronic{}
	fakeServer.Mux.HandleFunc("/", f.handle)
	provider := &gophercloud.ProviderClient{
		TokenID: "fake-token",
		EndpointLocator: func(gophercloud.EndpointOpts) (string, error) {
			return fakeServer.Server.URL + "/", nil
		},
	}
	return func(a *auth.Options) (*auth.Client, error) {
		return a.NewClientForTest(provider, gophercloud.EndpointOpts{}), nil
	}, f
}

// execLeaves runs every verb of the nouns whose RunE the seam tests never
// reach, each with the flags its required path needs.
var execLeaves = [][]string{
	{"deploy", "template", "list"},
	{"deploy", "template", "show", execUUID},
	{"deploy", "template", "create", "CUSTOM_X", "--steps", execDeploySteps, "--extra", "k=v"},
	{"deploy", "template", "set", execUUID, "--name", "CUSTOM_Y"},
	{"deploy", "template", "unset", execUUID, "--extra", "k"},
	{"deploy", "template", "delete", execUUID},
	{"inspection", "rule", "list"},
	{"inspection", "rule", "show", execUUID},
	{"inspection", "rule", "create", "--actions", "[]", "--uuid", execUUID, "--sensitive", "true", "--priority", "5"},
	{"inspection", "rule", "set", execUUID, "--description", "d", "--priority", "1"},
	{"inspection", "rule", "unset", execUUID, "--description"},
	{"inspection", "rule", "delete", execUUID},
	{"port", "group", "list"},
	{"port", "group", "show", execUUID},
	{"port", "group", "create", "--node", execUUID, "--physical-network", "physnet1", "--name", "pg"},
	{"port", "group", "set", execUUID, "--physical-network", "physnet2"},
	{"port", "group", "unset", execUUID, "--physical-network"},
	{"port", "group", "delete", execUUID},
	{"volume", "connector", "list"},
	{"volume", "connector", "show", execUUID},
	{"volume", "connector", "create", "--node", execUUID, "--type", "iqn", "--connector-id", "iqn.2010-10.org.openstack:x"},
	{"volume", "connector", "set", execUUID, "--type", "ip"},
	{"volume", "connector", "unset", execUUID, "--extra", "k"},
	{"volume", "connector", "delete", execUUID},
	{"volume", "target", "list"},
	{"volume", "target", "show", execUUID},
	{"volume", "target", "create", "--node", execUUID, "--type", "iscsi", "--boot-index", "0", "--volume-id", "v1"},
	{"volume", "target", "set", execUUID, "--boot-index", "1"},
	{"volume", "target", "unset", execUUID, "--extra", "k"},
	{"volume", "target", "delete", execUUID},
	{"runbook", "list"},
	{"runbook", "show", execUUID},
	{"runbook", "create", "--name", "CUSTOM_R", "--steps", execRunbookSteps, "--disable-ramdisk"},
	{"runbook", "set", execUUID, "--disable-ramdisk"},
	{"runbook", "unset", execUUID, "--disable-ramdisk"},
	{"runbook", "delete", execUUID},
	{"runbook", "trait", "list", execUUID},
	{"runbook", "trait", "add", execUUID, "CUSTOM_T"},
	{"runbook", "trait", "remove", execUUID, "--all"},
	{"chassis", "list"},
	{"chassis", "show", execUUID},
	{"chassis", "create", "--description", "d", "--extra", "k=v"},
	{"chassis", "set", execUUID, "--description", "e"},
	{"chassis", "unset", execUUID, "--description"},
	{"chassis", "delete", execUUID},
	{"port", "unset", execUUID, "--name"},
	{"node", "boot", "mode", "set", execUUID, "uefi"},
	{"driver", "passthru", "call", execUUID, "m", "--arg", "k=v"},
}

func TestExec_BaremetalLeaves(t *testing.T) {
	for _, argv := range execLeaves {
		t.Run(strings.Join(argv[:len(argv)-1], " "), func(t *testing.T) {
			authn, f := fakeIronicAuth(t)
			if out, err := execBaremetal(t, authn, argv...); err != nil {
				t.Fatalf("%v: %v\n%s", argv, err, out)
			}
			if len(f.calls) == 0 {
				t.Fatalf("%v made no request", argv)
			}
		})
	}
}

// Every RunE returns the client helper's error unchanged.
func TestExec_BaremetalLeavesAuthError(t *testing.T) {
	errAuth := errors.New("no credentials")
	failing := func(*auth.Options) (*auth.Client, error) { return nil, errAuth }
	for _, argv := range execLeaves {
		if _, err := execBaremetal(t, failing, argv...); !errors.Is(err, errAuth) {
			t.Errorf("%v: err = %v, want the authentication error", argv, err)
		}
	}
}

// Flags that only create takes, or that this package names through a shared
// constant, must reach the request body.
func TestExec_BaremetalFlagsReachTheBody(t *testing.T) {
	for _, tc := range []struct {
		argv         []string
		method, path string
		want         string
	}{
		{[]string{"inspection", "rule", "create", "--actions", "[]", "--uuid", execUUID, "--sensitive", "true"},
			"POST", "/inspection_rules", `{"actions":[],"sensitive":true,"uuid":"` + execUUID + `"}`},
		{[]string{"port", "group", "create", "--node", execUUID, "--physical-network", "physnet1"},
			"POST", "/portgroups", `{"node_uuid":"` + execUUID + `","physical_network":"physnet1"}`},
		{[]string{"volume", "target", "create", "--node", execUUID, "--type", "iscsi", "--boot-index", "0", "--volume-id", "v1"},
			"POST", "/volume/targets", `{"boot_index":0,"node_uuid":"` + execUUID + `","volume_id":"v1","volume_type":"iscsi"}`},
		{[]string{"runbook", "create", "--name", "CUSTOM_R", "--steps", execRunbookSteps, "--disable-ramdisk"},
			"POST", "/runbooks", `{"disable_ramdisk":true,"name":"CUSTOM_R","steps":` + execRunbookSteps + `}`},
	} {
		authn, f := fakeIronicAuth(t)
		if out, err := execBaremetal(t, authn, tc.argv...); err != nil {
			t.Fatalf("%v: %v\n%s", tc.argv, err, out)
		}
		jsonEq(t, strings.Join(tc.argv[:3], " "), f.body(t, tc.method, tc.path), tc.want)
	}
}

// baremetal create reads its files before authenticating, so it is run here
// rather than from execLeaves, whose auth-error test expects auth first.
func TestExec_BaremetalCreate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes.json")
	if err := os.WriteFile(path, []byte(`{"nodes": [{"driver": "fake-hardware", "ports": [{"address": "52:54:00:00:00:01"}]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	authn, f := fakeIronicAuth(t)
	out, err := execBaremetal(t, authn, "create", path)
	if err != nil {
		t.Fatalf("baremetal create: %v\n%s", err, out)
	}
	jsonEq(t, "port body", f.body(t, http.MethodPost, "/ports"), `{"address":"52:54:00:00:00:01","node_uuid":"`+execUUID+`"}`)
	if !strings.Contains(out, "Created node "+execUUID) {
		t.Errorf("output = %q", out)
	}
}

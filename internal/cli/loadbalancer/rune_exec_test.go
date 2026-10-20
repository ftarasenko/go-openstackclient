package loadbalancer

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	th "github.com/gophercloud/gophercloud/v2/testhelper"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// The tests in this file execute the unset, listener stats and availability
// zone verbs through cobra — Execute() → RunE → newLoadBalancerClient → seam —
// so a flag bound to the wrong field or a path built from the wrong argument
// fails here, which the seam tests skip by construction. See
// internal/cli/baremetal/rune_exec_test.go.

const execID = "eeeeeeee-0000-4000-8000-00000000000e"

type execCall struct {
	Method, Path string
	Body         any
}

// execOctavia answers a collection GET with every collection key empty, and
// any other request with one generic record under every envelope key.
type execOctavia struct {
	mu    sync.Mutex
	calls []execCall
}

func (f *execOctavia) handle(w http.ResponseWriter, r *http.Request) {
	call := execCall{Method: r.Method, Path: r.URL.EscapedPath()}
	if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
		_ = json.Unmarshal(raw, &call.Body)
	}
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.mu.Unlock()
	if r.Method == http.MethodDelete {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	last := call.Path[strings.LastIndex(call.Path, "/")+1:]
	if r.Method == http.MethodGet && (last == "availabilityzones" || last == "availabilityzoneprofiles" || last == "listeners") {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"availability_zones": []any{}, "availability_zone_profiles": []any{}, "listeners": []any{},
		})
		return
	}
	rec := map[string]any{"id": execID, "name": "x", "provisioning_status": "ACTIVE", "tags": []any{"a", "b"}}
	out := map[string]any{"stats": map[string]any{"bytes_in": 1}}
	for _, env := range []string{"loadbalancer", "listener", "pool", "member", "healthmonitor", "l7policy", "rule",
		"flavor", "availability_zone", "availability_zone_profile"} {
		out[env] = rec
	}
	_ = json.NewEncoder(w).Encode(out)
}

func (f *execOctavia) find(method, path string) *execCall {
	for i := range f.calls {
		if f.calls[i].Method == method && strings.HasSuffix(f.calls[i].Path, path) {
			return &f.calls[i]
		}
	}
	return nil
}

func execLoadBalancer(t *testing.T, argv ...string) (*execOctavia, string, error) {
	t.Helper()
	fakeServer := th.SetupHTTP()
	t.Cleanup(fakeServer.Teardown)
	f := &execOctavia{}
	fakeServer.Mux.HandleFunc("/", f.handle)
	provider := &gophercloud.ProviderClient{
		TokenID: "fake-token",
		EndpointLocator: func(gophercloud.EndpointOpts) (string, error) {
			return fakeServer.Server.URL + "/", nil
		},
	}
	a := &auth.Options{}
	a.SetAuthenticatorForTest(func(context.Context) (*auth.Client, error) {
		return a.NewClientForTest(provider, gophercloud.EndpointOpts{}), nil
	})
	root := &cobra.Command{Use: "koc"}
	root.AddCommand(NewCommand(a, &output.Options{Format: "json"}))
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs(append([]string{"loadbalancer"}, argv...))
	root.SilenceUsage, root.SilenceErrors = true, true
	err := root.ExecuteContext(context.Background())
	return f, buf.String(), err
}

func TestExec_LoadBalancerNewVerbs(t *testing.T) {
	cases := []struct {
		argv         []string
		method, path string
		body         string // JSON the request must carry; empty for none
	}{
		{[]string{"unset", execID, "--description", "--vip-sg-id", "--all-tag"}, http.MethodPut,
			"/v2.0/lbaas/loadbalancers/" + execID, `{"loadbalancer":{"description":null,"vip_sg_ids":null,"tags":[]}}`},
		{[]string{"listener", "unset", execID, "--default-pool", "--hsts-max-age", "--tag", "a"}, http.MethodPut,
			"/v2.0/lbaas/listeners/" + execID, `{"listener":{"default_pool_id":null,"hsts_max_age":null,"tags":["b"]}}`},
		{[]string{"pool", "unset", execID, "--tls-container-ref"}, http.MethodPut,
			"/v2.0/lbaas/pools/" + execID, `{"pool":{"tls_container_ref":null}}`},
		{[]string{"member", "unset", execID, execID, "--monitor-address"}, http.MethodPut,
			"/v2.0/lbaas/pools/" + execID + "/members/" + execID, `{"member":{"monitor_address":null}}`},
		{[]string{"healthmonitor", "unset", execID, "--http-method"}, http.MethodPut,
			"/v2.0/lbaas/healthmonitors/" + execID, `{"healthmonitor":{"http_method":null}}`},
		{[]string{"l7policy", "unset", execID, "--redirect-http-code"}, http.MethodPut,
			"/v2.0/lbaas/l7policies/" + execID, `{"l7policy":{"redirect_http_code":null}}`},
		{[]string{"l7rule", "unset", execID, "r1", "--key", "--wait"}, http.MethodPut,
			"/v2.0/lbaas/l7policies/" + execID + "/rules/r1", `{"rule":{"key":null}}`},
		{[]string{"flavor", "unset", execID, "--description"}, http.MethodPut,
			"/v2.0/lbaas/flavors/" + execID, `{"flavor":{"description":null}}`},
		{[]string{"listener", "stats", "show", execID}, http.MethodGet, "/v2.0/lbaas/listeners/" + execID + "/stats", ""},
		{[]string{"availabilityzoneprofile", "create", "p", "--provider", "amphora", "--availability-zone-data", "{}"}, http.MethodPost,
			"/v2.0/lbaas/availabilityzoneprofiles", `{"availability_zone_profile":{"name":"p","provider_name":"amphora","availability_zone_data":"{}"}}`},
		{[]string{"availabilityzoneprofile", "list", "--provider", "amphora"}, http.MethodGet, "/v2.0/lbaas/availabilityzoneprofiles", ""},
		{[]string{"availabilityzoneprofile", "show", execID}, http.MethodGet, "/v2.0/lbaas/availabilityzoneprofiles/" + execID, ""},
		{[]string{"availabilityzoneprofile", "set", execID, "--provider", "ovn"}, http.MethodPut,
			"/v2.0/lbaas/availabilityzoneprofiles/" + execID, `{"availability_zone_profile":{"provider_name":"ovn"}}`},
		{[]string{"availabilityzoneprofile", "delete", execID}, http.MethodDelete, "/v2.0/lbaas/availabilityzoneprofiles/" + execID, ""},
		{[]string{"availabilityzone", "create", "--name", "az1", "--availabilityzoneprofile", execID}, http.MethodPost,
			"/v2.0/lbaas/availabilityzones", `{"availability_zone":{"name":"az1","availability_zone_profile_id":"` + execID + `"}}`},
		{[]string{"availabilityzone", "list", "--enable"}, http.MethodGet, "/v2.0/lbaas/availabilityzones", ""},
		{[]string{"availabilityzone", "show", "az1"}, http.MethodGet, "/v2.0/lbaas/availabilityzones/az1", ""},
		{[]string{"availabilityzone", "set", "az1", "--disable"}, http.MethodPut,
			"/v2.0/lbaas/availabilityzones/az1", `{"availability_zone":{"enabled":false}}`},
		{[]string{"availabilityzone", "unset", "az1", "--description"}, http.MethodPut,
			"/v2.0/lbaas/availabilityzones/az1", `{"availability_zone":{"description":null}}`},
		{[]string{"availabilityzone", "delete", "az1"}, http.MethodDelete, "/v2.0/lbaas/availabilityzones/az1", ""},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.argv[:2], " "), func(t *testing.T) {
			f, out, err := execLoadBalancer(t, tc.argv...)
			if err != nil {
				t.Fatalf("koc loadbalancer %s: %v\n%s", strings.Join(tc.argv, " "), err, out)
			}
			call := f.find(tc.method, tc.path)
			if call == nil {
				t.Fatalf("no %s %s; calls: %+v", tc.method, tc.path, f.calls)
			}
			if tc.body == "" {
				return
			}
			var want any
			if err := json.Unmarshal([]byte(tc.body), &want); err != nil {
				t.Fatal(err)
			}
			got, _ := json.Marshal(call.Body)
			wantJSON, _ := json.Marshal(want)
			if string(got) != string(wantJSON) {
				t.Errorf("body = %s, want %s", got, wantJSON)
			}
		})
	}
}

// A unset with no attribute flag fails before any request.
func TestExec_UnsetWithoutFlagsMakesNoRequest(t *testing.T) {
	f, _, err := execLoadBalancer(t, "pool", "unset", execID)
	if err == nil || !strings.Contains(err.Error(), "nothing to unset") {
		t.Fatalf("err = %v", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("requests made: %+v", f.calls)
	}
}

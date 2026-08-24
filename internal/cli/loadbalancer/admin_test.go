package loadbalancer

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

func TestRunL7PolicyCreate_RequestBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubEmptyList(fakeServer, "/v2.0/lbaas/listeners", "listeners")
	stubEmptyList(fakeServer, "/v2.0/lbaas/pools", "pools")
	var gotMethod string
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/l7policies", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"l7policies": []}`))
			return
		}
		gotMethod = r.Method
		th.TestJSONRequest(t, r, `{"l7policy": {
          "name": "to-api",
          "listener_id": "li1",
          "action": "REDIRECT_TO_POOL",
          "position": 1,
          "redirect_pool_id": "po1"
        }}`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"l7policy": {
          "id": "l7p1", "name": "to-api", "action": "REDIRECT_TO_POOL", "position": 1,
          "listener_id": "li1", "redirect_pool_id": "po1", "provisioning_status": "PENDING_CREATE"
        }}`))
	})

	f := &l7PolicyWriteFlags{
		listener:     "li1",
		action:       "REDIRECT_TO_POOL",
		position:     1,
		redirectPool: "po1",
	}
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runL7PolicyCreate(context.Background(), lbClient(fakeServer), o, "to-api", f, "", &buf); err != nil {
		t.Fatalf("runL7PolicyCreate error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if !strings.Contains(buf.String(), "l7p1") {
		t.Errorf("output missing the new policy ID\n---\n%s", buf.String())
	}
}

// Each l7 action needs its own redirect target; octavia rejects a mismatch with a
// generic 400, so the command names the missing flag first.
func TestCheckL7Action(t *testing.T) {
	tests := []struct {
		name    string
		flags   l7PolicyWriteFlags
		wantErr string
	}{
		{name: "pool", flags: l7PolicyWriteFlags{action: "REDIRECT_TO_POOL", redirectPool: "po1"}},
		{name: "url", flags: l7PolicyWriteFlags{action: "REDIRECT_TO_URL", redirectURL: "https://x.invalid/"}},
		{name: "prefix", flags: l7PolicyWriteFlags{action: "REDIRECT_PREFIX", redirectPrefix: "https://x.invalid"}},
		{name: "reject", flags: l7PolicyWriteFlags{action: "REJECT"}},
		{
			name:    "pool without target",
			flags:   l7PolicyWriteFlags{action: "REDIRECT_TO_POOL"},
			wantErr: "requires --redirect-pool",
		},
		{
			name:    "url without target",
			flags:   l7PolicyWriteFlags{action: "REDIRECT_TO_URL"},
			wantErr: "requires --redirect-url",
		},
		{
			name:    "reject with a target",
			flags:   l7PolicyWriteFlags{action: "REJECT", redirectURL: "https://x.invalid/"},
			wantErr: "takes no redirect target",
		},
		{
			name:    "unknown action",
			flags:   l7PolicyWriteFlags{action: "REDIRECT_TO_MARS"},
			wantErr: "unsupported --action",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			flags := tc.flags
			err := checkL7Action(&flags)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("checkL7Action() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("checkL7Action() = %v, want one containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestRunL7RuleCreate_ScopedToThePolicy(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubEmptyList(fakeServer, "/v2.0/lbaas/l7policies", "l7policies")
	var gotPath string
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/l7policies/l7p1/rules", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		th.TestJSONRequest(t, r, `{"rule": {
          "type": "PATH", "compare_type": "STARTS_WITH", "value": "/api"
        }}`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"rule": {
          "id": "l7r1", "type": "PATH", "compare_type": "STARTS_WITH", "value": "/api"
        }}`))
	})

	f := &l7RuleWriteFlags{typ: "PATH", compareType: "STARTS_WITH", value: "/api"}
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runL7RuleCreate(context.Background(), lbClient(fakeServer), o, "l7p1", f, "", &buf); err != nil {
		t.Fatalf("runL7RuleCreate error: %v", err)
	}
	if gotPath != "/v2.0/lbaas/l7policies/l7p1/rules" {
		t.Errorf("path = %q, want the policy-scoped rule collection", gotPath)
	}
}

// HEADER and COOKIE rules compare a named field, so --key is required.
func TestL7RuleCreate_HeaderAndCookieRequireAKey(t *testing.T) {
	for _, typ := range []string{"HEADER", "COOKIE"} {
		cmd := newL7RuleCreateCommand(nil, &output.Options{Format: output.FormatTable})
		cmd.SetArgs([]string{"l7p1", "--type=" + typ, "--compare-type=EQUAL_TO", "--value=x"})
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		err := cmd.Execute()
		if err == nil || !strings.Contains(err.Error(), "requires --key") {
			t.Errorf("--type %s: err = %v, want a missing-key error", typ, err)
		}
	}
}

// -1 means unlimited in octavia, so a negative quota is legitimate and only the
// flags actually given may be sent.
func TestRunLBQuotaSet_SendsOnlyGivenQuotasIncludingUnlimited(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/v2.0/quotas/p1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"quota": {"listener": -1, "loadbalancer": 5}}`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"quota": {"listener": -1, "pool": 10, "member": 50, "l7policy": 5, "l7rule": 20}}`))
	})

	f := &lbQuotaSetFlags{loadBalancer: 5, listener: -1, pool: 999}
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runLBQuotaSet(context.Background(), lbClient(fakeServer), o, "p1", f,
		changedSet{"loadbalancer": true, "listener": true}, &buf)
	if err != nil {
		t.Fatalf("runLBQuotaSet error: %v", err)
	}
}

func TestRunLBQuotaSet_RejectsEmptyUpdate(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runLBQuotaSet(context.Background(), lbClient(fakeServer), o, "p1", &lbQuotaSetFlags{}, changedSet{}, &buf)
	if err == nil || !strings.Contains(err.Error(), "nothing to set") {
		t.Fatalf("expected a 'nothing to set' error, got %v", err)
	}
}

// The defaults endpoint has no typed gophercloud call, so the URL is what matters
// — and it must share the prefix gophercloud's typed quota calls use, which is
// /v2.0/quotas rather than /v2.0/lbaas/quotas.
func TestRunLBQuotaDefaultsShow_RawEndpoint(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotPath string
	fakeServer.Mux.HandleFunc("/v2.0/quotas/defaults", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		th.TestMethod(t, r, http.MethodGet)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"quota": {"loadbalancer": 10, "listener": -1, "pool": 10, "member": 50, "healthmonitor": -1, "l7policy": 5, "l7rule": 20}}`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runLBQuotaDefaultsShow(context.Background(), lbClient(fakeServer), o, &buf); err != nil {
		t.Fatalf("runLBQuotaDefaultsShow error: %v", err)
	}
	if gotPath != "/v2.0/quotas/defaults" {
		t.Errorf("path = %q, want /v2.0/quotas/defaults", gotPath)
	}
	for _, want := range []string{"listener", "-1", "l7rule", "20"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q\n---\n%s", want, buf.String())
		}
	}
}

func TestRunAmphoraList_FiltersAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubLBList(fakeServer)
	fakeServer.Mux.HandleFunc("/v2.0/octavia/amphorae", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		th.TestFormValues(t, r, map[string]string{"role": "MASTER", "status": "ALLOCATED"})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"amphorae": [{
          "id": "am1", "loadbalancer_id": "lb1", "compute_id": "srv1", "role": "MASTER",
          "status": "ALLOCATED", "lb_network_ip": "10.1.0.5", "ha_ip": "10.0.0.10",
          "image_id": "img1", "cached_zone": "az1", "vrrp_ip": "10.1.0.6"
        }]}`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	f := &amphoraListFlags{role: "MASTER", status: "ALLOCATED", long: true}
	if err := runAmphoraList(context.Background(), lbClient(fakeServer), o, f, &buf); err != nil {
		t.Fatalf("runAmphoraList error: %v", err)
	}
	for _, want := range []string{"am1", "lb1", "MASTER", "ALLOCATED", "10.1.0.5", "srv1", "az1"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q\n---\n%s", want, buf.String())
		}
	}
}

// The amphora admin endpoints sit under /octavia/, not /lbaas/ — the one detail
// most likely to be got wrong in a raw fallback.
func TestAmphoraRawFallbacks_UseTheOctaviaPathSegment(t *testing.T) {
	t.Run("configure", func(t *testing.T) {
		fakeServer := th.SetupHTTP()
		defer fakeServer.Teardown()

		var gotMethod, gotPath string
		fakeServer.Mux.HandleFunc("/v2.0/octavia/amphorae/am1/config", func(w http.ResponseWriter, r *http.Request) {
			gotMethod, gotPath = r.Method, r.URL.Path
			w.WriteHeader(http.StatusAccepted)
		})

		var buf bytes.Buffer
		if err := runAmphoraConfigure(context.Background(), lbClient(fakeServer), "am1", &buf); err != nil {
			t.Fatalf("runAmphoraConfigure error: %v", err)
		}
		if gotMethod != http.MethodPut || gotPath != "/v2.0/octavia/amphorae/am1/config" {
			t.Errorf("got %s %s, want PUT /v2.0/octavia/amphorae/am1/config", gotMethod, gotPath)
		}
	})

	t.Run("delete", func(t *testing.T) {
		fakeServer := th.SetupHTTP()
		defer fakeServer.Teardown()

		var gotMethod, gotPath string
		fakeServer.Mux.HandleFunc("/v2.0/octavia/amphorae/am1", func(w http.ResponseWriter, r *http.Request) {
			gotMethod, gotPath = r.Method, r.URL.Path
			w.WriteHeader(http.StatusNoContent)
		})

		var buf bytes.Buffer
		if err := runAmphoraDelete(context.Background(), lbClient(fakeServer), []string{"am1"}, &buf); err != nil {
			t.Fatalf("runAmphoraDelete error: %v", err)
		}
		if gotMethod != http.MethodDelete || gotPath != "/v2.0/octavia/amphorae/am1" {
			t.Errorf("got %s %s, want DELETE /v2.0/octavia/amphorae/am1", gotMethod, gotPath)
		}
	})

	// Unlike the load balancer's own stats, this endpoint returns one entry per
	// listener, so it renders as a list.
	t.Run("stats show", func(t *testing.T) {
		fakeServer := th.SetupHTTP()
		defer fakeServer.Teardown()

		fakeServer.Mux.HandleFunc("/v2.0/octavia/amphorae/am1/stats", func(w http.ResponseWriter, r *http.Request) {
			th.TestMethod(t, r, http.MethodGet)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"amphora_stats": [
              {"listener_id": "li1", "loadbalancer_id": "lb1", "active_connections": 2,
               "bytes_in": 100, "bytes_out": 200, "request_errors": 0, "total_connections": 9},
              {"listener_id": "li2", "loadbalancer_id": "lb1", "active_connections": 0,
               "bytes_in": 0, "bytes_out": 0, "request_errors": 3, "total_connections": 1}
            ]}`))
		})

		o := &output.Options{Format: output.FormatTable}
		var buf bytes.Buffer
		if err := runAmphoraStatsShow(context.Background(), lbClient(fakeServer), o, "am1", &buf); err != nil {
			t.Fatalf("runAmphoraStatsShow error: %v", err)
		}
		for _, want := range []string{"Listener ID", "li1", "li2", "100", "200"} {
			if !strings.Contains(buf.String(), want) {
				t.Errorf("output missing %q\n---\n%s", want, buf.String())
			}
		}
	})
}

func TestRunProviderList_AndCapabilities(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/v2.0/lbaas/providers", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"providers": [
          {"name": "amphora", "description": "The Octavia Amphora driver."},
          {"name": "ovn", "description": "Provider driver for OVN."}
        ]}`))
	})
	var gotCapPath string
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/providers/amphora/capabilities", func(w http.ResponseWriter, r *http.Request) {
		gotCapPath = r.URL.Path
		th.TestMethod(t, r, http.MethodGet)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"flavor_capabilities": [
          {"name": "loadbalancer_topology", "description": "The load balancer topology."}
        ]}`))
	})

	o := &output.Options{Format: output.FormatTable}
	var list bytes.Buffer
	if err := runProviderList(context.Background(), lbClient(fakeServer), o, &list); err != nil {
		t.Fatalf("runProviderList error: %v", err)
	}
	for _, want := range []string{"amphora", "ovn", "Amphora driver"} {
		if !strings.Contains(list.String(), want) {
			t.Errorf("provider list output missing %q\n---\n%s", want, list.String())
		}
	}

	var caps bytes.Buffer
	if err := runProviderCapabilityList(context.Background(), lbClient(fakeServer), o, "amphora", &caps); err != nil {
		t.Fatalf("runProviderCapabilityList error: %v", err)
	}
	if gotCapPath != "/v2.0/lbaas/providers/amphora/capabilities" {
		t.Errorf("capability path = %q", gotCapPath)
	}
	if !strings.Contains(caps.String(), "loadbalancer_topology") {
		t.Errorf("capability output missing the capability\n---\n%s", caps.String())
	}
}

// flavors.UpdateOpts tags Enabled omitempty, so a false would be dropped and
// --disable would silently do nothing. It goes through an explicit raw PUT.
func TestRunFlavorSet_DisableSendsEnabledFalse(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubEmptyList(fakeServer, "/v2.0/lbaas/flavors", "flavors")
	var bodies []string
	fakeServer.Mux.HandleFunc("/v2.0/lbaas/flavors/fl1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"flavor": {"id": "fl1", "name": "standard", "enabled": false, "flavor_profile_id": "fp1"}}`))
			return
		}
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		bodies = append(bodies, string(buf))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"flavor": {"id": "fl1", "name": "standard", "enabled": false, "flavor_profile_id": "fp1"}}`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runFlavorSet(context.Background(), lbClient(fakeServer), o, "fl1",
		&octaviaFlavorSetFlags{changed: changedSet{"disable": true}}, &buf)
	if err != nil {
		t.Fatalf("runFlavorSet error: %v", err)
	}
	if len(bodies) != 1 || !strings.Contains(bodies[0], `"enabled":false`) {
		t.Errorf("expected one PUT carrying enabled:false, got %q", bodies)
	}
	if !strings.Contains(buf.String(), "false") {
		t.Errorf("output should report the flavor as disabled\n---\n%s", buf.String())
	}
}

func TestRunFlavorSet_RejectsEmptyUpdate(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runFlavorSet(context.Background(), lbClient(fakeServer), o, "fl1",
		&octaviaFlavorSetFlags{changed: changedSet{}}, &buf)
	if err == nil || !strings.Contains(err.Error(), "nothing to set") {
		t.Fatalf("expected a 'nothing to set' error, got %v", err)
	}
}

// flavorprofiles.ListOpts has no name filter, so the name→ID match is client-side.
func TestResolveFlavorProfileID_MatchesClientSide(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/v2.0/lbaas/flavorprofiles", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		if r.URL.Query().Has("name") {
			t.Errorf("flavorprofiles has no name filter; it must not be sent, got %q", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"flavorprofiles": [
          {"id": "fp1", "name": "amphora-single", "provider_name": "amphora"},
          {"id": "fp2", "name": "amphora-ha", "provider_name": "amphora"}
        ]}`))
	})

	got, err := resolveFlavorProfileID(context.Background(), lbClient(fakeServer), "amphora-ha")
	if err != nil {
		t.Fatalf("resolveFlavorProfileID error: %v", err)
	}
	if got != "fp2" {
		t.Errorf("resolveFlavorProfileID() = %q, want fp2", got)
	}

	// An unmatched name falls back to being treated as an ID.
	got, err = resolveFlavorProfileID(context.Background(), lbClient(fakeServer), "nonesuch")
	if err != nil {
		t.Fatalf("resolveFlavorProfileID error: %v", err)
	}
	if got != "nonesuch" {
		t.Errorf("resolveFlavorProfileID() = %q, want the literal reference back", got)
	}
}

// TestRunLBQuotaUnset_ClearsOnlyNamedQuotas is the regression for koc's old
// `unset`, which DELETEd the whole quota set and so silently reverted quotas the
// operator never named. Upstream octaviaclient's UnsetQuota PUTs an explicit
// null per named key; only those keys may appear in the body.
func TestRunLBQuotaUnset_ClearsOnlyNamedQuotas(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	fakeServer.Mux.HandleFunc("/v2.0/quotas/p1", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		th.TestMethod(t, r, "PUT")
		th.TestJSONRequest(t, r, `{"quota": {"listener": null, "pool": null}}`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"quota": {"loadbalancer": 5, "listener": -1, "pool": -1, "member": 50, "healthmonitor": -1, "l7policy": 5, "l7rule": 20}}`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runLBQuotaUnset(context.Background(), lbClient(fakeServer), o, "p1",
		[]string{"listener", "pool"}, &buf); err != nil {
		t.Fatalf("runLBQuotaUnset error: %v", err)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT (DELETE would clear every quota)", gotMethod)
	}
	// The refreshed quota set is rendered, so the untouched loadbalancer=5 shows.
	for _, want := range []string{"loadbalancer", "5", "listener"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q\n---\n%s", want, buf.String())
		}
	}
}

// Upstream requires at least one flag; without one the command must refuse
// rather than fall back to clearing everything.
func TestLBQuotaUnset_RequiresAFlag(t *testing.T) {
	cmd := newLBQuotaUnsetCommand(&auth.Options{}, &output.Options{Format: output.FormatTable})
	cmd.SetArgs([]string{"p1"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected an error when no quota flag is given, got nil")
	}
	if !strings.Contains(err.Error(), "nothing to unset") {
		t.Errorf("error = %v, want a \"nothing to unset\" message", err)
	}
}

// All seven upstream flags must exist, or scripts written against
// python-octaviaclient break.
func TestLBQuotaUnset_HasAllSevenFlags(t *testing.T) {
	cmd := newLBQuotaUnsetCommand(&auth.Options{}, &output.Options{Format: output.FormatTable})
	for _, name := range []string{"loadbalancer", "listener", "pool", "member", "healthmonitor", "l7policy", "l7rule"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("--%s is not registered on \"loadbalancer quota unset\"", name)
		}
	}
}

// `quota reset` keeps the clear-everything behaviour: DELETE /v2.0/quotas/<id>.
func TestRunLBQuotaReset_DeletesTheWholeQuotaSet(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotPath string
	fakeServer.Mux.HandleFunc("/v2.0/quotas/p1", func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})

	var buf bytes.Buffer
	if err := runLBQuotaReset(context.Background(), lbClient(fakeServer), "p1", &buf); err != nil {
		t.Fatalf("runLBQuotaReset error: %v", err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/v2.0/quotas/p1" {
		t.Errorf("request = %s %s, want DELETE /v2.0/quotas/p1", gotMethod, gotPath)
	}
	if !strings.Contains(buf.String(), "Reset load balancer quotas for project p1") {
		t.Errorf("output = %q", buf.String())
	}
}

// `loadbalancer quota list` reads GET /v2.0/quotas, which gophercloud does not
// wrap; it must share the prefix the typed quota calls use.
func TestRunLBQuotaList_RawEndpointAndPagination(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var paths []string
	fakeServer.Mux.HandleFunc("/v2.0/quotas", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, "GET")
		paths = append(paths, r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("marker") == "" {
			// First page, with a next link. The legacy `load_balancer` and
			// `health_monitor` spellings must be understood too.
			_, _ = w.Write([]byte(`{
              "quotas": [
                {"project_id": "p1", "loadbalancer": 5, "listener": -1, "pool": 4,
                 "member": 50, "healthmonitor": 10, "l7policy": 5, "l7rule": 20},
                {"project_id": "p2", "load_balancer": 1, "health_monitor": 2}
              ],
              "quotas_links": [{"rel": "next", "href": "` + fakeServer.Server.URL + `/v2.0/quotas?marker=p2"}]
            }`))
			return
		}
		_, _ = w.Write([]byte(`{"quotas": [{"project_id": "p3", "loadbalancer": 7}], "quotas_links": []}`))
	})

	o := &output.Options{Format: output.FormatCSV}
	var buf bytes.Buffer
	if err := runLBQuotaList(context.Background(), lbClient(fakeServer), o, "", &buf); err != nil {
		t.Fatalf("runLBQuotaList error: %v", err)
	}
	if len(paths) != 2 {
		t.Fatalf("requests = %v, want the next link to be followed", paths)
	}
	if paths[0] != "/v2.0/quotas" {
		t.Errorf("first request = %q, want /v2.0/quotas", paths[0])
	}
	out := buf.String()
	for _, want := range []string{
		"Project ID,Load Balancer,Listener,Pool,Member,Health Monitor,L7Policy,L7Rule",
		"p1,5,-1,4,50,10,5,20",
		"p2,1,,,,2,,", // legacy spellings map across; absent keys stay empty
		"p3,7,,,,,,",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s", want, out)
		}
	}
}

// --project narrows the listing to one project via the API's own filter.
func TestRunLBQuotaList_ProjectFilter(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotQuery string
	fakeServer.Mux.HandleFunc("/v2.0/quotas", func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"quotas": [{"project_id": "p1", "loadbalancer": 5}], "quotas_links": []}`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runLBQuotaList(context.Background(), lbClient(fakeServer), o, "p1", &buf); err != nil {
		t.Fatalf("runLBQuotaList error: %v", err)
	}
	if gotQuery != "project_id=p1" {
		t.Errorf("query = %q, want project_id=p1", gotQuery)
	}
}

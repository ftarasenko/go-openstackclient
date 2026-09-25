package network

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	th "github.com/gophercloud/gophercloud/v2/testhelper"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// These tests execute every leaf of the plugin namespaces (fwaas, bgp,
// bgpvpn, vpnaas, tap mirror) through cobra, three ways: against a mock that
// answers everything, with authentication failing, and with the API failing.
// The seam tests pin request bodies; this pins the RunE glue — a flag bound to
// the wrong field, a verb wired to the wrong seam, an error swallowed.

const (
	pluginFixtureID   = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	pluginFixtureName = "fixture"
)

// pluginFixtureObj is the one object every mocked response carries.
var pluginFixtureObj = map[string]any{
	"id": pluginFixtureID, "name": pluginFixtureName, "project_id": pluginFixtureID, "tags": []string{"t1"},
}

// pluginListKeys maps a collection's last URL segment to its response key.
// A list must carry exactly that key: AllPages merges pages under the body's
// only key and fails on a body with several.
var pluginListKeys = map[string]string{
	"firewall_groups": "firewall_groups", "firewall_policies": "firewall_policies", "firewall_rules": "firewall_rules",
	"bgp-speakers": "bgp_speakers", "bgp-peers": "bgp_peers", "agents": "agents", "bgp-dragents": "agents",
	"get_advertised_routes": "advertised_routes",
	"bgpvpns":               "bgpvpns", "network_associations": "network_associations",
	"router_associations": "router_associations", "port_associations": "port_associations",
	"ikepolicies": "ikepolicies", "ipsecpolicies": "ipsecpolicies", "ipsec-site-connections": "ipsec_site_connections",
	"vpnservices": "vpnservices", "endpoint-groups": "endpoint_groups", "tap_mirrors": "tap_mirrors",
	"networks": "networks", "routers": "routers", "ports": "ports", "floatingips": "floatingips",
	"subnetpools": "subnetpools",
}

// pluginSingleKeys are the wrappers a single-resource response may use. The
// body carries all of them, plus the object at the top level for the
// unwrapped insert_rule/remove_rule responses; each extractor picks its own.
var pluginSingleKeys = []string{
	"firewall_group", "firewall_policy", "firewall_rule",
	"bgp_speaker", "bgp_peer", "agent",
	"bgpvpn", "network_association", "router_association", "port_association",
	"ikepolicy", "ipsecpolicy", "ipsec_site_connection", "vpnservice", "endpoint_group",
	"tap_mirror", "network", "router", "port", "floatingip",
}

// pluginFixtureHandler answers any request in the plugin namespaces.
func pluginFixtureHandler(t *testing.T, requests *int) http.HandlerFunc {
	t.Helper()
	single := map[string]any{"id": pluginFixtureID, "name": pluginFixtureName, "tags": []string{"t1"}} // tags: the /tags PUT reply
	for _, k := range pluginSingleKeys {
		single[k] = pluginFixtureObj
	}
	// gophercloud's SubnetPool decoder insists on the prefix lengths.
	single["subnetpool"] = map[string]any{
		"id": pluginFixtureID, "name": pluginFixtureName,
		"default_prefixlen": 24, "min_prefixlen": 8, "max_prefixlen": 32,
	}
	return func(w http.ResponseWriter, r *http.Request) {
		*requests++
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var body any = single
		segs := strings.Split(strings.TrimSuffix(r.URL.Path, "/"), "/")
		if key, ok := pluginListKeys[segs[len(segs)-1]]; ok && r.Method == http.MethodGet {
			body = map[string]any{key: []any{pluginFixtureObj}}
		}
		b, err := json.Marshal(body)
		if err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
		}
		_, _ = w.Write(b)
	}
}

// execPlugin runs argv through the real command tree with the given
// authenticator result.
func execPlugin(t *testing.T, authErr error, fakeServer th.FakeServer, argv []string) (string, error) {
	t.Helper()
	a := &auth.Options{}
	provider := &gophercloud.ProviderClient{
		TokenID: "fake-token",
		EndpointLocator: func(gophercloud.EndpointOpts) (string, error) {
			return fakeServer.Server.URL + "/", nil
		},
	}
	a.SetAuthenticatorForTest(func(context.Context) (*auth.Client, error) {
		if authErr != nil {
			return nil, authErr
		}
		return a.NewClientForTest(provider, gophercloud.EndpointOpts{}), nil
	})
	root := &cobra.Command{Use: "koc", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(NewCommand(a, &output.Options{Format: output.FormatCSV})...)
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs(argv)
	err := root.ExecuteContext(context.Background())
	return buf.String(), err
}

// pluginLeaves is one invocation per leaf; U is an ID (passed through by the
// resolvers) and N a name (resolved by a list call).
func pluginLeaves() [][]string {
	const U, N = pluginFixtureID, pluginFixtureName
	return [][]string{
		// fwaas
		{"firewall", "group", "create", "fw1", "--ingress-firewall-policy", U, "--port", U},
		{"firewall", "group", "delete", U},
		{"firewall", "group", "list", "--long"},
		{"firewall", "group", "set", N, "--name", "fw2", "--egress-firewall-policy", U},
		{"firewall", "group", "show", N},
		{"firewall", "group", "unset", U, "--ingress-firewall-policy"},
		{"firewall", "group", "policy", "create", "p1", "--firewall-rule", U},
		{"firewall", "group", "policy", "delete", U},
		{"firewall", "group", "policy", "list", "--long"},
		{"firewall", "group", "policy", "set", N, "--name", "p2"},
		{"firewall", "group", "policy", "show", N},
		{"firewall", "group", "policy", "unset", U, "--audited"},
		{"firewall", "group", "policy", "add", "rule", U, U},
		{"firewall", "group", "policy", "remove", "rule", U, U},
		{"firewall", "group", "rule", "create", "r1", "--protocol", "tcp", "--action", "allow"},
		{"firewall", "group", "rule", "delete", U},
		{"firewall", "group", "rule", "list", "--long"},
		{"firewall", "group", "rule", "set", N, "--name", "r2"},
		{"firewall", "group", "rule", "show", N},
		{"firewall", "group", "rule", "unset", U, "--enable-rule"},
		// bgp
		{"bgp", "speaker", "create", "s1", "--local-as", "64512"},
		{"bgp", "speaker", "delete", U},
		{"bgp", "speaker", "list"},
		{"bgp", "speaker", "set", N, "--name", "s2"},
		{"bgp", "speaker", "show", N},
		{"bgp", "speaker", "add", "network", U, U},
		{"bgp", "speaker", "remove", "network", U, U},
		{"bgp", "speaker", "add", "peer", U, U},
		{"bgp", "speaker", "remove", "peer", U, U},
		{"bgp", "speaker", "list", "advertised", "routes", U},
		{"bgp", "peer", "create", "p1", "--peer-ip", "192.0.2.1", "--remote-as", "64513"},
		{"bgp", "peer", "delete", U},
		{"bgp", "peer", "list"},
		{"bgp", "peer", "set", N, "--name", "p2"},
		{"bgp", "peer", "show", N},
		{"bgp", "dragent", "add", "speaker", U, U},
		{"bgp", "dragent", "remove", "speaker", U, U},
		{"bgp", "dragent", "list"},
		{"bgp", "dragent", "list", "--bgp-speaker", U},
		// bgpvpn
		{"bgpvpn", "create", "--name", "v1", "--route-target", "64512:1"},
		{"bgpvpn", "delete", U},
		{"bgpvpn", "list", "--long"},
		{"bgpvpn", "set", N, "--name", "v2"},
		{"bgpvpn", "show", N},
		{"bgpvpn", "unset", U, "--all-route-target"},
		{"bgpvpn", "network", "association", "create", U, U},
		{"bgpvpn", "network", "association", "delete", U, U},
		{"bgpvpn", "network", "association", "list", U},
		{"bgpvpn", "network", "association", "show", U, U},
		{"bgpvpn", "router", "association", "create", U, U, "--advertise_extra_routes"},
		{"bgpvpn", "router", "association", "delete", U, U},
		{"bgpvpn", "router", "association", "list", U, "--long"},
		{"bgpvpn", "router", "association", "set", U, U, "--no-advertise_extra_routes"},
		{"bgpvpn", "router", "association", "show", U, U},
		{"bgpvpn", "router", "association", "unset", U, U, "--advertise_extra_routes"},
		{"bgpvpn", "port", "association", "create", U, U, "--prefix-route", "prefix=192.0.2.0/24"},
		{"bgpvpn", "port", "association", "delete", U, U},
		{"bgpvpn", "port", "association", "list", U, "--long"},
		{"bgpvpn", "port", "association", "set", U, U, "--no-prefix-route"},
		{"bgpvpn", "port", "association", "show", U, U},
		{"bgpvpn", "port", "association", "unset", U, U, "--all-prefix-routes"},
		// vpnaas
		{"vpn", "endpoint", "group", "create", "e1", "--type", "cidr", "--value", "192.0.2.0/24"},
		{"vpn", "endpoint", "group", "delete", U},
		{"vpn", "endpoint", "group", "list", "--long"},
		{"vpn", "endpoint", "group", "set", N, "--name", "e2"},
		{"vpn", "endpoint", "group", "show", N},
		{"vpn", "ike", "policy", "create", "i1"},
		{"vpn", "ike", "policy", "delete", U},
		{"vpn", "ike", "policy", "list", "--long"},
		{"vpn", "ike", "policy", "set", N, "--name", "i2"},
		{"vpn", "ike", "policy", "show", N},
		{"vpn", "ipsec", "policy", "create", "p1"},
		{"vpn", "ipsec", "policy", "delete", U},
		{"vpn", "ipsec", "policy", "list", "--long"},
		{"vpn", "ipsec", "policy", "set", N, "--name", "p2"},
		{"vpn", "ipsec", "policy", "show", N},
		{"vpn", "ipsec", "site", "connection", "create", "c1", "--vpnservice", U, "--ikepolicy", U,
			"--ipsecpolicy", U, "--peer-address", "192.0.2.1", "--peer-id", "192.0.2.1", "--psk", "secret",
			"--peer-cidr", "198.51.100.0/24"},
		{"vpn", "ipsec", "site", "connection", "delete", U},
		{"vpn", "ipsec", "site", "connection", "list", "--long"},
		{"vpn", "ipsec", "site", "connection", "set", N, "--name", "c2"},
		{"vpn", "ipsec", "site", "connection", "show", N},
		{"vpn", "service", "create", "s1", "--router", U},
		{"vpn", "service", "delete", U},
		{"vpn", "service", "list", "--long"},
		{"vpn", "service", "set", N, "--name", "s2"},
		{"vpn", "service", "show", N},
		// tap mirror
		{"tap", "mirror", "create", "--name", "m1", "--port", U, "--directions", "IN=99",
			"--remote-ip", "192.0.2.9", "--mirror-type", "erspanv1"},
		{"tap", "mirror", "delete", U},
		{"tap", "mirror", "list"},
		{"tap", "mirror", "show", N},
		{"tap", "mirror", "update", N, "--name", "m2"},
	}
}

func TestExec_PluginLeaves_Succeed(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	requests := 0
	fakeServer.Mux.HandleFunc("/", pluginFixtureHandler(t, &requests))
	for _, argv := range pluginLeaves() {
		t.Run(strings.Join(argv[:3], " "), func(t *testing.T) {
			before := requests
			if out, err := execPlugin(t, nil, fakeServer, argv); err != nil {
				t.Fatalf("koc %s: %v\n%s", strings.Join(argv, " "), err, out)
			}
			if requests == before {
				t.Errorf("koc %s made no request", strings.Join(argv, " "))
			}
		})
	}
}

func TestExec_PluginLeaves_AuthFailureIsAnError(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/", func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("request %s %s made without credentials", r.Method, r.URL.Path)
	})
	authErr := errors.New("no credentials")
	for _, argv := range pluginLeaves() {
		if _, err := execPlugin(t, authErr, fakeServer, argv); !errors.Is(err, authErr) {
			t.Errorf("koc %s: err = %v, want the auth error", strings.Join(argv, " "), err)
		}
	}
}

// Every API failure must surface as an error — including through the
// explain* helpers, which here find no extension list either.
func TestExec_PluginLeaves_APIFailureIsAnError(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	for _, argv := range pluginLeaves() {
		if _, err := execPlugin(t, nil, fakeServer, argv); err == nil {
			t.Errorf("koc %s: a 404 from the API exited 0", strings.Join(argv, " "))
		}
	}
}

// The set/unset tails of the core nouns share one shape (PUT or GET, then the
// tag change, then render); each is driven with an attribute and a tag so both
// halves run.
func TestExec_UpdateTails_Succeed(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	requests, tagPuts := 0, 0
	h := pluginFixtureHandler(t, &requests)
	fakeServer.Mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/tags") {
			tagPuts++
		}
		h(w, r)
	})
	const U = pluginFixtureID
	for _, argv := range [][]string{
		{"network", "set", U, "--name", "n2", "--tag", "t2"},
		{"network", "unset", U, "--tag", "t1"},
		{"router", "set", U, "--name", "r2", "--tag", "t2"},
		{"router", "unset", U, "--tag", "t1"},
		{"port", "set", U, "--name", "p2", "--tag", "t2"},
		{"port", "unset", U, "--tag", "t1"},
		{"floating", "ip", "set", U, "--description", "d", "--tag", "t2"},
		{"floating", "ip", "unset", U, "--tag", "t1"},
	} {
		before := tagPuts
		out, err := execPlugin(t, nil, fakeServer, argv)
		if err != nil {
			t.Fatalf("koc %s: %v\n%s", strings.Join(argv, " "), err, out)
		}
		if !strings.Contains(out, U) {
			t.Errorf("koc %s did not render the resource:\n%s", strings.Join(argv, " "), out)
		}
		if tagPuts == before {
			t.Errorf("koc %s did not apply the tag change", strings.Join(argv, " "))
		}
	}
}

// subnet pool create/set fold their flag pairs in check before any request,
// and port list settles --network/--project before the list call.
func TestExec_SubnetPoolFlagPairsAndPortOwnerFilters(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	requests := 0
	fakeServer.Mux.HandleFunc("/", pluginFixtureHandler(t, &requests))
	const U = pluginFixtureID
	for _, argv := range [][]string{
		{"subnet", "pool", "create", "sp1", "--pool-prefix", "10.0.0.0/8", "--share", "--default"},
		{"subnet", "pool", "set", U, "--no-default"},
		{"port", "list", "--network", U, "--project", U},
	} {
		if out, err := execPlugin(t, nil, fakeServer, argv); err != nil {
			t.Fatalf("koc %s: %v\n%s", strings.Join(argv, " "), err, out)
		}
	}
	for _, argv := range [][]string{
		{"subnet", "pool", "create", "sp1", "--pool-prefix", "10.0.0.0/8", "--share", "--no-share"},
		{"subnet", "pool", "set", U, "--default", "--no-default"},
	} {
		before := requests
		_, err := execPlugin(t, nil, fakeServer, argv)
		if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
			t.Errorf("koc %s: err = %v, want a mutually-exclusive error", strings.Join(argv, " "), err)
		}
		if requests != before {
			t.Errorf("koc %s sent a request before rejecting its flags", strings.Join(argv, " "))
		}
	}
}

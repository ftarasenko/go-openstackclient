package network

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/layer3/routers"
	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// An unknown -c must fail before any request: after the write the resource
// exists, and an error then reads as a failed create that a script retries.
func TestWriteVerbs_UnknownColumnSendsNothing(t *testing.T) {
	ctx := context.Background()
	w := io.Discard
	for name, run := range map[string]func(*gophercloud.ServiceClient, *output.Options) error{
		"address scope create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runAddressScopeCreate(ctx, c, o, "x", &addressScopeCreateFlags{}, w)
		},
		"address group create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runAddressGroupCreate(ctx, c, o, "x", &addressGroupCreateFlags{}, w)
		},
		"network agent set": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runAgentSet(ctx, c, o, "x", &agentSetFlags{}, nil, w)
		},
		"bgp peer create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runBGPPeerCreate(ctx, c, o, "x", &bgpPeerCreateFlags{}, nil, w)
		},
		"bgp speaker create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runBGPSpeakerCreate(ctx, c, o, "x", &bgpSpeakerCreateFlags{}, w)
		},
		"bgpvpn create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runBGPVPNCreate(ctx, c, o, &bgpvpnCreateFlags{}, nil, w)
		},
		"bgpvpn network association create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runBGPVPNNetworkAssocCreate(ctx, c, o, bgpvpnAssocRef{}, "", w)
		},
		"bgpvpn router association create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runBGPVPNRouterAssocCreate(ctx, c, o, bgpvpnAssocRef{}, "", bgpvpnAdvertiseFlags{}, w)
		},
		"bgpvpn port association create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runBGPVPNPortAssocCreate(ctx, c, o, bgpvpnAssocRef{}, "", &bgpvpnPortAssocFlags{}, w)
		},
		"network rbac create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runRBACCreate(ctx, c, o, "x", &rbacCreateFlags{}, w)
		},
		"network segment create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runSegmentCreate(ctx, c, o, "x", &segmentCreateFlags{}, w)
		},
		"floating ip port forwarding create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runPortForwardingCreate(ctx, c, o, "x", &portForwardingFlags{}, w)
		},
		"router add route": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runRouterRoute(ctx, c, o, "x", nil, true, w)
		},
		"floating ip create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runFloatingIPCreate(ctx, c, o, "x", &floatingIPCreateFlags{}, w)
		},
		"firewall group policy create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runFirewallPolicyCreate(ctx, c, o, &fwPolicyFlags{}, w)
		},
		"firewall group rule create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runFirewallRuleCreate(ctx, c, o, &fwRuleFlags{}, w)
		},
		"firewall group create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runFirewallGroupCreate(ctx, c, o, &fwGroupFlags{}, w)
		},
		"network create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runNetworkCreate(ctx, c, o, "x", &networkCreateFlags{}, w)
		},
		"port create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runPortCreate(ctx, c, o, "x", &portCreateFlags{}, nil, w)
		},
		"network qos policy create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runQoSPolicyCreate(ctx, c, o, "x", &qosPolicyCreateFlags{}, w)
		},
		"network qos rule create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runQoSRuleCreate(ctx, c, o, "x", qosRuleKind{}, nil, w)
		},
		"router add gateway": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runRouterAddGateway(ctx, c, o, "x", "y", nil, w)
		},
		"router create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runRouterCreate(ctx, c, o, "x", &routerCreateFlags{}, w)
		},
		"security group rule create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runSecurityGroupRuleCreate(ctx, c, o, "x", &secGroupRuleCreateFlags{}, w)
		},
		"security group create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runSecurityGroupCreate(ctx, c, o, "x", &secGroupCreateFlags{}, nil, w)
		},
		"subnet create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runSubnetCreate(ctx, c, o, "x", &subnetCreateFlags{}, w)
		},
		"subnet pool create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runSubnetPoolCreate(ctx, c, o, "x", &subnetPoolWriteFlags{}, "", w)
		},
		"tap mirror create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runTapMirrorCreate(ctx, c, o, &tapMirrorCreateFlags{}, nil, w)
		},
		"network trunk create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runTrunkCreate(ctx, c, o, "x", &trunkCreateFlags{}, w)
		},
		"vpn endpoint group create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runVPNEndpointGroupCreate(ctx, c, o, &vpnEndpointGroupFlags{}, w)
		},
		"vpn ike policy create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runIKEPolicyCreate(ctx, c, o, &vpnPolicyFlags{}, w)
		},
		"vpn ipsec policy create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runIPsecPolicyCreate(ctx, c, o, &vpnPolicyFlags{}, w)
		},
		"vpn ipsec site connection create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runIPsecSiteConnectionCreate(ctx, c, o, &ipsecSiteConnectionFlags{}, w)
		},
		"vpn service create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runVPNServiceCreate(ctx, c, o, &vpnServiceFlags{}, w)
		},
	} {
		t.Run(name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()
			var calls int
			fakeServer.Mux.HandleFunc("/", func(http.ResponseWriter, *http.Request) { calls++ })

			o := &output.Options{Format: output.FormatValue, Columns: []string{"id", "bogus"}}
			err := run(networkClient(fakeServer), o)
			var ce *output.ColumnError
			if !errors.As(err, &ce) || ce.Rendering || strings.Join(ce.Unknown, ",") != "bogus" {
				t.Fatalf("err = %v, want a pre-flight ColumnError for bogus", err)
			}
			if calls != 0 {
				t.Errorf("%d request(s) sent despite the bad column, want none", calls)
			}
		})
	}
}

// The catalogs come from the builders on a zero value, so only the fields a
// builder adds when neutron sends them can drift: render each with all of them
// present and require the catalog to cover the result.
func TestWriteVerbs_CatalogsCoverConditionalFields(t *testing.T) {
	var n networkExt
	mustDecode(t, `{"id":"n","qinq":true,"l2_adjacency":true}`, &n)
	var s subnetExt
	mustDecode(t, `{"id":"s","router:external":true}`, &s)
	var ext routerExtAttrs
	mustDecode(t, `{"ha":true,"availability_zones":["az"],"flavor_id":"f","enable_ndp_proxy":true,
		"enable_default_route_bfd":true,"enable_default_route_ecmp":true,"evpn_vni":7,"enable_snat":true}`, &ext)

	for name, tc := range map[string]struct {
		rendered, catalog []string
	}{
		"network": {fieldNames(networkShowFields(&n)), networkColumns},
		"subnet":  {fieldNames(subnetShowFields(&s)), subnetColumns},
		"router":  {fieldNames(routerDetailFields(&routers.Router{}, ext)), routerColumns},
		// QoS rules render as neutron returns them, one shape per rule type.
		"qos rule": {qosRuleFixtureKeys(t), qosRuleColumns},
	} {
		for _, f := range tc.rendered {
			if !containsString(tc.catalog, f) {
				t.Errorf("%s: rendered field %q is missing from the catalog, so -c %s would be refused", name, f, f)
			}
		}
	}
}

func qosRuleFixtureKeys(t *testing.T) []string {
	t.Helper()
	var keys []string
	for _, body := range []string{
		`{"id":"r","max_kbps":1,"max_burst_kbps":1,"direction":"egress","qos_policy_id":"p","type":"bandwidth_limit"}`,
		`{"id":"r","dscp_mark":10}`,
		`{"id":"r","min_kbps":1,"direction":"egress"}`,
		`{"id":"r","min_kpps":1,"direction":"any"}`,
		`{"id":"r","max_kpps":1,"max_burst_kpps":1,"direction":"ingress","project_id":"p","tenant_id":"p"}`,
	} {
		var m map[string]any
		mustDecode(t, body, &m)
		for k := range m {
			keys = append(keys, k)
		}
	}
	return keys
}

func mustDecode(t *testing.T, body string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(body), v); err != nil {
		t.Fatalf("decoding %s: %v", body, err)
	}
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

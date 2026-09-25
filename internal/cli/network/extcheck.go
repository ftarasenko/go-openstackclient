package network

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
)

// attrExtensions maps a neutron body attribute to the API extension that
// defines it. Neutron has no microversions: a cloud without the extension
// answers a request carrying the attribute with a bare 400 ("Unrecognized
// attribute(s)"), which says nothing about why. Upstream OSC turns that into a
// named-extension error for a handful of attributes
// (network/common.py check_missing_extension_if_error); koc does the same for
// every extension attribute it can send, which matters because the fleet spans
// Zed to current and most of the post-Zed flags are exactly these.
//
// Aliases are read from neutron-lib 5.0.1's api/definitions (ALIAS), not from
// memory; re-check there when adding an entry.
var attrExtensions = map[string]string{
	// upstream's own map
	"allowed_address_pairs": "allowed-address-pairs",
	"dns_domain":            "dns-integration",
	"dns_name":              "dns-integration",
	"extra_dhcp_opts":       "extra_dhcp_opt",
	"leak_routes":           "ovn-bgp",
	"qos_policy_id":         "qos",
	"security_groups":       "security-groups",
	// the rest of what koc sends
	"port_security_enabled":     "port-security",
	"is_default":                "auto-allocated-topology",
	"vlan_transparent":          "vlan-transparent",
	"qinq":                      "qinq", // neutron's wire name (QINQ_FIELD); see netAttrQinQ
	"vlan_qinq":                 "qinq", // upstream OSC's spelling, reachable via --extra-property
	"pvlan":                     "pvlan",
	"pvlan_type":                "pvlan",
	"pvlan_community":           "pvlan",
	"ha":                        "l3-ha",
	"distributed":               "dvr",
	"flavor_id":                 "l3-flavors",
	"enable_ndp_proxy":          "l3-ext-ndp-proxy",
	"enable_default_route_bfd":  "enable-default-route-bfd",
	"enable_default_route_ecmp": "enable-default-route-ecmp",
	"evpn_vni":                  "evpn",
	"advertise_host":            "evpn",
	"propagate_uplink_status":   "uplink-status-propagation",
	"data_plane_status":         "data-plane-status",
	"trusted":                   "port-trusted-vif",
	"hardware_offload_type":     "port-hardware-offload-type",
	"hints":                     "port-hints",
	"numa_affinity_policy":      "port-numa-affinity-policy",
	"device_profile":            "port-device-profile",
	"dns_publish_fixed_ip":      "subnet-dns-publish-fixed-ip",
	"service_types":             "subnet-service-types",
	fieldBindingHostID:          "binding",
	"binding:profile":           "binding",
	"binding:vnic_type":         "binding",
	// Synthetic keys, which no request body carries: a port write adds them to
	// the map it explains (portExplainAttrs) where the extension depends on the
	// attribute's value or on the resource, not on the attribute name alone.
	"numa_affinity_policy=socket": "port-numa-affinity-policy-socket",
	"port.dns_domain":             "dns-domain-ports",
}

// explainMissingExtension annotates a failed neutron write whose body carried
// extension attributes. On an HTTP error it lists the cloud's extensions once
// and, when any attribute's extension is absent, names it — so
// `router create --enable-ndp-proxy` on a Zed cloud says "requires the
// l3-ext-ndp-proxy extension" instead of a bare 400. Any other error, or a
// failure to list extensions, returns err unchanged: the check only ever adds
// information.
func explainMissingExtension(ctx context.Context, client *gophercloud.ServiceClient, err error, attrs map[string]any) error {
	if err == nil || len(attrs) == 0 {
		return err
	}
	var httpErr gophercloud.ErrUnexpectedResponseCode
	if !errors.As(err, &httpErr) {
		return err
	}
	byAlias := map[string][]string{}
	for attr := range attrs {
		if alias, ok := attrExtensions[attr]; ok {
			byAlias[alias] = append(byAlias[alias], attr)
		}
	}
	if len(byAlias) == 0 {
		return err
	}
	exts, lerr := listNetworkExtensions(ctx, client)
	if lerr != nil {
		return err
	}
	enabled := make(map[string]bool, len(exts))
	for _, e := range exts {
		enabled[e.Alias] = true
	}
	var missing []string
	for alias, attrs := range byAlias {
		if !enabled[alias] {
			slices.Sort(attrs)
			missing = append(missing, fmt.Sprintf("%s (for %s)", alias, strings.Join(attrs, ", ")))
		}
	}
	if len(missing) == 0 {
		return err
	}
	slices.Sort(missing)
	return fmt.Errorf("%w\nthis cloud's neutron does not enable the extension(s) the request needs: %s",
		err, strings.Join(missing, "; "))
}

// Extension aliases of the neutron plugin services (neutron-lib 5.0.1
// api/definitions ALIAS). A plugin that is not loaded has no URL tree at all,
// so every call on it answers 404 — indistinguishable from "no such resource"
// unless the error says otherwise.
const (
	extVPNaaS             = "vpnaas"
	extVPNEndpointGroups  = "vpn-endpoint-groups"
	extFWaaSv2            = "fwaas_v2"
	extBGPVPN             = "bgpvpn"
	extBGPVPNRoutesCtl    = "bgpvpn-routes-control"
	extBGP                = "bgp"
	extBGPDRAgentSchedule = "bgp_dragent_scheduler"
	extTapMirror          = "tap-mirror"
)

// explainMissingService annotates a 404 from a plugin service's endpoint: when
// the cloud's extension list lacks alias, the service is not deployed, and the
// error says so instead of reading as a missing resource. Any other error, a
// 404 on a cloud that has the extension (a genuinely missing resource), or a
// failure to list extensions returns err unchanged.
func explainMissingService(ctx context.Context, client *gophercloud.ServiceClient, err error, alias string) error {
	if err == nil || !gophercloud.ResponseCodeIs(err, http.StatusNotFound) {
		return err
	}
	exts, lerr := listNetworkExtensions(ctx, client)
	if lerr != nil {
		return err
	}
	for _, e := range exts {
		if e.Alias == alias {
			return err
		}
	}
	return fmt.Errorf("%w\nthis cloud's neutron does not enable the %s extension; the service is not deployed here", err, alias)
}

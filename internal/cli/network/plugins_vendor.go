package network

// Temporary: these blank imports keep the neutron plugin packages in vendor/
// while the plugin nouns (VPNaaS, FWaaS, BGPVPN, dynamic routing, TaaS tap
// mirrors) land in separate commits. `go mod vendor` only keeps imported
// packages, so without a live importer the offline build would lose them. This
// file is deleted by the commit that gives the last of them a real importer.
import (
	_ "github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/bgp/peers"              // BGP peers
	_ "github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/bgp/speakers"           // BGP speakers
	_ "github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/bgpvpns"                // BGP VPNs
	_ "github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/fwaas_v2/groups"        // firewall groups
	_ "github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/fwaas_v2/policies"      // firewall policies
	_ "github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/fwaas_v2/rules"         // firewall rules
	_ "github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/taas/tapmirrors"        // tap mirrors
	_ "github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/vpnaas/endpointgroups"  // VPN endpoint groups
	_ "github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/vpnaas/ikepolicies"     // IKE policies
	_ "github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/vpnaas/ipsecpolicies"   // IPsec policies
	_ "github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/vpnaas/services"        // VPN services
	_ "github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/vpnaas/siteconnections" // IPsec site connections
)

package network_test

import (
	"context"
	"testing"

	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/layer3/addressscopes"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/layer3/floatingips"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/layer3/portforwarding"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/layer3/routers"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/qos/policies"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/rbacpolicies"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/security/addressgroups"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/security/groups"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/security/rules"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/segments"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/subnetpools"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/trunks"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/networks"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/ports"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/subnets"

	"github.com/ftarasenko/koc/pkg/openstack/network"
)

// Every With…Attrs wrapper puts attrs at the top level of its envelope, and a
// nil attrs leaves the typed body alone.
func TestWithAttrs(t *testing.T) {
	a := map[string]any{"x": 1}
	for _, tc := range []struct {
		key   string
		build func(map[string]any) (map[string]any, error)
	}{
		{"network", func(m map[string]any) (map[string]any, error) {
			return network.WithNetworkCreateAttrs(networks.CreateOpts{}, m).ToNetworkCreateMap()
		}},
		{"network", func(m map[string]any) (map[string]any, error) {
			return network.WithNetworkUpdateAttrs(networks.UpdateOpts{}, m).ToNetworkUpdateMap()
		}},
		{"subnet", func(m map[string]any) (map[string]any, error) {
			return network.WithSubnetCreateAttrs(subnets.CreateOpts{NetworkID: "n", CIDR: "192.0.2.0/24"}, m).ToSubnetCreateMap()
		}},
		{"subnet", func(m map[string]any) (map[string]any, error) {
			return network.WithSubnetUpdateAttrs(subnets.UpdateOpts{}, m).ToSubnetUpdateMap()
		}},
		{"port", func(m map[string]any) (map[string]any, error) {
			return network.WithPortCreateAttrs(ports.CreateOpts{NetworkID: "n"}, m).ToPortCreateMap()
		}},
		{"port", func(m map[string]any) (map[string]any, error) {
			return network.WithPortUpdateAttrs(ports.UpdateOpts{}, m).ToPortUpdateMap()
		}},
		{"router", func(m map[string]any) (map[string]any, error) {
			return network.WithRouterCreateAttrs(routers.CreateOpts{}, m).ToRouterCreateMap()
		}},
		{"router", func(m map[string]any) (map[string]any, error) {
			return network.WithRouterUpdateAttrs(routers.UpdateOpts{}, m).ToRouterUpdateMap()
		}},
		{"floatingip", func(m map[string]any) (map[string]any, error) {
			return network.WithFloatingIPCreateAttrs(floatingips.CreateOpts{FloatingNetworkID: "n"}, m).ToFloatingIPCreateMap()
		}},
		{"floatingip", func(m map[string]any) (map[string]any, error) {
			return network.WithFloatingIPUpdateAttrs(floatingips.UpdateOpts{}, m).ToFloatingIPUpdateMap()
		}},
		{"security_group", func(m map[string]any) (map[string]any, error) {
			return network.WithSecurityGroupCreateAttrs(groups.CreateOpts{Name: "g"}, m).ToSecGroupCreateMap()
		}},
		{"security_group", func(m map[string]any) (map[string]any, error) {
			return network.WithSecurityGroupUpdateAttrs(groups.UpdateOpts{}, m).ToSecGroupUpdateMap()
		}},
		{"security_group_rule", func(m map[string]any) (map[string]any, error) {
			return network.WithSecurityGroupRuleCreateAttrs(rules.CreateOpts{Direction: rules.DirEgress, EtherType: rules.EtherType6, SecGroupID: "g"}, m).ToSecGroupRuleCreateMap()
		}},
		{"subnetpool", func(m map[string]any) (map[string]any, error) {
			return network.WithSubnetPoolCreateAttrs(subnetpools.CreateOpts{Name: "p"}, m).ToSubnetPoolCreateMap()
		}},
		{"subnetpool", func(m map[string]any) (map[string]any, error) {
			return network.WithSubnetPoolUpdateAttrs(subnetpools.UpdateOpts{}, m).ToSubnetPoolUpdateMap()
		}},
		{"address_scope", func(m map[string]any) (map[string]any, error) {
			return network.WithAddressScopeCreateAttrs(addressscopes.CreateOpts{Name: "s", IPVersion: 4}, m).ToAddressScopeCreateMap()
		}},
		{"address_scope", func(m map[string]any) (map[string]any, error) {
			return network.WithAddressScopeUpdateAttrs(addressscopes.UpdateOpts{}, m).ToAddressScopeUpdateMap()
		}},
		{"address_group", func(m map[string]any) (map[string]any, error) {
			return network.WithAddressGroupCreateAttrs(addressgroups.CreateOpts{Name: "g", Addresses: []string{"192.0.2.1/32"}}, m).ToAddressGroupCreateMap()
		}},
		{"address_group", func(m map[string]any) (map[string]any, error) {
			return network.WithAddressGroupUpdateAttrs(addressgroups.UpdateOpts{}, m).ToAddressGroupUpdateMap()
		}},
		{"policy", func(m map[string]any) (map[string]any, error) {
			return network.WithQoSPolicyCreateAttrs(policies.CreateOpts{Name: "q"}, m).ToPolicyCreateMap()
		}},
		{"policy", func(m map[string]any) (map[string]any, error) {
			return network.WithQoSPolicyUpdateAttrs(policies.UpdateOpts{}, m).ToPolicyUpdateMap()
		}},
		{"rbac_policy", func(m map[string]any) (map[string]any, error) {
			return network.WithRBACPolicyCreateAttrs(rbacpolicies.CreateOpts{Action: rbacpolicies.ActionAccessShared, ObjectType: "network", TargetTenant: "*", ObjectID: "n"}, m).ToRBACPolicyCreateMap()
		}},
		{"rbac_policy", func(m map[string]any) (map[string]any, error) {
			return network.WithRBACPolicyUpdateAttrs(rbacpolicies.UpdateOpts{TargetTenant: "p"}, m).ToRBACPolicyUpdateMap()
		}},
		{"segment", func(m map[string]any) (map[string]any, error) {
			return network.WithSegmentCreateAttrs(segments.CreateOpts{NetworkID: "n", NetworkType: "vlan"}, m).ToSegmentCreateMap()
		}},
		{"segment", func(m map[string]any) (map[string]any, error) {
			return network.WithSegmentUpdateAttrs(segments.UpdateOpts{}, m).ToSegmentUpdateMap()
		}},
		{"trunk", func(m map[string]any) (map[string]any, error) {
			return network.WithTrunkCreateAttrs(trunks.CreateOpts{PortID: "p"}, m).ToTrunkCreateMap()
		}},
		{"port_forwarding", func(m map[string]any) (map[string]any, error) {
			return network.WithPortForwardingCreateAttrs(portforwarding.CreateOpts{InternalPortID: "p", Protocol: "tcp"}, m).ToPortForwardingCreateMap()
		}},
		{"port_forwarding", func(m map[string]any) (map[string]any, error) {
			return network.WithPortForwardingUpdateAttrs(portforwarding.UpdateOpts{}, m).ToPortForwardingUpdateMap()
		}},
	} {
		with, err := tc.build(a)
		inner, ok := with[tc.key].(map[string]any)
		if err != nil || !ok || inner["x"] != 1 {
			t.Errorf("%s with attrs = %v, %v", tc.key, with, err)
		}
		without, err := tc.build(nil)
		if inner, ok := without[tc.key].(map[string]any); err != nil || !ok || inner["x"] != nil {
			t.Errorf("%s without attrs = %v, %v", tc.key, without, err)
		}
	}
}

// odd builds a body whose envelope is not an object.
type odd struct{}

func (odd) ToNetworkCreateMap() (map[string]any, error) { return map[string]any{"network": "?"}, nil }

func TestWithAttrs_UnexpectedShape(t *testing.T) {
	_, err := network.WithNetworkCreateAttrs(odd{}, map[string]any{"x": 1}).ToNetworkCreateMap()
	if err == nil || err.Error() != "unexpected network request body shape: string" {
		t.Errorf("error = %v", err)
	}
}

// oddUpdate is a builder that is not a struct, so it has no revision guard.
type oddUpdate map[string]any

func (o oddUpdate) ToNetworkUpdateMap() (map[string]any, error) {
	return map[string]any{"network": map[string]any(o)}, nil
}

// The revision guard is read through a pointer too, and its absence is fine.
func TestWithAttrs_RevisionGuard(t *testing.T) {
	rev := 3
	seen, sc := fakeNeutron(t, map[string]reply{"PUT /networks/n": {Body: `{"network":{"id":"n"}}`}})
	ctx := context.Background()
	for _, opts := range []networks.UpdateOptsBuilder{
		network.WithNetworkUpdateAttrs(&networks.UpdateOpts{RevisionNumber: &rev}, nil),
		network.WithNetworkUpdateAttrs(oddUpdate{"name": "x"}, map[string]any{"mtu": 1400}),
	} {
		if err := networks.Update(ctx, sc, "n", opts).Err; err != nil {
			t.Fatal(err)
		}
	}
	assertRequests(t, *seen, []req{
		{"PUT", "/networks/n", "", "revision_number=3", `{"network":{}}`},
		{"PUT", "/networks/n", "", "", `{"network":{"mtu":1400,"name":"x"}}`},
	})
}

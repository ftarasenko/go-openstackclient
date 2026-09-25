package network

// Per-resource adapters for bodyExt (attrs.go). Each wraps a gophercloud opts
// builder's To<Resource><Verb>Map and satisfies the same builder interface, so
// a verb writes
//
//	floatingips.Create(ctx, client, withFloatingIPCreateAttrs(opts, extra))
//
// instead of hand-rolling a wrapper type per missing attribute. A nil or empty
// extra returns the typed body unchanged.

type networkCreateExt struct{ bodyExt }

func (e networkCreateExt) ToNetworkCreateMap() (map[string]any, error) { return e.body() }

func withNetworkCreateAttrs(b interface {
	ToNetworkCreateMap() (map[string]any, error)
}, extra map[string]any) networkCreateExt {
	return networkCreateExt{bodyExt{build: b.ToNetworkCreateMap, key: "network", extra: extra}}
}

type networkUpdateExt struct{ bodyExt }

func (e networkUpdateExt) ToNetworkUpdateMap() (map[string]any, error) { return e.body() }

func withNetworkUpdateAttrs(b interface {
	ToNetworkUpdateMap() (map[string]any, error)
}, extra map[string]any) networkUpdateExt {
	return networkUpdateExt{bodyExt{build: b.ToNetworkUpdateMap, key: "network", extra: extra}}
}

type subnetCreateExt struct{ bodyExt }

func (e subnetCreateExt) ToSubnetCreateMap() (map[string]any, error) { return e.body() }

func withSubnetCreateAttrs(b interface {
	ToSubnetCreateMap() (map[string]any, error)
}, extra map[string]any) subnetCreateExt {
	return subnetCreateExt{bodyExt{build: b.ToSubnetCreateMap, key: "subnet", extra: extra}}
}

type subnetUpdateExt struct{ bodyExt }

func (e subnetUpdateExt) ToSubnetUpdateMap() (map[string]any, error) { return e.body() }

func withSubnetUpdateAttrs(b interface {
	ToSubnetUpdateMap() (map[string]any, error)
}, extra map[string]any) subnetUpdateExt {
	return subnetUpdateExt{bodyExt{build: b.ToSubnetUpdateMap, key: "subnet", extra: extra}}
}

type portCreateExt struct{ bodyExt }

func (e portCreateExt) ToPortCreateMap() (map[string]any, error) { return e.body() }

func withPortCreateAttrs(b interface {
	ToPortCreateMap() (map[string]any, error)
}, extra map[string]any) portCreateExt {
	return portCreateExt{bodyExt{build: b.ToPortCreateMap, key: "port", extra: extra}}
}

type portUpdateExt struct{ bodyExt }

func (e portUpdateExt) ToPortUpdateMap() (map[string]any, error) { return e.body() }

func withPortUpdateAttrs(b interface {
	ToPortUpdateMap() (map[string]any, error)
}, extra map[string]any) portUpdateExt {
	return portUpdateExt{bodyExt{build: b.ToPortUpdateMap, key: "port", extra: extra}}
}

type routerCreateExt struct{ bodyExt }

func (e routerCreateExt) ToRouterCreateMap() (map[string]any, error) { return e.body() }

func withRouterCreateAttrs(b interface {
	ToRouterCreateMap() (map[string]any, error)
}, extra map[string]any) routerCreateExt {
	return routerCreateExt{bodyExt{build: b.ToRouterCreateMap, key: "router", extra: extra}}
}

type routerUpdateExt struct{ bodyExt }

func (e routerUpdateExt) ToRouterUpdateMap() (map[string]any, error) { return e.body() }

func withRouterUpdateAttrs(b interface {
	ToRouterUpdateMap() (map[string]any, error)
}, extra map[string]any) routerUpdateExt {
	return routerUpdateExt{bodyExt{build: b.ToRouterUpdateMap, key: "router", extra: extra}}
}

type floatingIPCreateExt struct{ bodyExt }

func (e floatingIPCreateExt) ToFloatingIPCreateMap() (map[string]any, error) { return e.body() }

func withFloatingIPCreateAttrs(b interface {
	ToFloatingIPCreateMap() (map[string]any, error)
}, extra map[string]any) floatingIPCreateExt {
	return floatingIPCreateExt{bodyExt{build: b.ToFloatingIPCreateMap, key: "floatingip", extra: extra}}
}

type floatingIPUpdateExt struct{ bodyExt }

func (e floatingIPUpdateExt) ToFloatingIPUpdateMap() (map[string]any, error) { return e.body() }

func withFloatingIPUpdateAttrs(b interface {
	ToFloatingIPUpdateMap() (map[string]any, error)
}, extra map[string]any) floatingIPUpdateExt {
	return floatingIPUpdateExt{bodyExt{build: b.ToFloatingIPUpdateMap, key: "floatingip", extra: extra}}
}

type secGroupCreateExt struct{ bodyExt }

func (e secGroupCreateExt) ToSecGroupCreateMap() (map[string]any, error) { return e.body() }

func withSecGroupCreateAttrs(b interface {
	ToSecGroupCreateMap() (map[string]any, error)
}, extra map[string]any) secGroupCreateExt {
	return secGroupCreateExt{bodyExt{build: b.ToSecGroupCreateMap, key: "security_group", extra: extra}}
}

type secGroupUpdateExt struct{ bodyExt }

func (e secGroupUpdateExt) ToSecGroupUpdateMap() (map[string]any, error) { return e.body() }

func withSecGroupUpdateAttrs(b interface {
	ToSecGroupUpdateMap() (map[string]any, error)
}, extra map[string]any) secGroupUpdateExt {
	return secGroupUpdateExt{bodyExt{build: b.ToSecGroupUpdateMap, key: "security_group", extra: extra}}
}

type secGroupRuleCreateExt struct{ bodyExt }

func (e secGroupRuleCreateExt) ToSecGroupRuleCreateMap() (map[string]any, error) { return e.body() }

func withSecGroupRuleCreateAttrs(b interface {
	ToSecGroupRuleCreateMap() (map[string]any, error)
}, extra map[string]any) secGroupRuleCreateExt {
	return secGroupRuleCreateExt{bodyExt{build: b.ToSecGroupRuleCreateMap, key: "security_group_rule", extra: extra}}
}

type subnetPoolCreateExt struct{ bodyExt }

func (e subnetPoolCreateExt) ToSubnetPoolCreateMap() (map[string]any, error) { return e.body() }

func withSubnetPoolCreateAttrs(b interface {
	ToSubnetPoolCreateMap() (map[string]any, error)
}, extra map[string]any) subnetPoolCreateExt {
	return subnetPoolCreateExt{bodyExt{build: b.ToSubnetPoolCreateMap, key: "subnetpool", extra: extra}}
}

type subnetPoolUpdateExt struct{ bodyExt }

func (e subnetPoolUpdateExt) ToSubnetPoolUpdateMap() (map[string]any, error) { return e.body() }

func withSubnetPoolUpdateAttrs(b interface {
	ToSubnetPoolUpdateMap() (map[string]any, error)
}, extra map[string]any) subnetPoolUpdateExt {
	return subnetPoolUpdateExt{bodyExt{build: b.ToSubnetPoolUpdateMap, key: "subnetpool", extra: extra}}
}

type addressScopeCreateExt struct{ bodyExt }

func (e addressScopeCreateExt) ToAddressScopeCreateMap() (map[string]any, error) { return e.body() }

func withAddressScopeCreateAttrs(b interface {
	ToAddressScopeCreateMap() (map[string]any, error)
}, extra map[string]any) addressScopeCreateExt {
	return addressScopeCreateExt{bodyExt{build: b.ToAddressScopeCreateMap, key: "address_scope", extra: extra}}
}

type addressScopeUpdateExt struct{ bodyExt }

func (e addressScopeUpdateExt) ToAddressScopeUpdateMap() (map[string]any, error) { return e.body() }

func withAddressScopeUpdateAttrs(b interface {
	ToAddressScopeUpdateMap() (map[string]any, error)
}, extra map[string]any) addressScopeUpdateExt {
	return addressScopeUpdateExt{bodyExt{build: b.ToAddressScopeUpdateMap, key: "address_scope", extra: extra}}
}

type addressGroupCreateExt struct{ bodyExt }

func (e addressGroupCreateExt) ToAddressGroupCreateMap() (map[string]any, error) { return e.body() }

func withAddressGroupCreateAttrs(b interface {
	ToAddressGroupCreateMap() (map[string]any, error)
}, extra map[string]any) addressGroupCreateExt {
	return addressGroupCreateExt{bodyExt{build: b.ToAddressGroupCreateMap, key: "address_group", extra: extra}}
}

type addressGroupUpdateExt struct{ bodyExt }

func (e addressGroupUpdateExt) ToAddressGroupUpdateMap() (map[string]any, error) { return e.body() }

func withAddressGroupUpdateAttrs(b interface {
	ToAddressGroupUpdateMap() (map[string]any, error)
}, extra map[string]any) addressGroupUpdateExt {
	return addressGroupUpdateExt{bodyExt{build: b.ToAddressGroupUpdateMap, key: "address_group", extra: extra}}
}

type qosPolicyCreateExt struct{ bodyExt }

func (e qosPolicyCreateExt) ToPolicyCreateMap() (map[string]any, error) { return e.body() }

func withQosPolicyCreateAttrs(b interface {
	ToPolicyCreateMap() (map[string]any, error)
}, extra map[string]any) qosPolicyCreateExt {
	return qosPolicyCreateExt{bodyExt{build: b.ToPolicyCreateMap, key: "policy", extra: extra}}
}

type qosPolicyUpdateExt struct{ bodyExt }

func (e qosPolicyUpdateExt) ToPolicyUpdateMap() (map[string]any, error) { return e.body() }

func withQosPolicyUpdateAttrs(b interface {
	ToPolicyUpdateMap() (map[string]any, error)
}, extra map[string]any) qosPolicyUpdateExt {
	return qosPolicyUpdateExt{bodyExt{build: b.ToPolicyUpdateMap, key: "policy", extra: extra}}
}

type rbacCreateExt struct{ bodyExt }

func (e rbacCreateExt) ToRBACPolicyCreateMap() (map[string]any, error) { return e.body() }

func withRbacCreateAttrs(b interface {
	ToRBACPolicyCreateMap() (map[string]any, error)
}, extra map[string]any) rbacCreateExt {
	return rbacCreateExt{bodyExt{build: b.ToRBACPolicyCreateMap, key: "rbac_policy", extra: extra}}
}

type rbacUpdateExt struct{ bodyExt }

func (e rbacUpdateExt) ToRBACPolicyUpdateMap() (map[string]any, error) { return e.body() }

func withRbacUpdateAttrs(b interface {
	ToRBACPolicyUpdateMap() (map[string]any, error)
}, extra map[string]any) rbacUpdateExt {
	return rbacUpdateExt{bodyExt{build: b.ToRBACPolicyUpdateMap, key: "rbac_policy", extra: extra}}
}

type segmentCreateExt struct{ bodyExt }

func (e segmentCreateExt) ToSegmentCreateMap() (map[string]any, error) { return e.body() }

func withSegmentCreateAttrs(b interface {
	ToSegmentCreateMap() (map[string]any, error)
}, extra map[string]any) segmentCreateExt {
	return segmentCreateExt{bodyExt{build: b.ToSegmentCreateMap, key: "segment", extra: extra}}
}

type segmentUpdateExt struct{ bodyExt }

func (e segmentUpdateExt) ToSegmentUpdateMap() (map[string]any, error) { return e.body() }

func withSegmentUpdateAttrs(b interface {
	ToSegmentUpdateMap() (map[string]any, error)
}, extra map[string]any) segmentUpdateExt {
	return segmentUpdateExt{bodyExt{build: b.ToSegmentUpdateMap, key: "segment", extra: extra}}
}

type trunkCreateExt struct{ bodyExt }

func (e trunkCreateExt) ToTrunkCreateMap() (map[string]any, error) { return e.body() }

func withTrunkCreateAttrs(b interface {
	ToTrunkCreateMap() (map[string]any, error)
}, extra map[string]any) trunkCreateExt {
	return trunkCreateExt{bodyExt{build: b.ToTrunkCreateMap, key: "trunk", extra: extra}}
}

type portForwardingCreateExt struct{ bodyExt }

func (e portForwardingCreateExt) ToPortForwardingCreateMap() (map[string]any, error) { return e.body() }

func withPortForwardingCreateAttrs(b interface {
	ToPortForwardingCreateMap() (map[string]any, error)
}, extra map[string]any) portForwardingCreateExt {
	return portForwardingCreateExt{bodyExt{build: b.ToPortForwardingCreateMap, key: "port_forwarding", extra: extra}}
}

type portForwardingUpdateExt struct{ bodyExt }

func (e portForwardingUpdateExt) ToPortForwardingUpdateMap() (map[string]any, error) { return e.body() }

func withPortForwardingUpdateAttrs(b interface {
	ToPortForwardingUpdateMap() (map[string]any, error)
}, extra map[string]any) portForwardingUpdateExt {
	return portForwardingUpdateExt{bodyExt{build: b.ToPortForwardingUpdateMap, key: "port_forwarding", extra: extra}}
}

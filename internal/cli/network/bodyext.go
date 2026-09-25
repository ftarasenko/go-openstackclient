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

type trunkCreateExt struct{ bodyExt }

func (e trunkCreateExt) ToTrunkCreateMap() (map[string]any, error) { return e.body() }

func withTrunkCreateAttrs(b interface {
	ToTrunkCreateMap() (map[string]any, error)
}, extra map[string]any) trunkCreateExt {
	return trunkCreateExt{bodyExt{build: b.ToTrunkCreateMap, key: "trunk", extra: extra}}
}

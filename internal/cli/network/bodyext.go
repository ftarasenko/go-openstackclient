package network

// Per-resource adapters for bodyExt (attrs.go). Each wraps a gophercloud opts
// builder's To<Resource><Verb>Map and satisfies the same builder interface, so
// a verb writes
//
//	floatingips.Create(ctx, client, withFloatingIPCreateAttrs(opts, extra))
//
// instead of hand-rolling a wrapper type per missing attribute. A nil or empty
// extra returns the typed body unchanged.

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

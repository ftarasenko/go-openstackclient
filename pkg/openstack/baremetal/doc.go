// Package baremetal is the ironic (baremetal v1) half of koc's API, plus the
// ironic-inspector calls: nodes and their power, provision, console, BIOS,
// firmware, inventory, traits, VIFs, passthru and virtual media; ports, port
// groups, volume connectors and targets, allocations, deploy templates,
// runbooks, inspection rules, shards, conductors and drivers.
//
// Functions take a *gophercloud.ServiceClient from openstack.Session.Baremetal
// (the introspection functions take openstack.Session.Introspection) and
// return gophercloud v2 types where they exist. Resources gophercloud does
// not model (port groups, runbooks, inspection rules, volume resources,
// shards) are raw requests decoded into maps; their writes take a body map
// and a Patch.
//
// koc supports clouds back to Zed (API 1.80). A function whose feature is
// newer turns the 400/404/405/406 an older ironic answers with into a
// *MicroversionError, checked with errors.As, when the client's pin or the
// cloud's maximum explains it.
//
// FindPortGroupByAddress is strict: zero or several matches are a
// *resolve.NotFoundError or *resolve.AmbiguousError. The provision and
// allocation waits take a context (the deadline) and an explicit interval.
package baremetal

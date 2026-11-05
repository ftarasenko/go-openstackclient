// Package dns is the designate (DNS v2) half of koc's API: zones and their
// recordsets, shares, transfers, exports, imports and tasks, plus the admin
// tail (quotas, TSIG keys, limits, pools, PTR records, service statuses,
// blacklists and TLDs).
//
// Functions take a *gophercloud.ServiceClient from openstack.Session.DNS and
// return gophercloud v2 types where they exist. The calls gophercloud has no
// package for are raw requests on the same client, decoded into this
// package's types. Designate's admin headers (all projects, sudo) are a
// property of the client: WithScope returns a copy that sends them on every
// request.
//
// Find…ID functions are strict: a UUID passes through, a name must match
// exactly one resource, and zero or several matches are a
// *resolve.NotFoundError or *resolve.AmbiguousError.
package dns

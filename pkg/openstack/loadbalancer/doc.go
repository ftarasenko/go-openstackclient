// Package loadbalancer is the octavia (load-balancer v2) half of koc's API:
// load balancers and their listeners, pools, members, health monitors and L7
// policies and rules, plus the admin tail (quotas, amphorae, providers,
// flavors and flavor profiles).
//
// Functions take a *gophercloud.ServiceClient from
// openstack.Session.LoadBalancer and return gophercloud v2 types where they
// exist. The calls gophercloud has no package for (the quota listing, defaults
// and per-key unset, amphora configure, delete and stats, provider
// capabilities, and disabling a flavor) are raw requests on the same client.
//
// Find…ID functions are strict: a UUID passes through, a name must match
// exactly one resource, and zero or several matches are a
// *resolve.NotFoundError or *resolve.AmbiguousError.
package loadbalancer

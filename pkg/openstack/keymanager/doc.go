// Package keymanager is the barbican (key-manager v1) half of koc's API:
// secrets, their payloads and consumers, containers, orders and the ACLs of
// secrets and containers.
//
// Functions take a *gophercloud.ServiceClient from
// openstack.Session.KeyManager, which pins barbican's microversion 1.1, and
// return gophercloud v2 types.
//
// Barbican names every resource by an href and has no name lookup (names are
// not unique), so functions take the UUID; RefUUID derives it from an href or
// a bare UUID, and ParseACLRef also tells a secret's href from a container's.
// The list functions return one limit/offset window, as barbican's own client
// does, rather than every page.
package keymanager

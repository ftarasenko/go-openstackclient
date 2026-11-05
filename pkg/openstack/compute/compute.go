// Package compute is koc's nova (compute v2) layer. It is laid out by API
// resource, not by the koc command tree: flavors and keypairs so far.
//
// Every function takes the compute client openstack.Session.Compute returns
// and speaks gophercloud's types. Where gophercloud cannot express a request,
// as with clearing a flavor's description, the function here sends it raw, so
// an importer and the koc CLI send the same requests.
//
// Errors are wrapped with what the call was doing and the reference it was
// given ("deleting flavor "<id>": …"); gophercloud's error stays reachable
// through errors.As, so gophercloud.ResponseCodeIs keeps working on it.
package compute

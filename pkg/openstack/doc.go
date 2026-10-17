// Package openstack is the entry point of koc's public Go API: an
// authenticated [Session] and the per-service gophercloud clients derived from
// it, with the microversion defaults the koc CLI uses.
//
// The koc command-line client is built on these packages, so what it does on
// the wire is what an importer gets. The service packages below this one
// (paging, resolve, image, …) are plain functions on a
// *gophercloud.ServiceClient in gophercloud's own style; a Session is only the
// convenient way to build those clients.
//
// # Building a session
//
// An importer that already authenticates — with an application credential, a
// token, or whatever its deployment uses — hands koc the ProviderClient:
//
//	pc, err := openstack.AuthenticatedClient(ctx, ao) // gophercloud's
//	s := kocopenstack.FromProvider(pc, gophercloud.EndpointOpts{Region: "RegionOne"}, kocopenstack.Options{})
//	nets, err := s.Network()
//
// [New] authenticates from a plain [Config] instead: explicit
// gophercloud.AuthOptions, or a named cloud from clouds.yaml content the caller
// supplies. Neither constructor reads the environment, standard input or a
// terminal, and neither looks for files: paths, prompts and OS_* variables are
// the caller's business (the koc CLI does all three before calling New).
//
// One token, several regions: [Session.ForRegion] returns a Session that shares
// the ProviderClient — and so the token and its re-authentication — but
// resolves catalog endpoints in another region.
//
// # API stability
//
// The public API is the exported identifiers in
// github.com/ftarasenko/go-openstackclient/pkg/... and the gophercloud v2 types
// they accept and return. Nothing under internal/ is part of it, and neither is
// the koc command line: its flags and output are a separate, user-facing
// contract with its own compatibility rules.
//
// Versions follow semantic versioning on the repository's single tag stream
// (one module, one version for the CLI and the API):
//
//   - While the version is v0.y.z, a breaking change to pkg/ bumps the MINOR
//     version — and still follows the deprecation policy below; it does not
//     simply break.
//   - From v1.0.0 on, a breaking change to pkg/ is a new major version with a
//     /vN module path.
//   - Because gophercloud types are part of the API, moving to a new
//     gophercloud major version is a breaking change, and therefore a major
//     version of this module once it is past v1.
//
// # Deprecation policy
//
// An exported identifier is removed only after it has carried a
//
//	// Deprecated: use X instead.
//
// comment for at least two MINOR releases and 90 days, whichever is longer.
// The commit that deprecates it carries a "Deprecated:" footer naming the
// identifier, and the release notes list those footers under their own
// "Deprecations" heading, ahead of the features.
package openstack

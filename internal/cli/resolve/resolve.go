// Package resolve is the CLI's name→ID policy over the public
// pkg/openstack/resolve. A command authenticated against one service (e.g.
// compute) often accepts a reference that lives in another service (an image
// name, a network name, a project name); these helpers take the appropriate
// secondary service client and turn a name into an ID, passing UUIDs through
// untouched.
//
// Resolution policy (shared by all helpers): if the reference already looks
// like a UUID it is returned as-is without an API call; otherwise the service
// is listed filtered by name and exactly one match yields its ID, zero matches
// fall back to treating the reference as an opaque ID, and multiple matches are
// an error (the caller must disambiguate with an ID).
//
// The zero-match fallback is the CLI's (README "Known limitations"), not the
// public package's: there, no match is a *resolve.NotFoundError, and this
// package turns exactly that error back into the literal reference.
package resolve

import (
	"context"
	"errors"

	"github.com/gophercloud/gophercloud/v2"

	"github.com/ftarasenko/go-openstackclient/pkg/openstack/resolve"
)

// IsUUID reports whether ref is a UUID in either accepted form: dashed
// (nova, neutron, glance, ironic) or 32 undashed hex digits (keystone).
func IsUUID(ref string) bool { return resolve.IsUUID(ref) }

// orRef applies the zero-match fallback to one strict resolution.
func orRef(ref, id string, err error) (string, error) {
	var notFound *resolve.NotFoundError
	if errors.As(err, &notFound) {
		return ref, nil
	}
	return id, err
}

// ImageID resolves a glance image name (or ID) to an image ID using the given
// image service client.
func ImageID(ctx context.Context, imageClient *gophercloud.ServiceClient, ref string) (string, error) {
	id, err := resolver().ImageID(ctx, imageClient, ref)
	return orRef(ref, id, err)
}

// NetworkID resolves a neutron network name (or ID) to a network ID using the
// given network service client.
func NetworkID(ctx context.Context, networkClient *gophercloud.ServiceClient, ref string) (string, error) {
	id, err := resolver().NetworkID(ctx, networkClient, ref)
	return orRef(ref, id, err)
}

// ProjectID resolves a keystone project name (or ID) to a project ID using the
// given identity service client.
func ProjectID(ctx context.Context, identityClient *gophercloud.ServiceClient, ref string) (string, error) {
	id, err := resolver().ProjectID(ctx, identityClient, ref)
	return orRef(ref, id, err)
}

// ServerID resolves a nova server name (or ID) to a server ID using the given
// compute service client. Nova's ?name= filter is a substring match, so only
// exact name matches count (see the public resolver).
func ServerID(ctx context.Context, computeClient *gophercloud.ServiceClient, ref string) (string, error) {
	id, err := resolver().ServerID(ctx, computeClient, ref)
	return orRef(ref, id, err)
}

// VolumeID resolves a cinder volume name (or ID) to a volume ID using the given
// volume service client.
func VolumeID(ctx context.Context, volumeClient *gophercloud.ServiceClient, ref string) (string, error) {
	id, err := resolver().VolumeID(ctx, volumeClient, ref)
	return orRef(ref, id, err)
}

// DomainID resolves a keystone domain name (or ID) to a domain ID using the
// given identity service client.
func DomainID(ctx context.Context, identityClient *gophercloud.ServiceClient, ref string) (string, error) {
	id, err := resolver().DomainID(ctx, identityClient, ref)
	return orRef(ref, id, err)
}

// ProjectIDInDomain is ProjectID narrowed to one domain (name or ID), backing
// the --project/--project-domain pair OSC uses when a project name exists in
// more than one domain. An empty domainRef behaves exactly like ProjectID.
//
// The two lookups fall back separately: a domain name that matches nothing is
// used as a domain ID, as it always was, rather than failing the project.
func ProjectIDInDomain(ctx context.Context, identityClient *gophercloud.ServiceClient, ref, domainRef string) (string, error) {
	if domainRef == "" {
		return ProjectID(ctx, identityClient, ref)
	}
	if ref == "" || IsUUID(ref) {
		return ref, nil
	}
	domainID, err := DomainID(ctx, identityClient, domainRef)
	if err != nil {
		return "", err
	}
	return ProjectIDInDomainID(ctx, identityClient, ref, domainID)
}

// ProjectIDInDomainID is ProjectIDInDomain for a domain already known by ID
// (--os-project-domain-id), so the domain needs no lookup — and must not get a
// by-name one: the default domain's ID is "default", its name "Default".
func ProjectIDInDomainID(ctx context.Context, identityClient *gophercloud.ServiceClient, ref, domainID string) (string, error) {
	id, err := resolver().ProjectIDInDomainID(ctx, identityClient, ref, domainID)
	return orRef(ref, id, err)
}

// SubnetID resolves a neutron subnet name (or ID) to a subnet ID using the given
// network service client.
func SubnetID(ctx context.Context, networkClient *gophercloud.ServiceClient, ref string) (string, error) {
	id, err := resolver().SubnetID(ctx, networkClient, ref)
	return orRef(ref, id, err)
}

// PortID resolves a neutron port name (or ID) to a port ID using the given
// network service client.
func PortID(ctx context.Context, networkClient *gophercloud.ServiceClient, ref string) (string, error) {
	id, err := resolver().PortID(ctx, networkClient, ref)
	return orRef(ref, id, err)
}

// UserID resolves a keystone user name (or ID) to a user ID using the given
// identity service client.
func UserID(ctx context.Context, identityClient *gophercloud.ServiceClient, ref string) (string, error) {
	id, err := resolver().UserID(ctx, identityClient, ref)
	return orRef(ref, id, err)
}

// UserIDInDomain is UserID narrowed to one domain (name or ID), backing the
// --user/--user-domain pair OSC uses when a user name exists in more than one
// domain. An empty domainRef behaves exactly like UserID. As with
// ProjectIDInDomain, the domain and the user fall back separately.
func UserIDInDomain(ctx context.Context, identityClient *gophercloud.ServiceClient, ref, domainRef string) (string, error) {
	if domainRef == "" {
		return UserID(ctx, identityClient, ref)
	}
	if ref == "" || IsUUID(ref) {
		return ref, nil
	}
	domainID, err := DomainID(ctx, identityClient, domainRef)
	if err != nil {
		return "", err
	}
	id, err := resolver().UserIDInDomainID(ctx, identityClient, ref, domainID)
	return orRef(ref, id, err)
}

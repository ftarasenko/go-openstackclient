package resolve

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"
)

// The package-level resolvers are the zero Resolver's: no cache.

// ImageID is [Resolver.ImageID] without a cache.
func ImageID(ctx context.Context, imageClient *gophercloud.ServiceClient, ref string) (string, error) {
	return Resolver{}.ImageID(ctx, imageClient, ref)
}

// NetworkID is [Resolver.NetworkID] without a cache.
func NetworkID(ctx context.Context, networkClient *gophercloud.ServiceClient, ref string) (string, error) {
	return Resolver{}.NetworkID(ctx, networkClient, ref)
}

// SubnetID is [Resolver.SubnetID] without a cache.
func SubnetID(ctx context.Context, networkClient *gophercloud.ServiceClient, ref string) (string, error) {
	return Resolver{}.SubnetID(ctx, networkClient, ref)
}

// PortID is [Resolver.PortID] without a cache.
func PortID(ctx context.Context, networkClient *gophercloud.ServiceClient, ref string) (string, error) {
	return Resolver{}.PortID(ctx, networkClient, ref)
}

// VolumeID is [Resolver.VolumeID] without a cache.
func VolumeID(ctx context.Context, volumeClient *gophercloud.ServiceClient, ref string) (string, error) {
	return Resolver{}.VolumeID(ctx, volumeClient, ref)
}

// ServerID is [Resolver.ServerID] without a cache.
func ServerID(ctx context.Context, computeClient *gophercloud.ServiceClient, ref string) (string, error) {
	return Resolver{}.ServerID(ctx, computeClient, ref)
}

// ProjectID is [Resolver.ProjectID] without a cache.
func ProjectID(ctx context.Context, identityClient *gophercloud.ServiceClient, ref string) (string, error) {
	return Resolver{}.ProjectID(ctx, identityClient, ref)
}

// ProjectIDInDomain is [Resolver.ProjectIDInDomain] without a cache.
func ProjectIDInDomain(ctx context.Context, identityClient *gophercloud.ServiceClient, ref, domainRef string) (string, error) {
	return Resolver{}.ProjectIDInDomain(ctx, identityClient, ref, domainRef)
}

// ProjectIDInDomainID is [Resolver.ProjectIDInDomainID] without a cache.
func ProjectIDInDomainID(ctx context.Context, identityClient *gophercloud.ServiceClient, ref, domainID string) (string, error) {
	return Resolver{}.ProjectIDInDomainID(ctx, identityClient, ref, domainID)
}

// DomainID is [Resolver.DomainID] without a cache.
func DomainID(ctx context.Context, identityClient *gophercloud.ServiceClient, ref string) (string, error) {
	return Resolver{}.DomainID(ctx, identityClient, ref)
}

// UserID is [Resolver.UserID] without a cache.
func UserID(ctx context.Context, identityClient *gophercloud.ServiceClient, ref string) (string, error) {
	return Resolver{}.UserID(ctx, identityClient, ref)
}

// UserIDInDomain is [Resolver.UserIDInDomain] without a cache.
func UserIDInDomain(ctx context.Context, identityClient *gophercloud.ServiceClient, ref, domainRef string) (string, error) {
	return Resolver{}.UserIDInDomain(ctx, identityClient, ref, domainRef)
}

// UserIDInDomainID is [Resolver.UserIDInDomainID] without a cache.
func UserIDInDomainID(ctx context.Context, identityClient *gophercloud.ServiceClient, ref, domainID string) (string, error) {
	return Resolver{}.UserIDInDomainID(ctx, identityClient, ref, domainID)
}

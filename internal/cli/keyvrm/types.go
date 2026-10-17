// Package keyvrm implements the "koc keyvrm" command surface for KeyVRM
// (Keystack Virtual Resource Manager), an in-house service registered in the
// Keystone catalog as service type "keyvrm". Auth is the standard Keystone
// token, so it reuses koc's normal auth/output/TLS layers.
//
// The typed request ("SDK") layer is the public pkg/keyvrm; this package is
// the cobra/output CLI layer over it.
package keyvrm

import keyvrmapi "github.com/ftarasenko/go-openstackclient/pkg/keyvrm"

// AppConfig and the other DTOs are the public request layer's, named here so
// the CLI layer reads as before.
type AppConfig = keyvrmapi.AppConfig

// HostAggregateConfig is keyvrmapi.HostAggregateConfig.
type HostAggregateConfig = keyvrmapi.HostAggregateConfig

// AvailabilityZone is keyvrmapi.AvailabilityZone.
type AvailabilityZone = keyvrmapi.AvailabilityZone

// Event is keyvrmapi.Event.
type Event = keyvrmapi.Event

// Recommendation is keyvrmapi.Recommendation.
type Recommendation = keyvrmapi.Recommendation

// Operation is keyvrmapi.Operation.
type Operation = keyvrmapi.Operation

// listOpts carries the shared pagination + optional filters for list calls.
type listOpts struct {
	Limit  int
	Offset int
	// filters is a set of resource-specific query params (nil-safe).
	filters map[string]string
}

// api is o as the public request layer takes it.
func (o listOpts) api() keyvrmapi.ListOpts {
	return keyvrmapi.ListOpts{Limit: o.Limit, Offset: o.Offset, Filters: o.filters}
}

package auth

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"
)

// The hooks below exist so a command's RunE — the cobra→client glue above the
// runXxx seam — can be executed in a test. Every RunE reaches a service client
// through NewServiceClient/NewServiceSession, which authenticate first, so
// without a seam that whole layer is only reachable against a real Keystone.
//
// They deliberately live in an ordinary file rather than an export_test.go:
// the callers are the *command packages'* tests, and an export_test.go is
// compiled only into its own package's test binary. Client.opts is unexported
// and dereferenced by the microversion-setting factories (Compute, Volume,
// Placement, Baremetal, KeyVRM), so a command package also cannot construct a
// usable Client itself. Both hooks are inert unless a test calls them.
//
// internal/auth is not importable outside this module, so this is not public
// API.

// SetAuthenticatorForTest makes NewServiceClient and NewServiceSession obtain
// their *Client from fn instead of authenticating. Test-only.
func (o *Options) SetAuthenticatorForTest(fn func(context.Context) (*Client, error)) {
	o.authenticate = fn
}

// NewClientForTest builds a Client that derives service clients from provider,
// with o supplying the microversion settings the factories read. Pair it with a
// ProviderClient whose EndpointLocator points at an httptest server so the real
// factory code — catalog lookup, microversion assignment, wrapService — runs
// unchanged. Test-only.
func (o *Options) NewClientForTest(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) *Client {
	return &Client{Provider: provider, Endpoint: eo, opts: o}
}

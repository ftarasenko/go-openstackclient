package loadbalancer

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/loadbalancer/v2/providers"
)

// ListProviders lists the deployment's load balancer providers.
func ListProviders(ctx context.Context, c *gophercloud.ServiceClient, opts providers.ListOptsBuilder) ([]providers.Provider, error) {
	pages, err := providers.List(c, opts).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing load balancer providers")
	}
	all, err := providers.ExtractProviders(pages)
	return all, wrap(err, "parsing provider list")
}

// ProviderCapability is one flavor capability a provider supports.
type ProviderCapability struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// ListProviderCapabilities lists a provider's flavor capabilities, the keys
// a flavor profile's flavor_data may set. A raw GET: gophercloud has no call.
func ListProviderCapabilities(ctx context.Context, c *gophercloud.ServiceClient, provider string) ([]ProviderCapability, error) {
	var body struct {
		Capabilities []ProviderCapability `json:"flavor_capabilities"`
	}
	resp, err := c.Get(ctx, c.ServiceURL("lbaas", "providers", provider, "capabilities"), &body, nil)
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if _, _, err = gophercloud.ParseResponse(resp, err); err != nil {
		return nil, wrap(err, "listing capabilities of provider %s", provider)
	}
	return body.Capabilities, nil
}

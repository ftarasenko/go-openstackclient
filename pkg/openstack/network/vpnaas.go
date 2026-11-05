package network

import (
	"context"
	"errors"
	"net/url"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/vpnaas/endpointgroups"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/vpnaas/ikepolicies"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/vpnaas/ipsecpolicies"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/vpnaas/services"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/vpnaas/siteconnections"
)

// VPNaaS: VPN services, IKE and IPsec policies, IPsec site connections and
// endpoint groups (the vpnaas and vpn-endpoint-groups extensions). The typed
// opts cannot clear or send everything neutron takes (services.CreateOpts
// always sends admin_state_up, services.UpdateOpts has no subnet_id or
// flavor_id), so a caller may pass a builder of its own. A response that
// decodes without its resource key is an error here, where gophercloud's
// Extract returns a nil pointer and no error.

var errNoResource = errors.New("the response carried no resource")

func present[T any](v *T, err error) (*T, error) {
	if err == nil && v == nil {
		err = errNoResource
	}
	return v, err
}

type networkFlavor struct {
	ID string `json:"id"`
}

// FindNetworkFlavorID resolves a neutron service flavor (the flavors
// extension, as a VPN service's flavor_id names it) name or ID to an ID.
func FindNetworkFlavorID(ctx context.Context, c *gophercloud.ServiceClient, ref string) (string, error) {
	return findID(ctx, "flavor", ref, func(ctx context.Context) ([]networkFlavor, error) {
		var body struct {
			Flavors []networkFlavor `json:"flavors"`
		}
		resp, err := c.Get(ctx, c.ServiceURL("flavors")+"?"+url.Values{"name": {ref}}.Encode(), &body, nil)
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		if _, _, err = gophercloud.ParseResponse(resp, err); err != nil {
			return nil, err
		}
		return body.Flavors, nil
	}, func(f networkFlavor) string { return f.ID })
}

// FindVPNServiceID resolves a VPN service name or ID to an ID.
func FindVPNServiceID(ctx context.Context, c *gophercloud.ServiceClient, ref string) (string, error) {
	return findID(ctx, "VPN service", ref, func(ctx context.Context) ([]services.Service, error) {
		pages, err := services.List(c, services.ListOpts{Name: ref}).AllPages(ctx)
		return extracted(pages, err, services.ExtractServices)
	}, func(v services.Service) string { return v.ID })
}

// ListVPNServices lists every VPN service matching opts.
func ListVPNServices(ctx context.Context, c *gophercloud.ServiceClient, opts services.ListOptsBuilder) ([]services.Service, error) {
	pages, err := services.List(c, opts).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing VPN services")
	}
	all, err := services.ExtractServices(pages)
	return all, wrap(err, "parsing VPN service list")
}

// GetVPNService reads a VPN service.
func GetVPNService(ctx context.Context, c *gophercloud.ServiceClient, id string) (*services.Service, error) {
	v, err := present(services.Get(ctx, c, id).Extract())
	return v, wrap(err, "getting VPN service %s", id)
}

// CreateVPNService creates a VPN service.
func CreateVPNService(ctx context.Context, c *gophercloud.ServiceClient, opts services.CreateOptsBuilder) (*services.Service, error) {
	v, err := present(services.Create(ctx, c, opts).Extract())
	return v, wrap(err, "creating VPN service")
}

// UpdateVPNService updates a VPN service.
func UpdateVPNService(ctx context.Context, c *gophercloud.ServiceClient, id string, opts services.UpdateOptsBuilder) (*services.Service, error) {
	v, err := present(services.Update(ctx, c, id, opts).Extract())
	return v, wrap(err, "updating VPN service %s", id)
}

// DeleteVPNService deletes a VPN service.
func DeleteVPNService(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(services.Delete(ctx, c, id).ExtractErr(), "deleting VPN service %s", id)
}

// FindIKEPolicyID resolves an IKE policy name or ID to an ID.
func FindIKEPolicyID(ctx context.Context, c *gophercloud.ServiceClient, ref string) (string, error) {
	return findID(ctx, "IKE policy", ref, func(ctx context.Context) ([]ikepolicies.Policy, error) {
		pages, err := ikepolicies.List(c, ikepolicies.ListOpts{Name: ref}).AllPages(ctx)
		return extracted(pages, err, ikepolicies.ExtractPolicies)
	}, func(v ikepolicies.Policy) string { return v.ID })
}

// ListIKEPolicies lists every IKE policy matching opts.
func ListIKEPolicies(ctx context.Context, c *gophercloud.ServiceClient, opts ikepolicies.ListOptsBuilder) ([]ikepolicies.Policy, error) {
	pages, err := ikepolicies.List(c, opts).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing IKE policies")
	}
	all, err := ikepolicies.ExtractPolicies(pages)
	return all, wrap(err, "parsing IKE policy list")
}

// GetIKEPolicy reads an IKE policy.
func GetIKEPolicy(ctx context.Context, c *gophercloud.ServiceClient, id string) (*ikepolicies.Policy, error) {
	v, err := present(ikepolicies.Get(ctx, c, id).Extract())
	return v, wrap(err, "getting IKE policy %s", id)
}

// CreateIKEPolicy creates an IKE policy.
func CreateIKEPolicy(ctx context.Context, c *gophercloud.ServiceClient, opts ikepolicies.CreateOptsBuilder) (*ikepolicies.Policy, error) {
	v, err := present(ikepolicies.Create(ctx, c, opts).Extract())
	return v, wrap(err, "creating IKE policy")
}

// UpdateIKEPolicy updates an IKE policy.
func UpdateIKEPolicy(ctx context.Context, c *gophercloud.ServiceClient, id string, opts ikepolicies.UpdateOptsBuilder) (*ikepolicies.Policy, error) {
	v, err := present(ikepolicies.Update(ctx, c, id, opts).Extract())
	return v, wrap(err, "updating IKE policy %s", id)
}

// DeleteIKEPolicy deletes an IKE policy.
func DeleteIKEPolicy(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(ikepolicies.Delete(ctx, c, id).ExtractErr(), "deleting IKE policy %s", id)
}

// FindIPsecPolicyID resolves an IPsec policy name or ID to an ID.
func FindIPsecPolicyID(ctx context.Context, c *gophercloud.ServiceClient, ref string) (string, error) {
	return findID(ctx, "IPsec policy", ref, func(ctx context.Context) ([]ipsecpolicies.Policy, error) {
		pages, err := ipsecpolicies.List(c, ipsecpolicies.ListOpts{Name: ref}).AllPages(ctx)
		return extracted(pages, err, ipsecpolicies.ExtractPolicies)
	}, func(v ipsecpolicies.Policy) string { return v.ID })
}

// ListIPsecPolicies lists every IPsec policy matching opts.
func ListIPsecPolicies(ctx context.Context, c *gophercloud.ServiceClient, opts ipsecpolicies.ListOptsBuilder) ([]ipsecpolicies.Policy, error) {
	pages, err := ipsecpolicies.List(c, opts).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing IPsec policies")
	}
	all, err := ipsecpolicies.ExtractPolicies(pages)
	return all, wrap(err, "parsing IPsec policy list")
}

// GetIPsecPolicy reads an IPsec policy.
func GetIPsecPolicy(ctx context.Context, c *gophercloud.ServiceClient, id string) (*ipsecpolicies.Policy, error) {
	v, err := present(ipsecpolicies.Get(ctx, c, id).Extract())
	return v, wrap(err, "getting IPsec policy %s", id)
}

// CreateIPsecPolicy creates an IPsec policy.
func CreateIPsecPolicy(ctx context.Context, c *gophercloud.ServiceClient, opts ipsecpolicies.CreateOptsBuilder) (*ipsecpolicies.Policy, error) {
	v, err := present(ipsecpolicies.Create(ctx, c, opts).Extract())
	return v, wrap(err, "creating IPsec policy")
}

// UpdateIPsecPolicy updates an IPsec policy.
func UpdateIPsecPolicy(ctx context.Context, c *gophercloud.ServiceClient, id string, opts ipsecpolicies.UpdateOptsBuilder) (*ipsecpolicies.Policy, error) {
	v, err := present(ipsecpolicies.Update(ctx, c, id, opts).Extract())
	return v, wrap(err, "updating IPsec policy %s", id)
}

// DeleteIPsecPolicy deletes an IPsec policy.
func DeleteIPsecPolicy(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(ipsecpolicies.Delete(ctx, c, id).ExtractErr(), "deleting IPsec policy %s", id)
}

// FindIPsecSiteConnectionID resolves an IPsec site connection name or ID to an ID.
func FindIPsecSiteConnectionID(ctx context.Context, c *gophercloud.ServiceClient, ref string) (string, error) {
	return findID(ctx, "IPsec site connection", ref, func(ctx context.Context) ([]siteconnections.Connection, error) {
		pages, err := siteconnections.List(c, siteconnections.ListOpts{Name: ref}).AllPages(ctx)
		return extracted(pages, err, siteconnections.ExtractConnections)
	}, func(v siteconnections.Connection) string { return v.ID })
}

// ListIPsecSiteConnections lists every IPsec site connection matching opts.
func ListIPsecSiteConnections(ctx context.Context, c *gophercloud.ServiceClient, opts siteconnections.ListOptsBuilder) ([]siteconnections.Connection, error) {
	pages, err := siteconnections.List(c, opts).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing IPsec site connections")
	}
	all, err := siteconnections.ExtractConnections(pages)
	return all, wrap(err, "parsing IPsec site connection list")
}

// GetIPsecSiteConnection reads an IPsec site connection.
func GetIPsecSiteConnection(ctx context.Context, c *gophercloud.ServiceClient, id string) (*siteconnections.Connection, error) {
	v, err := present(siteconnections.Get(ctx, c, id).Extract())
	return v, wrap(err, "getting IPsec site connection %s", id)
}

// CreateIPsecSiteConnection creates an IPsec site connection.
func CreateIPsecSiteConnection(ctx context.Context, c *gophercloud.ServiceClient, opts siteconnections.CreateOptsBuilder) (*siteconnections.Connection, error) {
	v, err := present(siteconnections.Create(ctx, c, opts).Extract())
	return v, wrap(err, "creating IPsec site connection")
}

// UpdateIPsecSiteConnection updates an IPsec site connection.
func UpdateIPsecSiteConnection(ctx context.Context, c *gophercloud.ServiceClient, id string, opts siteconnections.UpdateOptsBuilder) (*siteconnections.Connection, error) {
	v, err := present(siteconnections.Update(ctx, c, id, opts).Extract())
	return v, wrap(err, "updating IPsec site connection %s", id)
}

// DeleteIPsecSiteConnection deletes an IPsec site connection.
func DeleteIPsecSiteConnection(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(siteconnections.Delete(ctx, c, id).ExtractErr(), "deleting IPsec site connection %s", id)
}

// FindVPNEndpointGroupID resolves a VPN endpoint group name or ID to an ID.
func FindVPNEndpointGroupID(ctx context.Context, c *gophercloud.ServiceClient, ref string) (string, error) {
	return findID(ctx, "endpoint group", ref, func(ctx context.Context) ([]endpointgroups.EndpointGroup, error) {
		pages, err := endpointgroups.List(c, endpointgroups.ListOpts{Name: ref}).AllPages(ctx)
		return extracted(pages, err, endpointgroups.ExtractEndpointGroups)
	}, func(v endpointgroups.EndpointGroup) string { return v.ID })
}

// ListVPNEndpointGroups lists every endpoint group matching opts.
func ListVPNEndpointGroups(ctx context.Context, c *gophercloud.ServiceClient, opts endpointgroups.ListOptsBuilder) ([]endpointgroups.EndpointGroup, error) {
	pages, err := endpointgroups.List(c, opts).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing endpoint groups")
	}
	all, err := endpointgroups.ExtractEndpointGroups(pages)
	return all, wrap(err, "parsing endpoint group list")
}

// GetVPNEndpointGroup reads a VPN endpoint group.
func GetVPNEndpointGroup(ctx context.Context, c *gophercloud.ServiceClient, id string) (*endpointgroups.EndpointGroup, error) {
	v, err := present(endpointgroups.Get(ctx, c, id).Extract())
	return v, wrap(err, "getting endpoint group %s", id)
}

// CreateVPNEndpointGroup creates a VPN endpoint group.
func CreateVPNEndpointGroup(ctx context.Context, c *gophercloud.ServiceClient, opts endpointgroups.CreateOptsBuilder) (*endpointgroups.EndpointGroup, error) {
	v, err := present(endpointgroups.Create(ctx, c, opts).Extract())
	return v, wrap(err, "creating endpoint group")
}

// UpdateVPNEndpointGroup updates a VPN endpoint group.
func UpdateVPNEndpointGroup(ctx context.Context, c *gophercloud.ServiceClient, id string, opts endpointgroups.UpdateOptsBuilder) (*endpointgroups.EndpointGroup, error) {
	v, err := present(endpointgroups.Update(ctx, c, id, opts).Extract())
	return v, wrap(err, "updating endpoint group %s", id)
}

// DeleteVPNEndpointGroup deletes a VPN endpoint group.
func DeleteVPNEndpointGroup(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(endpointgroups.Delete(ctx, c, id).ExtractErr(), "deleting endpoint group %s", id)
}

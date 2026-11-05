package network

import (
	"context"
	"fmt"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/subnets"
)

// Subnet is gophercloud's subnet plus the attributes it does not model.
type Subnet struct {
	subnets.Subnet
	SubnetAttrs
}

// SubnetAttrs are subnet extension attributes gophercloud's subnets.Subnet
// does not model. Both are pointers, nil on a cloud without the extension.
type SubnetAttrs struct {
	// LeakRoutes is ovn-bgp's leak_routes.
	LeakRoutes *bool `json:"leak_routes"`
	// RouterExternal is the subnet-external-network extension's read-only
	// router:external: true on a subnet of an external network.
	RouterExternal *bool `json:"router:external"`
}

func extractSubnet(r interface {
	ExtractIntoStructPtr(to any, label string) error
},
) (*Subnet, error) {
	var s Subnet
	if err := r.ExtractIntoStructPtr(&s, "subnet"); err != nil {
		return nil, err
	}
	return &s, nil
}

// ListSubnets lists every subnet matching opts, following pagination.
func ListSubnets(ctx context.Context, c *gophercloud.ServiceClient, opts subnets.ListOptsBuilder) ([]Subnet, error) {
	pages, err := subnets.List(c, opts).AllPages(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing subnets: %w", err)
	}
	page, ok := pages.(subnets.SubnetPage)
	if !ok {
		return nil, fmt.Errorf("parsing subnet list: unexpected page type %T", pages)
	}
	var all []Subnet
	if err := page.ExtractIntoSlicePtr(&all, "subnets"); err != nil {
		return nil, fmt.Errorf("parsing subnet list: %w", err)
	}
	return all, nil
}

// GetSubnet reads one subnet.
func GetSubnet(ctx context.Context, c *gophercloud.ServiceClient, id string) (*Subnet, error) {
	s, err := extractSubnet(subnets.Get(ctx, c, id))
	if err != nil {
		return nil, fmt.Errorf("getting subnet %s: %w", id, err)
	}
	return s, nil
}

// CreateSubnet creates a subnet. Wrap opts with WithSubnetCreateAttrs to send
// attributes gophercloud does not model.
func CreateSubnet(ctx context.Context, c *gophercloud.ServiceClient, opts subnets.CreateOptsBuilder) (*Subnet, error) {
	s, err := extractSubnet(subnets.Create(ctx, c, opts))
	if err != nil {
		return nil, fmt.Errorf("creating subnet: %w", err)
	}
	return s, nil
}

// UpdateSubnet updates a subnet.
func UpdateSubnet(ctx context.Context, c *gophercloud.ServiceClient, id string, opts subnets.UpdateOptsBuilder) (*Subnet, error) {
	s, err := extractSubnet(subnets.Update(ctx, c, id, opts))
	if err != nil {
		return nil, fmt.Errorf("updating subnet %s: %w", id, err)
	}
	return s, nil
}

// DeleteSubnet deletes a subnet.
func DeleteSubnet(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	if err := subnets.Delete(ctx, c, id).ExtractErr(); err != nil {
		return fmt.Errorf("deleting subnet %s: %w", id, err)
	}
	return nil
}

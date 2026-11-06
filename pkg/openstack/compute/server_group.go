package compute

import (
	"context"
	"fmt"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servergroups"

	"github.com/ftarasenko/koc/pkg/openstack/paging"
)

// ServerGroupPolicyMicroversion is the microversion that replaced a group's
// policies list with one policy and added rules. Below it, create with
// Policies; from it, with Policy and Rules.
const ServerGroupPolicyMicroversion = "2.64"

// ListServerGroups lists server groups. limit > 0 caps the result; nova
// takes limit only as a page size.
func ListServerGroups(ctx context.Context, c *gophercloud.ServiceClient, opts servergroups.ListOpts, limit int) ([]servergroups.ServerGroup, error) {
	all, err := paging.Collect(ctx, servergroups.List(c, opts), limit, servergroups.ExtractServerGroups)
	if err != nil {
		return nil, fmt.Errorf("listing server groups: %w", err)
	}
	return all, nil
}

// GetServerGroup reads a server group.
func GetServerGroup(ctx context.Context, c *gophercloud.ServiceClient, id string) (*servergroups.ServerGroup, error) {
	g, err := servergroups.Get(ctx, c, id).Extract()
	if err != nil {
		return nil, fmt.Errorf("showing server group %s: %w", id, err)
	}
	return g, nil
}

// CreateServerGroup creates a server group; see [ServerGroupPolicyMicroversion]
// for which policy fields to set.
func CreateServerGroup(ctx context.Context, c *gophercloud.ServiceClient, opts servergroups.CreateOpts) (*servergroups.ServerGroup, error) {
	g, err := servergroups.Create(ctx, c, opts).Extract()
	if err != nil {
		return nil, fmt.Errorf("creating server group %q: %w", opts.Name, err)
	}
	return g, nil
}

// DeleteServerGroup deletes a server group. Its servers keep running.
func DeleteServerGroup(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(servergroups.Delete(ctx, c, id).ExtractErr(), "deleting server group %s", id)
}

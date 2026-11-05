package identity

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/groups"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/users"
)

// ListGroups lists the groups opts selects.
func ListGroups(ctx context.Context, c *gophercloud.ServiceClient, opts groups.ListOpts) ([]groups.Group, error) {
	return listAll(ctx, groups.List(c, opts), groups.ExtractGroups, "listing groups")
}

// ListUserGroups lists the groups a user belongs to. Keystone takes no domain
// filter here.
func ListUserGroups(ctx context.Context, c *gophercloud.ServiceClient, userID string) ([]groups.Group, error) {
	return listAll(ctx, users.ListGroups(c, userID), groups.ExtractGroups, "listing groups of user %s", userID)
}

// GetGroup reads one group by ID.
func GetGroup(ctx context.Context, c *gophercloud.ServiceClient, id string) (*groups.Group, error) {
	g, err := groups.Get(ctx, c, id).Extract()
	return g, wrap(err, "showing group %s", id)
}

// CreateGroup creates a group.
func CreateGroup(ctx context.Context, c *gophercloud.ServiceClient, opts groups.CreateOpts) (*groups.Group, error) {
	g, err := groups.Create(ctx, c, opts).Extract()
	return g, wrap(err, "creating group %q", opts.Name)
}

// UpdateGroup changes the fields opts sets on a group.
func UpdateGroup(ctx context.Context, c *gophercloud.ServiceClient, id string, opts groups.UpdateOpts) (*groups.Group, error) {
	g, err := groups.Update(ctx, c, id, opts).Extract()
	return g, wrap(err, "updating group %s", id)
}

// DeleteGroup deletes a group.
func DeleteGroup(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(groups.Delete(ctx, c, id).ExtractErr(), "deleting group %s", id)
}

// AddUserToGroup adds a user to a group.
func AddUserToGroup(ctx context.Context, c *gophercloud.ServiceClient, groupID, userID string) error {
	return wrap(users.AddToGroup(ctx, c, groupID, userID).ExtractErr(), "adding user %s to group %s", userID, groupID)
}

// RemoveUserFromGroup removes a user from a group.
func RemoveUserFromGroup(ctx context.Context, c *gophercloud.ServiceClient, groupID, userID string) error {
	return wrap(users.RemoveFromGroup(ctx, c, groupID, userID).ExtractErr(), "removing user %s from group %s", userID, groupID)
}

// IsUserInGroup reports whether a user belongs to a group.
func IsUserInGroup(ctx context.Context, c *gophercloud.ServiceClient, groupID, userID string) (bool, error) {
	ok, err := users.IsMemberOfGroup(ctx, c, groupID, userID).Extract()
	return ok, wrap(err, "checking whether user %s is in group %s", userID, groupID)
}

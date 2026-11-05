package identity

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/users"
)

// ListUsers lists the users opts selects.
func ListUsers(ctx context.Context, c *gophercloud.ServiceClient, opts users.ListOpts) ([]users.User, error) {
	return listAll(ctx, users.List(c, opts), users.ExtractUsers, "listing users")
}

// GetUser reads one user by ID.
func GetUser(ctx context.Context, c *gophercloud.ServiceClient, id string) (*users.User, error) {
	u, err := users.Get(ctx, c, id).Extract()
	return u, wrap(err, "showing user %s", id)
}

// CreateUser creates a user.
func CreateUser(ctx context.Context, c *gophercloud.ServiceClient, opts users.CreateOpts) (*users.User, error) {
	u, err := users.Create(ctx, c, opts).Extract()
	return u, wrap(err, "creating user %q", opts.Name)
}

// UpdateUser changes the fields opts sets on a user, its password included.
func UpdateUser(ctx context.Context, c *gophercloud.ServiceClient, id string, opts users.UpdateOpts) (*users.User, error) {
	u, err := users.Update(ctx, c, id, opts).Extract()
	return u, wrap(err, "updating user %s", id)
}

// DeleteUser deletes a user.
func DeleteUser(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(users.Delete(ctx, c, id).ExtractErr(), "deleting user %s", id)
}

// ChangeUserPassword is keystone's self-service password change: it needs
// the original password and acts only on the user the token was issued to.
// Setting another user's password is UpdateUser.
func ChangeUserPassword(ctx context.Context, c *gophercloud.ServiceClient, id string, opts users.ChangePasswordOpts) error {
	return wrap(users.ChangePassword(ctx, c, id, opts).ExtractErr(), "changing password of user %s", id)
}

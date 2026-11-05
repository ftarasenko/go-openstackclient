package identity

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/applicationcredentials"
)

// Application credentials live under the user who owns them, so every call
// takes that user's ID.

// ListApplicationCredentials lists the user's application credentials;
// opts.Name filters by name.
func ListApplicationCredentials(ctx context.Context, c *gophercloud.ServiceClient, userID string,
	opts applicationcredentials.ListOpts,
) ([]applicationcredentials.ApplicationCredential, error) {
	return listAll(ctx, applicationcredentials.List(c, userID, opts), applicationcredentials.ExtractApplicationCredentials,
		"listing application credentials of user %s", userID)
}

// GetApplicationCredential returns one of the user's application credentials.
// Keystone never returns the secret again after creation.
func GetApplicationCredential(ctx context.Context, c *gophercloud.ServiceClient, userID, id string) (*applicationcredentials.ApplicationCredential, error) {
	r, err := applicationcredentials.Get(ctx, c, userID, id).Extract()
	return r, wrap(err, "showing application credential %s", id)
}

// CreateApplicationCredential creates an application credential for the user.
// The result carries the secret; this is the only response that does.
func CreateApplicationCredential(ctx context.Context, c *gophercloud.ServiceClient, userID string,
	opts applicationcredentials.CreateOpts,
) (*applicationcredentials.ApplicationCredential, error) {
	r, err := applicationcredentials.Create(ctx, c, userID, opts).Extract()
	return r, wrap(err, "creating application credential %q", opts.Name)
}

// DeleteApplicationCredential deletes one of the user's application credentials.
func DeleteApplicationCredential(ctx context.Context, c *gophercloud.ServiceClient, userID, id string) error {
	return wrap(applicationcredentials.Delete(ctx, c, userID, id).ExtractErr(), "deleting application credential %s", id)
}

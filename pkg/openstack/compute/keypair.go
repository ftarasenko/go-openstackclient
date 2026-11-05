package compute

import (
	"context"
	"fmt"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/keypairs"
)

// Keypairs belong to a user, not a project, and are keyed by name. An admin
// reaches another user's keypair through the UserID of the opts (nova 2.10
// and later).

// ListKeypairs lists the keypairs opts selects: the caller's own, or with
// keypairs.ListOpts.UserID another user's.
func ListKeypairs(ctx context.Context, c *gophercloud.ServiceClient, opts keypairs.ListOptsBuilder) ([]keypairs.KeyPair, error) {
	pages, err := keypairs.List(c, opts).AllPages(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing keypairs: %w", err)
	}
	all, err := keypairs.ExtractKeyPairs(pages)
	if err != nil {
		return nil, fmt.Errorf("parsing keypair list: %w", err)
	}
	return all, nil
}

// GetKeypair reads one keypair by name.
func GetKeypair(ctx context.Context, c *gophercloud.ServiceClient, name string, opts keypairs.GetOptsBuilder) (*keypairs.KeyPair, error) {
	k, err := keypairs.Get(ctx, c, name, opts).Extract()
	if err != nil {
		return nil, fmt.Errorf("showing keypair %q: %w", name, err)
	}
	return k, nil
}

// CreateKeypair imports opts.PublicKey or, when it is empty, has nova generate
// a pair. A generated private key is in the result's PrivateKey and nova
// returns it only this once.
func CreateKeypair(ctx context.Context, c *gophercloud.ServiceClient, opts keypairs.CreateOpts) (*keypairs.KeyPair, error) {
	k, err := keypairs.Create(ctx, c, opts).Extract()
	if err != nil {
		return nil, fmt.Errorf("creating keypair %q: %w", opts.Name, err)
	}
	return k, nil
}

// DeleteKeypair deletes one keypair by name.
func DeleteKeypair(ctx context.Context, c *gophercloud.ServiceClient, name string, opts keypairs.DeleteOptsBuilder) error {
	if err := keypairs.Delete(ctx, c, name, opts).ExtractErr(); err != nil {
		return fmt.Errorf("deleting keypair %q: %w", name, err)
	}
	return nil
}

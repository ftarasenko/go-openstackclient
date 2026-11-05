package dns

import (
	"context"
	"net/http"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/dns/v2/quotas"
	"github.com/gophercloud/gophercloud/v2/openstack/dns/v2/tsigkeys"
)

// GetQuota reads a project's DNS quotas. Another project's needs a client
// scoped with AllProjects or SudoProjectID.
func GetQuota(ctx context.Context, c *gophercloud.ServiceClient, projectID string) (*quotas.Quota, error) {
	q, err := quotas.Get(ctx, c, projectID).Extract()
	return q, wrap(err, "showing DNS quotas for project %q", projectID)
}

// UpdateQuota sets a project's DNS quotas.
func UpdateQuota(ctx context.Context, c *gophercloud.ServiceClient, projectID string,
	opts quotas.UpdateOptsBuilder,
) (*quotas.Quota, error) {
	q, err := quotas.Update(ctx, c, projectID, opts).Extract()
	return q, wrap(err, "setting DNS quotas for project %q", projectID)
}

// ResetQuota resets a project's DNS quotas to the defaults (a raw DELETE:
// gophercloud has no call for it).
func ResetQuota(ctx context.Context, c *gophercloud.ServiceClient, projectID string) error {
	err := del(ctx, c, c.ServiceURL("quotas", projectID), http.StatusNoContent)
	return wrap(err, "resetting DNS quotas for project %q", projectID)
}

// listTSIGKeys is the unwrapped listing FindTSIGKeyID shares.
func listTSIGKeys(ctx context.Context, c *gophercloud.ServiceClient, opts tsigkeys.ListOptsBuilder) ([]tsigkeys.TSIGKey, error) {
	pages, err := tsigkeys.List(c, opts).AllPages(ctx)
	if err != nil {
		return nil, err
	}
	return tsigkeys.ExtractTSIGKeys(pages)
}

// FindTSIGKeyID resolves a TSIG key name or ID to an ID.
func FindTSIGKeyID(ctx context.Context, c *gophercloud.ServiceClient, ref string) (string, error) {
	return findID(ctx, "TSIG key", ref, func(ctx context.Context) ([]tsigkeys.TSIGKey, error) {
		return listTSIGKeys(ctx, c, tsigkeys.ListOpts{Name: ref})
	}, func(k tsigkeys.TSIGKey) string { return k.ID })
}

// ListTSIGKeys lists TSIG keys matching opts.
func ListTSIGKeys(ctx context.Context, c *gophercloud.ServiceClient, opts tsigkeys.ListOptsBuilder) ([]tsigkeys.TSIGKey, error) {
	pages, err := tsigkeys.List(c, opts).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing TSIG keys")
	}
	all, err := tsigkeys.ExtractTSIGKeys(pages)
	return all, wrap(err, "parsing TSIG key list")
}

// GetTSIGKey reads one TSIG key, secret included.
func GetTSIGKey(ctx context.Context, c *gophercloud.ServiceClient, id string) (*tsigkeys.TSIGKey, error) {
	k, err := tsigkeys.Get(ctx, c, id).Extract()
	return k, wrap(err, "showing TSIG key %q", id)
}

// CreateTSIGKey creates a TSIG key.
func CreateTSIGKey(ctx context.Context, c *gophercloud.ServiceClient, opts tsigkeys.CreateOpts) (*tsigkeys.TSIGKey, error) {
	k, err := tsigkeys.Create(ctx, c, opts).Extract()
	return k, wrap(err, "creating TSIG key %q", opts.Name)
}

// UpdateTSIGKey updates a TSIG key.
func UpdateTSIGKey(ctx context.Context, c *gophercloud.ServiceClient, id string,
	opts tsigkeys.UpdateOptsBuilder,
) (*tsigkeys.TSIGKey, error) {
	k, err := tsigkeys.Update(ctx, c, id, opts).Extract()
	return k, wrap(err, "updating TSIG key %q", id)
}

// DeleteTSIGKey deletes a TSIG key.
func DeleteTSIGKey(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(tsigkeys.Delete(ctx, c, id).ExtractErr(), "deleting TSIG key %q", id)
}

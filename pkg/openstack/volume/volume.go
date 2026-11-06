// Package volume is koc's cinder (block-storage v3) layer: volumes, their
// actions and waits, and volume attachments.
//
// Every function takes the client openstack.Session.Volume returns; its
// Microversion decides what cinder answers. Listings take a limit: cinder
// treats limit as a page size, so a positive limit caps the result and 0
// collects every page.
//
// Errors are wrapped with what the call was doing and the ID it was given
// ("deleting volume <id>: …"); gophercloud's error stays reachable through
// errors.As.
package volume

import (
	"context"
	"fmt"
	"net/url"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/volumes"

	"github.com/ftarasenko/koc/pkg/openstack/paging"
)

// wrap prefixes err with what the call was doing; nil stays nil.
func wrap(err error, format string, args ...any) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf(format+": %w", append(args, err)...)
}

// ListOpts adds to a volume listing the user_id filter
// volumes.ListOpts has no field for. It is sent only when set, and cinder
// honours it only together with AllTenants.
type ListOpts struct {
	volumes.ListOptsBuilder
	UserID string
}

// ToVolumeListQuery implements volumes.ListOptsBuilder.
func (o ListOpts) ToVolumeListQuery() (string, error) {
	q, err := o.ListOptsBuilder.ToVolumeListQuery()
	if err != nil {
		return "", err
	}
	if o.UserID == "" {
		return q, nil
	}
	parsed, err := url.Parse(q)
	if err != nil {
		return "", err
	}
	params := parsed.Query()
	params.Add("user_id", o.UserID)
	return (&url.URL{RawQuery: params.Encode()}).String(), nil
}

// ListVolumes lists the volumes opts selects, up to limit (0: all).
func ListVolumes(ctx context.Context, c *gophercloud.ServiceClient, opts volumes.ListOptsBuilder, limit int) ([]volumes.Volume, error) {
	all, err := paging.Collect(ctx, volumes.List(c, opts), limit, volumes.ExtractVolumes)
	if err != nil {
		return nil, wrap(err, "listing volumes")
	}
	return all, nil
}

// GetVolume returns the volume with the given ID.
func GetVolume(ctx context.Context, c *gophercloud.ServiceClient, id string) (*volumes.Volume, error) {
	v, err := volumes.Get(ctx, c, id).Extract()
	return v, wrap(err, "getting volume %s", id)
}

// CreateVolume creates a volume. Cinder answers at once, with the volume
// still creating; [WaitForVolumeAvailable] waits for it to settle.
func CreateVolume(ctx context.Context, c *gophercloud.ServiceClient, opts volumes.CreateOptsBuilder,
	hints volumes.SchedulerHintOptsBuilder,
) (*volumes.Volume, error) {
	v, err := volumes.Create(ctx, c, opts, hints).Extract()
	return v, wrap(err, "creating volume")
}

// UpdateVolume applies opts to the volume with the given ID. Cinder replaces
// metadata wholesale; [MetadataUpdateOpts] sends it even when it is empty.
func UpdateVolume(ctx context.Context, c *gophercloud.ServiceClient, id string, opts volumes.UpdateOptsBuilder) (*volumes.Volume, error) {
	v, err := volumes.Update(ctx, c, id, opts).Extract()
	return v, wrap(err, "updating volume %s", id)
}

// MetadataUpdateOpts replaces a volume's metadata with Metadata. Unlike
// volumes.UpdateOpts, whose Metadata is omitempty, it always sends the key,
// so an empty map clears every property.
type MetadataUpdateOpts struct {
	Metadata map[string]string
}

// ToVolumeUpdateMap implements volumes.UpdateOptsBuilder.
func (o MetadataUpdateOpts) ToVolumeUpdateMap() (map[string]any, error) {
	m := map[string]string{}
	for k, v := range o.Metadata {
		m[k] = v
	}
	return map[string]any{"volume": map[string]any{"metadata": m}}, nil
}

// DeleteVolume deletes the volume with the given ID; opts may be nil.
func DeleteVolume(ctx context.Context, c *gophercloud.ServiceClient, id string, opts volumes.DeleteOptsBuilder) error {
	return wrap(volumes.Delete(ctx, c, id, opts).ExtractErr(), "deleting volume %s", id)
}

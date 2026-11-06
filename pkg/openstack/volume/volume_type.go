package volume

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/volumetypes"

	"github.com/ftarasenko/koc/pkg/openstack/paging"
)

// ListVolumeTypes lists the volume types opts selects, every page.
func ListVolumeTypes(ctx context.Context, c *gophercloud.ServiceClient, opts volumetypes.ListOptsBuilder) ([]volumetypes.VolumeType, error) {
	all, err := paging.Collect(ctx, volumetypes.List(c, opts), 0, volumetypes.ExtractVolumeTypes)
	if err != nil {
		return nil, wrap(err, "listing volume types")
	}
	return all, nil
}

// GetVolumeType returns the volume type with the given ID.
func GetVolumeType(ctx context.Context, c *gophercloud.ServiceClient, id string) (*volumetypes.VolumeType, error) {
	t, err := volumetypes.Get(ctx, c, id).Extract()
	return t, wrap(err, "getting volume type %s", id)
}

// CreateVolumeType creates a volume type (admin).
func CreateVolumeType(ctx context.Context, c *gophercloud.ServiceClient, opts volumetypes.CreateOptsBuilder) (*volumetypes.VolumeType, error) {
	t, err := volumetypes.Create(ctx, c, opts).Extract()
	return t, wrap(err, "creating volume type")
}

// UpdateVolumeType applies opts (name, description, visibility) to the type.
func UpdateVolumeType(ctx context.Context, c *gophercloud.ServiceClient, id string, opts volumetypes.UpdateOptsBuilder) (*volumetypes.VolumeType, error) {
	t, err := volumetypes.Update(ctx, c, id, opts).Extract()
	return t, wrap(err, "updating volume type %s", id)
}

// DeleteVolumeType deletes the volume type with the given ID.
func DeleteVolumeType(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(volumetypes.Delete(ctx, c, id).ExtractErr(), "deleting volume type %s", id)
}

// SetVolumeTypeExtraSpecs creates or updates the given extra-specs and returns
// them as cinder stored them. Keys not named are left alone.
func SetVolumeTypeExtraSpecs(ctx context.Context, c *gophercloud.ServiceClient, id string, specs map[string]string) (map[string]string, error) {
	m, err := volumetypes.CreateExtraSpecs(ctx, c, id, volumetypes.ExtraSpecsOpts(specs)).Extract()
	return m, wrap(err, "setting extra-specs on volume type %s", id)
}

// DeleteVolumeTypeExtraSpec removes one extra-spec from the volume type.
func DeleteVolumeTypeExtraSpec(ctx context.Context, c *gophercloud.ServiceClient, id, key string) error {
	return wrap(volumetypes.DeleteExtraSpec(ctx, c, id, key).ExtractErr(), "removing extra-spec %q from volume type %s", key, id)
}

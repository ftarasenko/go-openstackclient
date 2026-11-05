package volume

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/qos"

	"github.com/ftarasenko/go-openstackclient/pkg/openstack/paging"
)

// ListQoS lists QoS specifications, up to limit (0: all).
func ListQoS(ctx context.Context, c *gophercloud.ServiceClient, opts qos.ListOptsBuilder, limit int) ([]qos.QoS, error) {
	all, err := paging.Collect(ctx, qos.List(c, opts), limit, qos.ExtractQoS)
	if err != nil {
		return nil, wrap(err, "listing volume QoS specifications")
	}
	return all, nil
}

// ListQoSAssociations lists the volume types the QoS specification is
// associated with.
func ListQoSAssociations(ctx context.Context, c *gophercloud.ServiceClient, id string) ([]qos.QosAssociation, error) {
	all, err := paging.Collect(ctx, qos.ListAssociations(c, id), 0, qos.ExtractAssociations)
	if err != nil {
		return nil, wrap(err, "listing the associations of volume QoS specification %s", id)
	}
	return all, nil
}

// GetQoS returns the QoS specification with the given ID.
func GetQoS(ctx context.Context, c *gophercloud.ServiceClient, id string) (*qos.QoS, error) {
	q, err := qos.Get(ctx, c, id).Extract()
	return q, wrap(err, "showing volume QoS specification %s", id)
}

// CreateQoS creates a QoS specification (admin).
func CreateQoS(ctx context.Context, c *gophercloud.ServiceClient, opts qos.CreateOpts) (*qos.QoS, error) {
	q, err := qos.Create(ctx, c, opts).Extract()
	return q, wrap(err, "creating volume QoS specification %q", opts.Name)
}

// DeleteQoS deletes the QoS specification; force deletes it while it is
// still associated.
func DeleteQoS(ctx context.Context, c *gophercloud.ServiceClient, id string, force bool) error {
	return wrap(qos.Delete(ctx, c, id, qos.DeleteOpts{Force: force}).ExtractErr(), "deleting volume QoS specification %s", id)
}

// SetQoSSpecs creates or updates the given keys and returns them as cinder
// stored them. Keys not named are left alone.
func SetQoSSpecs(ctx context.Context, c *gophercloud.ServiceClient, id string, specs map[string]string) (map[string]string, error) {
	m, err := qos.Update(ctx, c, id, qos.UpdateOpts{Specs: specs}).Extract()
	return m, wrap(err, "updating QoS specification %s", id)
}

// DeleteQoSKeys removes the given keys from the QoS specification.
func DeleteQoSKeys(ctx context.Context, c *gophercloud.ServiceClient, id string, keys []string) error {
	return wrap(qos.DeleteKeys(ctx, c, id, qos.DeleteKeysOpts(keys)).ExtractErr(), "removing properties from QoS specification %s", id)
}

// AssociateQoS associates the QoS specification with a volume type.
func AssociateQoS(ctx context.Context, c *gophercloud.ServiceClient, id, typeID string) error {
	return wrap(qos.Associate(ctx, c, id, qos.AssociateOpts{VolumeTypeID: typeID}).ExtractErr(),
		"associating QoS specification %s with volume type %s", id, typeID)
}

// DisassociateQoS removes the association with one volume type.
func DisassociateQoS(ctx context.Context, c *gophercloud.ServiceClient, id, typeID string) error {
	return wrap(qos.Disassociate(ctx, c, id, qos.DisassociateOpts{VolumeTypeID: typeID}).ExtractErr(),
		"disassociating volume type %s from QoS specification %s", typeID, id)
}

// DisassociateAllQoS removes every volume type's association.
func DisassociateAllQoS(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(qos.DisassociateAll(ctx, c, id).ExtractErr(), "disassociating every volume type from QoS specification %s", id)
}

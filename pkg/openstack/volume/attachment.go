package volume

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/attachments"

	"github.com/ftarasenko/koc/pkg/openstack/paging"
)

// Microversions the attachment API needs. Below AttachmentsMicroversion cinder
// has no attachments endpoint and answers 404.
const (
	AttachmentsMicroversion        = "3.27"
	CompleteAttachmentMicroversion = "3.44"
	// AttachModeMicroversion is the first that honours CreateOpts.Mode.
	AttachModeMicroversion = "3.54"
)

// ListAttachments lists the attachments opts selects, up to limit (0: all).
func ListAttachments(ctx context.Context, c *gophercloud.ServiceClient, opts attachments.ListOptsBuilder, limit int) ([]attachments.Attachment, error) {
	all, err := paging.Collect(ctx, attachments.List(c, opts), limit, attachments.ExtractAttachments)
	if err != nil {
		return nil, wrap(err, "listing volume attachments")
	}
	return all, nil
}

// GetAttachment returns the attachment with the given ID.
func GetAttachment(ctx context.Context, c *gophercloud.ServiceClient, id string) (*attachments.Attachment, error) {
	at, err := attachments.Get(ctx, c, id).Extract()
	return at, wrap(err, "getting volume attachment %s", id)
}

// CreateAttachment attaches a volume to a server. Without a connector it only
// reserves the volume; [UpdateAttachment] supplies the connector later.
func CreateAttachment(ctx context.Context, c *gophercloud.ServiceClient, opts attachments.CreateOpts) (*attachments.Attachment, error) {
	at, err := attachments.Create(ctx, c, opts).Extract()
	return at, wrap(err, "creating attachment for volume %s", opts.VolumeUUID)
}

// UpdateAttachment replaces the attachment's connector.
func UpdateAttachment(ctx context.Context, c *gophercloud.ServiceClient, id string, opts attachments.UpdateOptsBuilder) (*attachments.Attachment, error) {
	at, err := attachments.Update(ctx, c, id, opts).Extract()
	return at, wrap(err, "updating volume attachment %s", id)
}

// DeleteAttachment deletes the attachment with the given ID.
func DeleteAttachment(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(attachments.Delete(ctx, c, id).ExtractErr(), "deleting volume attachment %s", id)
}

// CompleteAttachment marks the attachment complete
// (CompleteAttachmentMicroversion).
func CompleteAttachment(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(attachments.Complete(ctx, c, id).ExtractErr(), "completing volume attachment %s", id)
}

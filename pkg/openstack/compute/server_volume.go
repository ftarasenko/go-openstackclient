package compute

import (
	"context"
	"crypto/rsa"
	"fmt"
	"net/http"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/volumeattach"
)

// VolumeAttachment adds to volumeattach.VolumeAttachment the IDs nova 2.89
// reports in place of id.
type VolumeAttachment struct {
	volumeattach.VolumeAttachment
	AttachmentID string `json:"attachment_id"`
	BDMID        string `json:"bdm_uuid"`
}

// ListVolumeAttachments lists a server's volume attachments.
func ListVolumeAttachments(ctx context.Context, c *gophercloud.ServiceClient, serverID string) ([]VolumeAttachment, error) {
	list, err := listVolumeAttachments(ctx, c, serverID)
	if err != nil {
		return nil, fmt.Errorf("listing volume attachments of server %s: %w", serverID, err)
	}
	return list, nil
}

func listVolumeAttachments(ctx context.Context, c *gophercloud.ServiceClient, serverID string) ([]VolumeAttachment, error) {
	pages, err := volumeattach.List(c, serverID).AllPages(ctx)
	if err != nil {
		return nil, err
	}
	page, ok := pages.(volumeattach.VolumeAttachmentPage)
	if !ok {
		return nil, fmt.Errorf("unexpected page type %T", pages)
	}
	var body struct {
		VolumeAttachments []VolumeAttachment `json:"volumeAttachments"`
	}
	if err := page.ExtractInto(&body); err != nil {
		return nil, err
	}
	return body.VolumeAttachments, nil
}

// VolumeAttachmentUpdateMicroversion is the microversion from which an
// attachment update may change delete_on_termination.
const VolumeAttachmentUpdateMicroversion = "2.85"

// SetVolumeDeleteOnTermination sets whether a server's destruction deletes
// an attached volume (needs [VolumeAttachmentUpdateMicroversion]). It names
// the attachment's own volume, so nova takes it as an update rather than the
// admin-only swap.
func SetVolumeDeleteOnTermination(ctx context.Context, c *gophercloud.ServiceClient, serverID, volumeID string, deleteOnTermination bool) error {
	body := map[string]any{"volumeAttachment": map[string]any{
		"volumeId":              volumeID,
		"delete_on_termination": deleteOnTermination,
	}}
	r, err := c.Put(ctx, c.ServiceURL("servers", serverID, "os-volume_attachments", volumeID), body, nil,
		&gophercloud.RequestOpts{OkCodes: []int{http.StatusOK, http.StatusAccepted, http.StatusNoContent}})
	if r != nil {
		_ = r.Body.Close()
	}
	return wrap(err, "updating attachment of volume %s on server %s", volumeID, serverID)
}

// GetServerPassword reads the admin password the guest agent posted, and
// decrypts it with key when key is set; nil returns it encrypted, base64.
// An empty password means the guest posted none, or it was cleared.
func GetServerPassword(ctx context.Context, c *gophercloud.ServiceClient, id string, key *rsa.PrivateKey) (string, error) {
	pw, err := servers.GetPassword(ctx, c, id).ExtractPassword(key)
	if err != nil {
		return "", fmt.Errorf("reading the admin password of server %s: %w", id, err)
	}
	return pw, nil
}

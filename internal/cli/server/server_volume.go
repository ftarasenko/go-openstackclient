package server

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/volumeattach"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/resolve"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Flag surface mirrors upstream OSC's `server volume list/set` and the
// delete-on-termination/tag flags of `server add volume`
// (openstackclient/compute/v2/server_volume.py, server.py AddServerVolume).
// UNVERIFIED against KeyStack docs (https://docs.keystack.ru/ returned HTTP 403
// at implementation time); falls back to upstream OSC semantics.

// Microversions gating the volume-attachment fields (nova
// api/openstack/compute/schemas/volumes.py and the 2.70/2.79/2.89 view
// changes). All are at or below Zed's 2.93 cap.
const (
	volumeTagMicroversion           = "2.49" // tag in the attach body
	volumeTagShownMicroversion      = "2.70" // tag in the attachment view
	volumeDeleteOnTermMicroversion  = "2.79" // delete_on_termination in attach body + view
	volumeUpdateDeleteMicroversion  = "2.85" // PUT may change delete_on_termination
	volumeAttachmentIDsMicroversion = "2.89" // attachment_id + bdm_uuid replace id
)

const (
	flagDeleteOnTermination   = "delete-on-termination"
	flagPreserveOnTermination = "preserve-on-termination"
	flagEnableDeleteOnTerm    = "enable-delete-on-termination"
	flagDisableDeleteOnTerm   = "disable-delete-on-termination"
)

// newServerVolumeCommand builds "server volume ...": the attachments as nova
// records them, as opposed to "server add/remove volume", which change them.
func newServerVolumeCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "volume", Short: "Server volume attachments"}
	cmd.AddCommand(newServerVolumeListCommand(a, o), newServerVolumeSetCommand(a, o))
	return cmd
}

// --- list -------------------------------------------------------------------

func newServerVolumeListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "list <server>",
		Short: "List the volumes attached to a server",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newComputeClient(ctx, a)
			if err != nil {
				return err
			}
			return runServerVolumeList(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

// volumeAttachment adds the 2.89 fields gophercloud's VolumeAttachment lacks.
type volumeAttachment struct {
	volumeattach.VolumeAttachment
	AttachmentID string `json:"attachment_id"`
	BDMID        string `json:"bdm_uuid"`
}

// runServerVolumeList prints a server's volume attachments with upstream's
// columns for the client's microversion: ID until 2.89 dropped it, Tag from
// 2.70, Delete On Termination? from 2.79, the attachment and BDM IDs from 2.89.
func runServerVolumeList(ctx context.Context, client *gophercloud.ServiceClient,
	o *output.Options, ref string, w io.Writer,
) error {
	id, err := resolveServerID(ctx, client, ref)
	if err != nil {
		return err
	}
	pages, err := volumeattach.List(client, id).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing volume attachments of server %q: %w", ref, err)
	}
	page, ok := pages.(volumeattach.VolumeAttachmentPage)
	if !ok {
		return fmt.Errorf("listing volume attachments of server %q: unexpected page type %T", ref, pages)
	}
	var body struct {
		VolumeAttachments []volumeAttachment `json:"volumeAttachments"`
	}
	if err := page.ExtractInto(&body); err != nil {
		return fmt.Errorf("listing volume attachments of server %q: %w", ref, err)
	}
	attachments := body.VolumeAttachments

	showID := !computeSupportsMicroversion(client, volumeAttachmentIDsMicroversion)
	showTag := computeSupportsMicroversion(client, volumeTagShownMicroversion)
	showDelete := computeSupportsMicroversion(client, volumeDeleteOnTermMicroversion)
	showIDs := !showID

	var cols []string
	if showID {
		cols = append(cols, "ID")
	}
	cols = append(cols, "Device", "Server ID", "Volume ID")
	if showTag {
		cols = append(cols, "Tag")
	}
	if showDelete {
		cols = append(cols, "Delete On Termination?")
	}
	if showIDs {
		cols = append(cols, "Attachment ID", "BlockDeviceMapping UUID")
	}

	rows := make([][]any, 0, len(attachments))
	for _, va := range attachments {
		var row []any
		if showID {
			row = append(row, va.ID)
		}
		row = append(row, va.Device, va.ServerID, va.VolumeID)
		if showTag {
			row = append(row, optString(va.Tag))
		}
		if showDelete {
			row = append(row, optBool(va.DeleteOnTermination))
		}
		if showIDs {
			row = append(row, va.AttachmentID, va.BDMID)
		}
		rows = append(rows, row)
	}
	return o.WriteList(w, output.Table{Columns: cols, Rows: rows})
}

// optString and optBool turn an absent field into an empty cell rather than a
// JSON null or a pointer.
func optString(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

func optBool(p *bool) any {
	if p == nil {
		return nil
	}
	return *p
}

// --- set --------------------------------------------------------------------

func newServerVolumeSetCommand(a *auth.Options, _ *output.Options) *cobra.Command {
	var deleteOnTerm, preserveOnTerm bool
	cmd := &cobra.Command{
		Use:   "set <server> <volume>",
		Short: "Update a volume attachment on a server (nova 2.85 or later)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			var del *bool
			switch {
			case deleteOnTerm:
				del = &deleteOnTerm
			case preserveOnTerm:
				f := false
				del = &f
			}
			ctx := cmd.Context()
			s, err := newComputeSession(ctx, a)
			if err != nil {
				return err
			}
			volumeClient, err := s.auth.Volume()
			if err != nil {
				return err
			}
			return runServerVolumeSet(ctx, s.client, volumeClient, args[0], args[1], del)
		},
	}
	cmd.Flags().BoolVar(&deleteOnTerm, flagDeleteOnTermination, false,
		"delete the volume when the server is destroyed (nova 2.85 or later)")
	cmd.Flags().BoolVar(&preserveOnTerm, flagPreserveOnTermination, false,
		"preserve the volume when the server is destroyed (nova 2.85 or later)")
	cmd.MarkFlagsMutuallyExclusive(flagDeleteOnTermination, flagPreserveOnTermination)
	return cmd
}

// runServerVolumeSet changes delete_on_termination on an existing attachment.
// With neither flag it does nothing, like upstream. A volume name resolves
// through cinder, since nova keys the attachment by volume ID.
func runServerVolumeSet(ctx context.Context, client, volumeClient *gophercloud.ServiceClient,
	ref, volumeRef string, deleteOnTermination *bool,
) error {
	if deleteOnTermination == nil {
		return nil
	}
	if !computeSupportsMicroversion(client, volumeUpdateDeleteMicroversion) {
		return fmt.Errorf("--%s/--%s require nova microversion %s or later; this client is pinned to %s",
			flagDeleteOnTermination, flagPreserveOnTermination, volumeUpdateDeleteMicroversion, client.Microversion)
	}
	id, err := resolveServerID(ctx, client, ref)
	if err != nil {
		return err
	}
	volumeID, err := resolve.VolumeID(ctx, volumeClient, volumeRef)
	if err != nil {
		return err
	}
	if err := updateVolumeAttachment(ctx, client, id, volumeID, *deleteOnTermination); err != nil {
		return fmt.Errorf("updating attachment of volume %q on server %q: %w", volumeRef, ref, err)
	}
	return nil
}

// updateVolumeAttachment is the raw PUT gophercloud's volumeattach package has
// no call for (replace it once it does). Sending the attachment's own volume
// ID makes it an update rather than a swap, which nova gates behind the
// admin-only swap policy; delete_on_termination is the one field nova 2.85+
// lets an update change. Nova answers 202 with no body.
func updateVolumeAttachment(ctx context.Context, client *gophercloud.ServiceClient,
	serverID, volumeID string, deleteOnTermination bool,
) error {
	body := map[string]any{"volumeAttachment": map[string]any{
		"volumeId":              volumeID,
		"delete_on_termination": deleteOnTermination,
	}}
	resp, err := client.Put(ctx, client.ServiceURL("servers", serverID, "os-volume_attachments", volumeID), body, nil,
		&gophercloud.RequestOpts{OkCodes: []int{http.StatusOK, http.StatusAccepted, http.StatusNoContent}})
	if resp != nil {
		_ = resp.Body.Close()
	}
	return err
}

// --- add volume body ----------------------------------------------------------

// volumeAttachBody builds the "server add volume" request body. It is built
// here rather than through volumeattach.CreateOpts because that struct tags
// DeleteOnTermination omitempty, so an explicit --disable-delete-on-termination
// would vanish from the request instead of reaching nova as false.
func volumeAttachBody(client *gophercloud.ServiceClient, volumeID, device, tag string,
	deleteOnTermination *bool,
) (map[string]any, error) {
	att := map[string]any{"volumeId": volumeID}
	if device != "" {
		att["device"] = device
	}
	if tag != "" {
		if !computeSupportsMicroversion(client, volumeTagMicroversion) {
			return nil, fmt.Errorf("--tag requires nova microversion %s or later; this client is pinned to %s",
				volumeTagMicroversion, client.Microversion)
		}
		att["tag"] = tag
	}
	if deleteOnTermination != nil {
		if !computeSupportsMicroversion(client, volumeDeleteOnTermMicroversion) {
			return nil, fmt.Errorf("--%s/--%s require nova microversion %s or later; this client is pinned to %s",
				flagEnableDeleteOnTerm, flagDisableDeleteOnTerm, volumeDeleteOnTermMicroversion, client.Microversion)
		}
		att["delete_on_termination"] = *deleteOnTermination
	}
	return map[string]any{"volumeAttachment": att}, nil
}

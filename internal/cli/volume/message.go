package volume

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strconv"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// "koc volume message list|show|delete" — cinder's user messages: the record
// of why an asynchronous operation failed ("no valid backend was found", "image
// too large"), which a volume in error state otherwise does not say. Mirrors
// upstream OSC's volume/v3/volume_message.py.
//
// gophercloud has no blockstorage/v3/messages package, so these are raw
// ServiceClient calls (the AGENTS.md fallback) against GET /messages,
// GET /messages/<id> and DELETE /messages/<id>, decoding into the DTO below.
//
// Flag names follow upstream OSC; UNVERIFIED against KeyStack (docs.keystack.ru
// returned HTTP 403), so the surface falls back to upstream.

// messageMicroversion is where cinder added the messages API. Zed caps cinder at
// 3.70, so every supported cloud has it.
const messageMicroversion = "3.3"

type volumeMessage struct {
	ID              string `json:"id"`
	EventID         string `json:"event_id"`
	ResourceType    string `json:"resource_type"`
	ResourceUUID    string `json:"resource_uuid"`
	MessageLevel    string `json:"message_level"`
	UserMessage     string `json:"user_message"`
	RequestID       string `json:"request_id"`
	CreatedAt       string `json:"created_at"`
	GuaranteedUntil string `json:"guaranteed_until"`
}

func newMessageCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "message",
		Short: "Volume failure messages (requires volume API 3.3)",
	}
	cmd.AddCommand(
		newMessageListCommand(a, o),
		newMessageShowCommand(a, o),
		newMessageDeleteCommand(a),
	)
	return cmd
}

type messageListFlags struct {
	project, projectDomain string
	marker                 string
	limit                  int
}

func newMessageListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &messageListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List volume failure messages (requires volume API 3.3)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, session, err := newVolumeSession(ctx, a)
			if err != nil {
				return err
			}
			projectID, _, err := resolveProjectAndUser(ctx, session, f.project, f.projectDomain, "", "")
			if err != nil {
				return err
			}
			return runMessageList(ctx, client, o, projectID, f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.project, "project", "", "filter results by project (name or ID) (admin only)")
	fl.StringVar(&f.projectDomain, "project-domain", "", "domain owning --project, to disambiguate the name (name or ID)")
	fl.StringVar(&f.marker, "marker", "", "list messages after this ID (pagination)")
	fl.IntVar(&f.limit, "limit", 0, "maximum number of messages to return")
	return cmd
}

func runMessageList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	projectID string, f *messageListFlags, w io.Writer,
) error {
	if err := requireVolumeMicroversion(client, messageMicroversion, "volume message list"); err != nil {
		return err
	}
	q := url.Values{}
	if projectID != "" {
		q.Set("project_id", projectID)
	}
	if f.marker != "" {
		q.Set("marker", f.marker)
	}
	if f.limit > 0 {
		q.Set("limit", strconv.Itoa(f.limit))
	}
	next := client.ServiceURL("messages")
	if encoded := q.Encode(); encoded != "" {
		next += "?" + encoded
	}
	var all []volumeMessage
	for next != "" {
		var resp struct {
			Messages []volumeMessage `json:"messages"`
			Links    []struct {
				Href string `json:"href"`
				Rel  string `json:"rel"`
			} `json:"messages_links"`
		}
		r, err := client.Get(ctx, next, &resp, &gophercloud.RequestOpts{OkCodes: []int{200}})
		if r != nil {
			_ = r.Body.Close()
		}
		if err != nil {
			return fmt.Errorf("listing volume messages: %w", err)
		}
		all = append(all, resp.Messages...)
		next = ""
		// --limit is a hard cap, as upstream's single page is.
		if f.limit > 0 {
			if len(all) > f.limit {
				all = all[:f.limit]
			}
			break
		}
		for _, l := range resp.Links {
			if l.Rel == "next" {
				next = l.Href
			}
		}
	}
	t := output.Table{
		Columns: []string{"ID", "Event ID", "Resource Type", "Resource UUID", "Message Level",
			"User Message", "Request ID", "Created At", "Guaranteed Until"},
		Rows: make([][]any, 0, len(all)),
	}
	for _, m := range all {
		t.Rows = append(t.Rows, []any{m.ID, m.EventID, m.ResourceType, m.ResourceUUID, m.MessageLevel,
			m.UserMessage, m.RequestID, m.CreatedAt, m.GuaranteedUntil})
	}
	return o.WriteList(w, t)
}

func newMessageShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <message-id>",
		Short: "Show a volume failure message (requires volume API 3.3)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newVolumeClient(ctx, a)
			if err != nil {
				return err
			}
			return runMessageShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runMessageShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, id string, w io.Writer) error {
	if err := requireVolumeMicroversion(client, messageMicroversion, "volume message show"); err != nil {
		return err
	}
	var resp struct {
		Message volumeMessage `json:"message"`
	}
	r, err := client.Get(ctx, client.ServiceURL("messages", id), &resp, &gophercloud.RequestOpts{OkCodes: []int{200}})
	if r != nil {
		_ = r.Body.Close()
	}
	if err != nil {
		return fmt.Errorf("showing volume message %s: %w", id, err)
	}
	m := resp.Message
	// Upstream's field order: alphabetical by column header.
	return o.WriteSingle(w,
		[]string{"created_at", "event_id", "guaranteed_until", "id", "message_level", "request_id",
			"resource_type", "resource_uuid", "user_message"},
		[]any{m.CreatedAt, m.EventID, m.GuaranteedUntil, m.ID, m.MessageLevel, m.RequestID,
			m.ResourceType, m.ResourceUUID, m.UserMessage})
}

func newMessageDeleteCommand(a *auth.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <message-id> [<message-id>...]",
		Short: "Delete volume failure message(s) (requires volume API 3.3)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			client, err := newVolumeClient(ctx, a)
			if err != nil {
				return err
			}
			return runMessageDelete(ctx, client, args, cmd.OutOrStdout())
		},
	}
}

func runMessageDelete(ctx context.Context, client *gophercloud.ServiceClient, ids []string, w io.Writer) error {
	if err := requireVolumeMicroversion(client, messageMicroversion, "volume message delete"); err != nil {
		return err
	}
	return batchdelete.Each(ids, func(id string) error {
		r, err := client.Delete(ctx, client.ServiceURL("messages", id), &gophercloud.RequestOpts{OkCodes: []int{204}})
		if r != nil {
			_ = r.Body.Close()
		}
		if err != nil {
			return fmt.Errorf("deleting volume message %s: %w", id, err)
		}
		_, err = fmt.Fprintf(w, "Deleted volume message %s\n", id)
		return err
	})
}

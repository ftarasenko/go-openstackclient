package image

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/members"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/resolve"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// newImageAddCommand builds "image add ..." (currently just "project").
func newImageAddCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Add a relationship to an image",
	}
	cmd.AddCommand(newImageAddProjectCommand(a, o))
	return cmd
}

// newImageRemoveCommand builds "image remove ..." (currently just "project").
func newImageRemoveCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remove",
		Short: "Remove a relationship from an image",
	}
	cmd.AddCommand(newImageRemoveProjectCommand(a, o))
	return cmd
}

// newImageAddProjectCommand builds "image add project <image> <project>".
//
// The <image> and <project> references may be names or IDs; the image is
// resolved via glance and the project name→ID via the identity service.
func newImageAddProjectCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project <image> <project>",
		Short: "Share an image with a project (add an image member)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, session, err := newImageSession(ctx, a)
			if err != nil {
				return err
			}
			id, err := resolveImageID(ctx, client, args[0])
			if err != nil {
				return err
			}
			projectID, err := resolveProjectRef(ctx, session, args[1])
			if err != nil {
				return err
			}
			return runImageAddProject(ctx, client, o, id, projectID, cmd.OutOrStdout())
		},
	}
	return cmd
}

func runImageAddProject(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, imageID, projectID string, w io.Writer) error {
	m, err := members.Create(ctx, client, imageID, projectID).Extract()
	if err != nil {
		return fmt.Errorf("adding project %s to image %s: %w", projectID, imageID, err)
	}
	fields, values := memberFields(m)
	return o.WriteSingle(w, fields, values)
}

// newImageRemoveProjectCommand builds "image remove project <image> <project>".
func newImageRemoveProjectCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project <image> <project>",
		Short: "Stop sharing an image with a project (delete an image member)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, session, err := newImageSession(ctx, a)
			if err != nil {
				return err
			}
			id, err := resolveImageID(ctx, client, args[0])
			if err != nil {
				return err
			}
			projectID, err := resolveProjectRef(ctx, session, args[1])
			if err != nil {
				return err
			}
			return runImageRemoveProject(ctx, client, id, projectID, cmd.OutOrStdout())
		},
	}
	return cmd
}

func runImageRemoveProject(ctx context.Context, client *gophercloud.ServiceClient, imageID, projectID string, w io.Writer) error {
	if err := members.Delete(ctx, client, imageID, projectID).ExtractErr(); err != nil {
		return fmt.Errorf("removing project %s from image %s: %w", projectID, imageID, err)
	}
	if _, err := fmt.Fprintf(w, "Removed project %s from image %s\n", projectID, imageID); err != nil {
		return err
	}
	return nil
}

// resolveProjectRef turns a project name or ID into a project ID via the
// identity service derived from the shared session.
func resolveProjectRef(ctx context.Context, session *auth.Client, ref string) (string, error) {
	if ref == "" || resolve.IsUUID(ref) {
		return ref, nil
	}
	identityClient, err := session.Identity()
	if err != nil {
		return "", err
	}
	return resolve.ProjectID(ctx, identityClient, ref)
}

func memberFields(m *members.Member) ([]string, []any) {
	fields := []string{"image_id", "member_id", "status", "schema", "created_at", "updated_at"}
	values := []any{m.ImageID, m.MemberID, m.Status, m.Schema, m.CreatedAt, m.UpdatedAt}
	return fields, values
}

// newImageMemberCommand builds the "image member ..." group (list + set),
// mirroring the upstream "openstack image member" sharing workflow.
func newImageMemberCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "member",
		Short: "Manage image members (sharing)",
	}
	cmd.AddCommand(newImageMemberListCommand(a, o))
	cmd.AddCommand(newImageMemberSetCommand(a, o))
	return cmd
}

// newImageMemberListCommand builds "image member list <image>".
func newImageMemberListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list <image>",
		Short: "List the members an image is shared with",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newImageClient(ctx, a)
			if err != nil {
				return err
			}
			id, err := resolveImageID(ctx, client, args[0])
			if err != nil {
				return err
			}
			return runImageMemberList(ctx, client, o, id, cmd.OutOrStdout())
		},
	}
	return cmd
}

func runImageMemberList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, imageID string, w io.Writer) error {
	pages, err := members.List(client, imageID).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing members of image %s: %w", imageID, err)
	}
	all, err := members.ExtractMembers(pages)
	if err != nil {
		return fmt.Errorf("parsing member list for image %s: %w", imageID, err)
	}
	t := output.Table{
		Columns: []string{"Image ID", "Member ID", "Status"},
		Rows:    make([][]any, 0, len(all)),
	}
	for _, m := range all {
		t.Rows = append(t.Rows, []any{m.ImageID, m.MemberID, m.Status})
	}
	return o.WriteList(w, t)
}

// imageMemberSetFlags holds the mutually exclusive status flags for
// "image member set". They mirror upstream OSC (`openstack image set
// --accept/--reject/--pending`).
type imageMemberSetFlags struct {
	accept  bool
	reject  bool
	pending bool
}

func (f *imageMemberSetFlags) status() (string, error) {
	switch {
	case f.accept:
		return "accepted", nil
	case f.reject:
		return "rejected", nil
	case f.pending:
		return "pending", nil
	default:
		return "", fmt.Errorf("image member set requires one of --accept, --reject or --pending")
	}
}

// newImageMemberSetCommand builds "image member set <image> <member>".
//
// The acting member (the shared-with project) accepts, rejects, or resets its
// membership status. <member> is the project ID/name the image is shared with.
func newImageMemberSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &imageMemberSetFlags{}
	cmd := &cobra.Command{
		Use:   "set <image> <member>",
		Short: "Set the status of an image member (accept/reject/pending)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			status, err := f.status()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			client, session, err := newImageSession(ctx, a)
			if err != nil {
				return err
			}
			id, err := resolveImageID(ctx, client, args[0])
			if err != nil {
				return err
			}
			memberID, err := resolveProjectRef(ctx, session, args[1])
			if err != nil {
				return err
			}
			return runImageMemberSet(ctx, client, o, id, memberID, status, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&f.accept, "accept", false, "accept the image membership")
	fl.BoolVar(&f.reject, "reject", false, "reject the image membership")
	fl.BoolVar(&f.pending, "pending", false, "reset the image membership to pending")
	cmd.MarkFlagsMutuallyExclusive("accept", "reject", "pending")
	return cmd
}

func runImageMemberSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, imageID, memberID, status string, w io.Writer) error {
	m, err := members.Update(ctx, client, imageID, memberID, members.UpdateOpts{Status: status}).Extract()
	if err != nil {
		return fmt.Errorf("setting member %s status on image %s: %w", memberID, imageID, err)
	}
	fields, values := memberFields(m)
	return o.WriteSingle(w, fields, values)
}

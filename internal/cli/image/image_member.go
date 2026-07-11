package image

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/members"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
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
// <project> is used verbatim as the member (project) ID; project name→ID
// resolution would require the identity service and is not performed here.
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
			client, err := newImageClient(ctx, a)
			if err != nil {
				return err
			}
			id, err := resolveImageID(ctx, client, args[0])
			if err != nil {
				return err
			}
			return runImageAddProject(ctx, client, o, id, args[1], cmd.OutOrStdout())
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
			client, err := newImageClient(ctx, a)
			if err != nil {
				return err
			}
			id, err := resolveImageID(ctx, client, args[0])
			if err != nil {
				return err
			}
			return runImageRemoveProject(ctx, client, id, args[1], cmd.OutOrStdout())
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

func memberFields(m *members.Member) ([]string, []any) {
	fields := []string{"image_id", "member_id", "status", "schema", "created_at", "updated_at"}
	values := []any{m.ImageID, m.MemberID, m.Status, m.Schema, m.CreatedAt, m.UpdatedAt}
	return fields, values
}

package image

import (
	"context"
	"fmt"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/members"
)

// Image members are the projects a shared image is shared with. Each takes
// the project by ID; resolving a project name is keystone's business (see
// package resolve).

// AddMember shares an image with a project.
func AddMember(ctx context.Context, c *gophercloud.ServiceClient, imageID, projectID string) (*members.Member, error) {
	m, err := members.Create(ctx, c, imageID, projectID).Extract()
	if err != nil {
		return nil, fmt.Errorf("adding project %s to image %s: %w", projectID, imageID, err)
	}
	return m, nil
}

// RemoveMember stops sharing an image with a project.
func RemoveMember(ctx context.Context, c *gophercloud.ServiceClient, imageID, projectID string) error {
	if err := members.Delete(ctx, c, imageID, projectID).ExtractErr(); err != nil {
		return fmt.Errorf("removing project %s from image %s: %w", projectID, imageID, err)
	}
	return nil
}

// ListMembers lists the projects an image is shared with.
func ListMembers(ctx context.Context, c *gophercloud.ServiceClient, imageID string) ([]members.Member, error) {
	pages, err := members.List(c, imageID).AllPages(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing members of image %s: %w", imageID, err)
	}
	all, err := members.ExtractMembers(pages)
	if err != nil {
		return nil, fmt.Errorf("parsing member list for image %s: %w", imageID, err)
	}
	return all, nil
}

// UpdateMember sets a member's status — "accepted", "rejected" or "pending";
// it is the member project, not the owner, that accepts a shared image.
func UpdateMember(ctx context.Context, c *gophercloud.ServiceClient, imageID, memberID, status string) (*members.Member, error) {
	m, err := members.Update(ctx, c, imageID, memberID, members.UpdateOpts{Status: status}).Extract()
	if err != nil {
		return nil, fmt.Errorf("setting member %s status on image %s: %w", memberID, imageID, err)
	}
	return m, nil
}

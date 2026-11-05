package identity

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/projects"
)

// ListProjects lists the projects opts selects.
func ListProjects(ctx context.Context, c *gophercloud.ServiceClient, opts projects.ListOpts) ([]projects.Project, error) {
	return listAll(ctx, projects.List(c, opts), projects.ExtractProjects, "listing projects")
}

// GetProject reads one project by ID.
func GetProject(ctx context.Context, c *gophercloud.ServiceClient, id string) (*projects.Project, error) {
	p, err := projects.Get(ctx, c, id).Extract()
	return p, wrap(err, "showing project %s", id)
}

// CreateProject creates a project.
func CreateProject(ctx context.Context, c *gophercloud.ServiceClient, opts projects.CreateOpts) (*projects.Project, error) {
	p, err := projects.Create(ctx, c, opts).Extract()
	return p, wrap(err, "creating project %q", opts.Name)
}

// UpdateProject changes the fields opts sets on a project.
func UpdateProject(ctx context.Context, c *gophercloud.ServiceClient, id string, opts projects.UpdateOpts) (*projects.Project, error) {
	p, err := projects.Update(ctx, c, id, opts).Extract()
	return p, wrap(err, "updating project %s", id)
}

// DeleteProject deletes a project.
func DeleteProject(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(projects.Delete(ctx, c, id).ExtractErr(), "deleting project %s", id)
}

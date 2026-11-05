package keymanager

import (
	"context"
	"fmt"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/keymanager/v1/containers"
)

// ContainerListOpts filters a container listing. gophercloud's ListOpts has
// no type filter.
type ContainerListOpts struct {
	Limit  int    `q:"limit"`
	Offset int    `q:"offset"`
	Name   string `q:"name"`
	Type   string `q:"type"`
}

// ToContainerListQuery implements containers.ListOptsBuilder.
func (opts ContainerListOpts) ToContainerListQuery() (string, error) {
	q, err := gophercloud.BuildQueryString(opts)
	if err != nil {
		return "", err
	}
	return q.String(), nil
}

// ListContainers lists one window of containers: opts' limit entries from its
// offset.
func ListContainers(ctx context.Context, c *gophercloud.ServiceClient, opts containers.ListOptsBuilder) ([]containers.Container, error) {
	page, err := firstPage(ctx, containers.List(c, opts))
	if err != nil || page == nil {
		return nil, wrap(err, "listing containers")
	}
	all, err := containers.ExtractContainers(page)
	return all, wrap(err, "parsing container list")
}

// GetContainer returns a container.
func GetContainer(ctx context.Context, c *gophercloud.ServiceClient, id string) (*containers.Container, error) {
	r, err := containers.Get(ctx, c, id).Extract()
	return r, wrap(err, "getting container %s", id)
}

// CreateContainer stores a container and returns its UUID.
func CreateContainer(ctx context.Context, c *gophercloud.ServiceClient, opts containers.CreateOptsBuilder) (string, error) {
	created, err := containers.Create(ctx, c, opts).Extract()
	if err != nil {
		return "", wrap(err, "creating container")
	}
	id, err := RefUUID(created.ContainerRef, "Container")
	if err != nil {
		return "", fmt.Errorf("creating container: barbican answered container_ref %q: %w", created.ContainerRef, err)
	}
	return id, nil
}

// DeleteContainer deletes a container; the secrets in it stay.
func DeleteContainer(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(containers.Delete(ctx, c, id).ExtractErr(), "deleting container %s", id)
}

// Package image is koc's glance (image v2) layer: image records, their data,
// members, the interoperable import API, tasks and stores.
//
// Every function takes the image client openstack.Session.Image returns and
// speaks gophercloud's types. Where gophercloud has no call — activate and
// deactivate, the stores document — or gets a detail wrong — the multi-store
// keys of an import, the task type filter, the image-size header glance uses
// to size its store — the function here is the fix, so an importer and the koc
// CLI send the same requests.
//
// Errors are wrapped with what the call was doing and the image ID it was
// given ("getting image <id>: …"); gophercloud's error stays reachable through
// errors.As, so gophercloud.ResponseCodeIs keeps working on it.
package image

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/images"
)

// VisibilityAll is glance's fifth visibility *filter*: it lifts the visibility
// restriction a listing otherwise applies, so one list spans public, private,
// shared and community images (without it, glance hides other projects'
// community images). gophercloud has no constant for it because it is a
// query value only, never an image's own visibility.
const VisibilityAll images.ImageVisibility = "all"

// ListImages lists every image opts selects, following glance's pages.
func ListImages(ctx context.Context, c *gophercloud.ServiceClient, opts images.ListOptsBuilder) ([]images.Image, error) {
	pages, err := images.List(c, opts).AllPages(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing images: %w", err)
	}
	all, err := images.ExtractImages(pages)
	if err != nil {
		return nil, fmt.Errorf("parsing image list: %w", err)
	}
	return all, nil
}

// FindByName lists the images named exactly name. Glance's name filter has no
// wildcard, so this is the whole of a by-name lookup; whether no match, or
// several, is an error is the caller's policy (package resolve applies the
// strict one).
func FindByName(ctx context.Context, c *gophercloud.ServiceClient, name string) ([]images.Image, error) {
	pages, err := images.List(c, images.ListOpts{Name: name}).AllPages(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing images named %q: %w", name, err)
	}
	all, err := images.ExtractImages(pages)
	if err != nil {
		return nil, fmt.Errorf("parsing image list for %q: %w", name, err)
	}
	return all, nil
}

// GetImage reads one image.
func GetImage(ctx context.Context, c *gophercloud.ServiceClient, id string) (*images.Image, error) {
	img, err := images.Get(ctx, c, id).Extract()
	if err != nil {
		return nil, fmt.Errorf("getting image %s: %w", id, err)
	}
	return img, nil
}

// DeleteImage deletes one image.
func DeleteImage(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	if err := images.Delete(ctx, c, id).ExtractErr(); err != nil {
		return fmt.Errorf("deleting image %s: %w", id, err)
	}
	return nil
}

// UpdateImage applies a JSON-patch update and returns the updated image. A
// property whose name may contain '/' or '~' must be passed through
// [EscapePropertyName] first.
func UpdateImage(ctx context.Context, c *gophercloud.ServiceClient, id string, ops images.UpdateOpts) (*images.Image, error) {
	img, err := images.Update(ctx, c, id, ops).Extract()
	if err != nil {
		return nil, fmt.Errorf("updating image %s: %w", id, err)
	}
	return img, nil
}

// EscapePropertyName escapes a property name for images.UpdateImageProperty,
// per RFC 6901: '~' becomes "~0" and '/' becomes "~1". Glance patches a
// property at the JSON pointer "/<name>", and gophercloud builds that pointer
// without escaping, so an unescaped "a/b" would patch the wrong path.
func EscapePropertyName(name string) string {
	name = strings.ReplaceAll(name, "~", "~0")
	return strings.ReplaceAll(name, "/", "~1")
}

// ActivateImage reactivates a deactivated image. gophercloud has no call for
// glance's actions endpoint, so this is a raw POST.
func ActivateImage(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return imageAction(ctx, c, id, "reactivate")
}

// DeactivateImage deactivates an image: its data can no longer be downloaded
// or booted from, by anyone but an admin, until it is reactivated.
func DeactivateImage(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return imageAction(ctx, c, id, "deactivate")
}

// imageAction POSTs /v2/images/{id}/actions/{action}, which answers 204.
func imageAction(ctx context.Context, c *gophercloud.ServiceClient, id, action string) error {
	resp, err := c.Post(ctx, c.ServiceURL("images", id, "actions", action), nil, nil,
		&gophercloud.RequestOpts{OkCodes: []int{http.StatusNoContent}})
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if _, _, err = gophercloud.ParseResponse(resp, err); err != nil {
		return fmt.Errorf("%s image %s: %w", action, id, err)
	}
	return nil
}

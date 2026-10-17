package image

import (
	"context"
	"fmt"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/imageimport"
)

// ImportOpts is an import request: gophercloud's CreateOpts (the method and,
// for web-download, its URI) plus glance's multi-store keys, which gophercloud
// does not model. Stores and AllStores sit next to "method" at the top level
// of the body (openstacksdk image/v2/image.py import_image); both are omitted
// unless set, so the default request is the one CreateOpts alone builds.
type ImportOpts struct {
	imageimport.CreateOpts
	// Stores names the backend stores to import into.
	Stores []string
	// AllStores imports into every store glance has.
	AllStores bool
}

// ToImportCreateMap builds the request body; it implements
// imageimport.CreateOptsBuilder.
func (o ImportOpts) ToImportCreateMap() (map[string]any, error) {
	b, err := o.CreateOpts.ToImportCreateMap()
	if err != nil {
		return nil, err
	}
	if o.AllStores {
		b["all_stores"] = true
	}
	if len(o.Stores) > 0 {
		b["stores"] = o.Stores
	}
	return b, nil
}

// ImportImage starts an import into an existing image record. Glance answers
// 202 and imports asynchronously: the image's status goes importing → active,
// which GetImage observes.
func ImportImage(ctx context.Context, c *gophercloud.ServiceClient, id string, opts ImportOpts) error {
	if err := imageimport.Create(ctx, c, id, opts).ExtractErr(); err != nil {
		return fmt.Errorf("importing image %s: %w", id, err)
	}
	return nil
}

// GetImportInfo reads the import discovery document: the import methods this
// endpoint enables.
func GetImportInfo(ctx context.Context, c *gophercloud.ServiceClient) (*imageimport.ImportInfo, error) {
	info, err := imageimport.Get(ctx, c).Extract()
	if err != nil {
		return nil, fmt.Errorf("getting image import info: %w", err)
	}
	return info, nil
}

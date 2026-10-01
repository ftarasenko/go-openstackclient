package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/images"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
)

// imageNamer maps image IDs to names. A nil imageNamer, or an ID missing from
// its answer, leaves the image unnamed: names are a convenience, and upstream
// ignores a failed lookup too.
type imageNamer func(ctx context.Context, ids []string) map[string]string

// imageNameChunk bounds the id=in: list per glance request, so a fleet-wide
// listing does not build a URL some proxy refuses.
const imageNameChunk = 50

// glanceImageNamer looks names up in glance, deriving the image client only
// when there is something to name.
func glanceImageNamer(session *auth.Client) imageNamer {
	return func(ctx context.Context, ids []string) map[string]string {
		if len(ids) == 0 {
			return nil
		}
		client, err := session.Image()
		if err != nil {
			return nil
		}
		return lookupImageNames(ctx, client, ids)
	}
}

// lookupImageNames resolves ids with glance's id=in: filter, one request per
// chunk, as upstream's `server list` does.
func lookupImageNames(ctx context.Context, client *gophercloud.ServiceClient, ids []string) map[string]string {
	names := make(map[string]string, len(ids))
	for start := 0; start < len(ids); start += imageNameChunk {
		chunk := ids[start:min(start+imageNameChunk, len(ids))]
		pages, err := images.List(client, images.ListOpts{ID: "in:" + strings.Join(chunk, ",")}).AllPages(ctx)
		if err != nil {
			continue
		}
		list, err := images.ExtractImages(pages)
		if err != nil {
			continue
		}
		for _, img := range list {
			names[img.ID] = img.Name
		}
	}
	return names
}

// nameServerImage rewrites a raw server's image as upstream's server show
// prints it: "<name> (<id>)", or the ID alone when the name is unknown. A
// volume-booted server's empty image is left for humanizeServerValue.
func nameServerImage(ctx context.Context, server map[string]any, lookup imageNamer) {
	img, ok := server["image"].(map[string]any)
	if !ok {
		return
	}
	id, ok := img["id"].(string)
	if !ok || id == "" {
		return
	}
	if lookup != nil {
		if name, ok := lookup(ctx, []string{id})[id]; ok {
			server["image"] = fmt.Sprintf("%s (%s)", name, id)
			return
		}
	}
	server["image"] = id
}

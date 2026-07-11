package image

import (
	"context"
	"fmt"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/images"
)

// parseKeyVal splits a "key=value" string into its two halves. The value may
// itself contain '=' signs; only the first is treated as the separator.
func parseKeyVal(s string) (string, string, error) {
	i := strings.Index(s, "=")
	if i < 0 {
		return "", "", fmt.Errorf("expected key=value, got %q", s)
	}
	key := strings.TrimSpace(s[:i])
	if key == "" {
		return "", "", fmt.Errorf("empty key in %q", s)
	}
	return key, s[i+1:], nil
}

// parseKeyValMap turns a slice of "key=value" flag values into a string map.
func parseKeyValMap(pairs []string) (map[string]string, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	m := make(map[string]string, len(pairs))
	for _, p := range pairs {
		k, v, err := parseKeyVal(p)
		if err != nil {
			return nil, err
		}
		m[k] = v
	}
	return m, nil
}

// resolveImageID turns a user-supplied image reference (name or ID) into an image
// ID. It first tries an exact name lookup via a filtered list; if exactly one
// image matches, its ID is returned. Otherwise the reference is assumed to
// already be an ID and passed through unchanged (glance Get accepts only IDs).
func resolveImageID(ctx context.Context, client *gophercloud.ServiceClient, ref string) (string, error) {
	pages, err := images.List(client, images.ListOpts{Name: ref}).AllPages(ctx)
	if err != nil {
		// Name lookup failed; fall back to treating the reference as an ID.
		return ref, nil
	}
	all, err := images.ExtractImages(pages)
	if err != nil {
		return ref, nil
	}
	switch len(all) {
	case 0:
		return ref, nil
	case 1:
		return all[0].ID, nil
	default:
		return "", fmt.Errorf("multiple images named %q; specify an ID instead", ref)
	}
}

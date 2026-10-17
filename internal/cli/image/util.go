package image

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/gophercloud/gophercloud/v2"

	"github.com/ftarasenko/go-openstackclient/internal/cli/resolve"
	imageapi "github.com/ftarasenko/go-openstackclient/pkg/openstack/image"
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
// ID. A UUID reference is passed through untouched. Otherwise it tries an exact
// name lookup via a filtered list; if exactly one image matches, its ID is
// returned. Only a genuine zero-match falls back to treating the reference as a
// literal ID (documented trade-off) — real List/Extract errors are propagated so
// transient glance failures are not silently masked on write paths.
func resolveImageID(ctx context.Context, client *gophercloud.ServiceClient, ref string) (string, error) {
	if resolve.IsUUID(ref) {
		return ref, nil
	}
	all, err := imageapi.FindByName(ctx, client, ref)
	if err != nil {
		return "", err
	}
	switch len(all) {
	case 0:
		// No image by that name; fall back to treating the reference as an ID.
		return ref, nil
	case 1:
		return all[0].ID, nil
	default:
		return "", fmt.Errorf("multiple images named %q; specify an ID instead", ref)
	}
}

// relabel rewords an error from pkg/openstack/image the way this command has
// always printed it. The public functions name the image by the ID they were
// given; a few verbs name it by the reference the operator typed, so they
// keep their own wording around the same underlying error.
func relabel(err error, format string, args ...any) error {
	return fmt.Errorf(format+": %w", append(args, errors.Unwrap(err))...)
}

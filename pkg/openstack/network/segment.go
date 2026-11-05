package network

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/segments"
)

// The segment calls need the segment extension; without it neutron answers
// 404, as it does for a missing segment.

// ListSegments lists every network segment matching opts.
func ListSegments(ctx context.Context, c *gophercloud.ServiceClient, opts segments.ListOptsBuilder) ([]segments.Segment, error) {
	pages, err := segments.List(c, opts).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing network segments")
	}
	all, err := segments.ExtractSegments(pages)
	return all, wrap(err, "parsing the network segment list")
}

// GetSegment reads one network segment.
func GetSegment(ctx context.Context, c *gophercloud.ServiceClient, id string) (*segments.Segment, error) {
	s, err := segments.Get(ctx, c, id).Extract()
	return s, wrap(err, "showing network segment %s", id)
}

// CreateSegment creates a network segment.
func CreateSegment(ctx context.Context, c *gophercloud.ServiceClient, opts segments.CreateOptsBuilder) (*segments.Segment, error) {
	s, err := segments.Create(ctx, c, opts).Extract()
	return s, wrap(err, "creating network segment")
}

// UpdateSegment updates a network segment.
func UpdateSegment(ctx context.Context, c *gophercloud.ServiceClient, id string, opts segments.UpdateOptsBuilder) (*segments.Segment, error) {
	s, err := segments.Update(ctx, c, id, opts).Extract()
	return s, wrap(err, "updating network segment %s", id)
}

// DeleteSegment deletes a network segment.
func DeleteSegment(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(segments.Delete(ctx, c, id).ExtractErr(), "deleting network segment %s", id)
}

// FindSegmentID resolves a network segment name or ID to an ID.
func FindSegmentID(ctx context.Context, c *gophercloud.ServiceClient, ref string) (string, error) {
	return findID(ctx, "network segment", ref, func(ctx context.Context) ([]segments.Segment, error) {
		pages, err := segments.List(c, segments.ListOpts{Name: ref}).AllPages(ctx)
		return extracted(pages, err, segments.ExtractSegments)
	}, func(s segments.Segment) string { return s.ID })
}

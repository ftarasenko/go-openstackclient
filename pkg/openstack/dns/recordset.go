package dns

import (
	"context"
	"fmt"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/dns/v2/recordsets"
	"github.com/gophercloud/gophercloud/v2/pagination"

	"github.com/ftarasenko/go-openstackclient/pkg/openstack/paging"
	"github.com/ftarasenko/go-openstackclient/pkg/openstack/resolve"
)

// RecordSet is a recordset whose TTL may be null. Designate sends null when
// the recordset inherits the zone's TTL, which recordsets.RecordSet's int
// would read as 0.
type RecordSet struct {
	recordsets.RecordSet
	// NullableTTL is the TTL as sent; nil means inherited from the zone.
	NullableTTL *int
}

type recordSetTTL struct {
	TTL *int `json:"ttl"`
}

// extractRecordSet decodes a get, create or update result (designate sends
// the recordset without an envelope).
func extractRecordSet(r interface{ ExtractIntoStructPtr(any, string) error }) (*RecordSet, error) {
	var rs RecordSet
	if err := r.ExtractIntoStructPtr(&rs.RecordSet, ""); err != nil {
		return nil, err
	}
	var raw recordSetTTL
	if err := r.ExtractIntoStructPtr(&raw, ""); err != nil {
		return nil, err
	}
	rs.NullableTTL = raw.TTL
	return &rs, nil
}

// ExtractRecordSets is recordsets.ExtractRecordSets with nullable TTLs.
func ExtractRecordSets(page pagination.Page) ([]RecordSet, error) {
	typed, err := recordsets.ExtractRecordSets(page)
	if err != nil {
		return nil, err
	}
	rp, ok := page.(recordsets.RecordSetPage)
	if !ok {
		return nil, fmt.Errorf("unexpected recordset page type %T", page)
	}
	var raw []recordSetTTL
	if err := rp.ExtractIntoSlicePtr(&raw, "recordsets"); err != nil {
		return nil, err
	}
	out := make([]RecordSet, len(typed))
	for i := range typed {
		out[i].RecordSet = typed[i]
		if i < len(raw) {
			out[i].NullableTTL = raw[i].TTL
		}
	}
	return out, nil
}

// ListRecordSets lists a zone's recordsets matching opts, stopping after
// limit results (0 = no cap).
func ListRecordSets(ctx context.Context, c *gophercloud.ServiceClient, zoneID string,
	opts recordsets.ListOptsBuilder, limit int,
) ([]RecordSet, error) {
	all, err := paging.Collect(ctx, recordsets.ListByZone(c, zoneID, opts), limit, ExtractRecordSets)
	return all, wrap(err, "listing recordsets in zone %s", zoneID)
}

// FindRecordSets returns a zone's recordsets named name exactly; the name may
// omit its trailing dot. One name can hold a recordset per type.
func FindRecordSets(ctx context.Context, c *gophercloud.ServiceClient, zoneID, name string) ([]recordsets.RecordSet, error) {
	want := withTrailingDot(name)
	pages, err := recordsets.ListByZone(c, zoneID, recordsets.ListOpts{Name: want}).AllPages(ctx)
	if err != nil {
		return nil, fmt.Errorf("looking up recordset %q: %w", name, err)
	}
	all, err := recordsets.ExtractRecordSets(pages)
	if err != nil {
		return nil, fmt.Errorf("looking up recordset %q: %w", name, err)
	}
	var matches []recordsets.RecordSet
	for _, rs := range all {
		if rs.Name == want || rs.Name == name {
			matches = append(matches, rs)
		}
	}
	return matches, nil
}

// FindRecordSetID resolves a recordset name or ID in a zone to an ID.
func FindRecordSetID(ctx context.Context, c *gophercloud.ServiceClient, zoneID, ref string) (string, error) {
	if resolve.IsUUID(ref) {
		return ref, nil
	}
	matches, err := FindRecordSets(ctx, c, zoneID, ref)
	if err != nil {
		return "", err
	}
	switch len(matches) {
	case 1:
		return matches[0].ID, nil
	case 0:
		return "", &resolve.NotFoundError{Kind: "recordset", Ref: ref}
	default:
		return "", &resolve.AmbiguousError{Kind: "recordset", Ref: ref, Count: len(matches)}
	}
}

// GetRecordSet reads one recordset.
func GetRecordSet(ctx context.Context, c *gophercloud.ServiceClient, zoneID, id string) (*RecordSet, error) {
	rs, err := extractRecordSet(recordsets.Get(ctx, c, zoneID, id))
	return rs, wrap(err, "getting recordset %s", id)
}

// CreateRecordSet creates a recordset in a zone.
func CreateRecordSet(ctx context.Context, c *gophercloud.ServiceClient, zoneID string,
	opts recordsets.CreateOptsBuilder,
) (*RecordSet, error) {
	rs, err := extractRecordSet(recordsets.Create(ctx, c, zoneID, opts))
	return rs, wrap(err, "creating recordset in zone %s", zoneID)
}

// UpdateRecordSet updates a recordset.
func UpdateRecordSet(ctx context.Context, c *gophercloud.ServiceClient, zoneID, id string,
	opts recordsets.UpdateOptsBuilder,
) (*RecordSet, error) {
	rs, err := extractRecordSet(recordsets.Update(ctx, c, zoneID, id, opts))
	return rs, wrap(err, "updating recordset %s", id)
}

// DeleteRecordSet deletes a recordset.
func DeleteRecordSet(ctx context.Context, c *gophercloud.ServiceClient, zoneID, id string) error {
	return wrap(recordsets.Delete(ctx, c, zoneID, id).ExtractErr(), "deleting recordset %s", id)
}

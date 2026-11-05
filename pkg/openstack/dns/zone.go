package dns

import (
	"context"
	"fmt"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/dns/v2/zones"

	"github.com/ftarasenko/go-openstackclient/pkg/openstack/paging"
	"github.com/ftarasenko/go-openstackclient/pkg/openstack/resolve"
)

// ZoneDetail is a zone plus the attributes zones.Zone does not model.
type ZoneDetail struct {
	*zones.Zone
	// Shared is designate's zone-sharing flag (2023.2 on). It is nil when
	// designate does not send it.
	Shared *bool
}

// zoneResult is what zones.Get, Create and Update return.
type zoneResult interface {
	Extract() (*zones.Zone, error)
	ExtractInto(to any) error
}

// extractZone decodes the body twice, because zones.Zone's UnmarshalJSON
// drops the keys it does not know.
func extractZone(r zoneResult) (*ZoneDetail, error) {
	z, err := r.Extract()
	if err != nil {
		return nil, err
	}
	var ext struct {
		Shared *bool `json:"shared"`
	}
	if err := r.ExtractInto(&ext); err != nil {
		return nil, err
	}
	return &ZoneDetail{Zone: z, Shared: ext.Shared}, nil
}

// ZoneUpdateMap is a raw zone-update body. zones.UpdateOpts drops a zero
// TTL, so it cannot send "ttl": 0; this map can.
type ZoneUpdateMap map[string]any

// ToZoneUpdateMap implements zones.UpdateOptsBuilder.
func (m ZoneUpdateMap) ToZoneUpdateMap() (map[string]any, error) { return m, nil }

// FindZone resolves a zone by ID or name. The name may omit its trailing dot.
// Designate has no name-or-ID filter, so it lists every zone.
func FindZone(ctx context.Context, c *gophercloud.ServiceClient, ref string) (*zones.Zone, error) {
	pages, err := zones.List(c, zones.ListOpts{}).AllPages(ctx)
	if err != nil {
		return nil, fmt.Errorf("looking up zone %q: %w", ref, err)
	}
	all, err := zones.ExtractZones(pages)
	if err != nil {
		return nil, fmt.Errorf("looking up zone %q: %w", ref, err)
	}
	want := withTrailingDot(ref)
	for i := range all {
		if z := &all[i]; z.ID == ref || z.Name == ref || z.Name == want {
			return z, nil
		}
	}
	return nil, &resolve.NotFoundError{Kind: "zone", Ref: ref}
}

// FindZoneID is FindZone's ID.
func FindZoneID(ctx context.Context, c *gophercloud.ServiceClient, ref string) (string, error) {
	z, err := FindZone(ctx, c, ref)
	if err != nil {
		return "", err
	}
	return z.ID, nil
}

// ListZones lists zones matching opts, stopping after limit results (0 = no
// cap). Designate reads opts' Limit as a page size only.
func ListZones(ctx context.Context, c *gophercloud.ServiceClient, opts zones.ListOptsBuilder, limit int) ([]zones.Zone, error) {
	all, err := paging.Collect(ctx, zones.List(c, opts), limit, zones.ExtractZones)
	return all, wrap(err, "listing dns zones")
}

// GetZone reads one zone.
func GetZone(ctx context.Context, c *gophercloud.ServiceClient, id string) (*ZoneDetail, error) {
	z, err := extractZone(zones.Get(ctx, c, id))
	return z, wrap(err, "getting dns zone %s", id)
}

// CreateZone creates a zone.
func CreateZone(ctx context.Context, c *gophercloud.ServiceClient, opts zones.CreateOptsBuilder) (*ZoneDetail, error) {
	z, err := extractZone(zones.Create(ctx, c, opts))
	return z, wrap(err, "creating dns zone")
}

// UpdateZone updates a zone; ZoneUpdateMap can send a zero TTL.
func UpdateZone(ctx context.Context, c *gophercloud.ServiceClient, id string, opts zones.UpdateOptsBuilder) (*ZoneDetail, error) {
	z, err := extractZone(zones.Update(ctx, c, id, opts))
	return z, wrap(err, "updating dns zone %s", id)
}

// DeleteZone deletes a zone.
func DeleteZone(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	_, err := zones.Delete(ctx, c, id).Extract()
	return wrap(err, "deleting dns zone %s", id)
}

// ZoneTask starts one of a zone's tasks (abandon, xfr, pool_move) with an
// optional JSON body.
func ZoneTask(ctx context.Context, c *gophercloud.ServiceClient, zoneID, task string, body map[string]any) error {
	var jsonBody any
	if body != nil {
		jsonBody = body
	}
	err := postNoContent(ctx, c, c.ServiceURL("zones", zoneID, "tasks", task), jsonBody)
	return wrap(err, "%s zone %s", task, zoneID)
}

// ListZoneNameservers lists the nameservers serving a zone.
func ListZoneNameservers(ctx context.Context, c *gophercloud.ServiceClient, zoneID string) ([]PoolNS, error) {
	var body struct {
		Nameservers []PoolNS `json:"nameservers"`
	}
	err := getJSON(ctx, c, c.ServiceURL("zones", zoneID, "nameservers"), &body)
	if err != nil {
		return nil, wrap(err, "listing nameservers of zone %s", zoneID)
	}
	return body.Nameservers, nil
}

// ShareZone shares a zone with another project.
func ShareZone(ctx context.Context, c *gophercloud.ServiceClient, zoneID, targetProjectID string) (*zones.ZoneShare, error) {
	s, err := zones.Share(ctx, c, zoneID, zones.ShareZoneOpts{TargetProjectID: targetProjectID}).Extract()
	return s, wrap(err, "sharing zone %s with project %q", zoneID, targetProjectID)
}

// ListZoneShares lists a zone's shares.
func ListZoneShares(ctx context.Context, c *gophercloud.ServiceClient, zoneID string) ([]zones.ZoneShare, error) {
	pages, err := zones.ListShares(c, zoneID, zones.ListSharesOpts{}).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing shares of zone %s", zoneID)
	}
	all, err := zones.ExtractZoneShares(pages)
	return all, wrap(err, "parsing zone share list")
}

// GetZoneShare reads one share of a zone.
func GetZoneShare(ctx context.Context, c *gophercloud.ServiceClient, zoneID, shareID string) (*zones.ZoneShare, error) {
	s, err := zones.GetShare(ctx, c, zoneID, shareID).Extract()
	return s, wrap(err, "showing share %s of zone %s", shareID, zoneID)
}

// UnshareZone removes one share of a zone.
func UnshareZone(ctx context.Context, c *gophercloud.ServiceClient, zoneID, shareID string) error {
	err := zones.Unshare(ctx, c, zoneID, shareID).ExtractErr()
	return wrap(err, "removing share %s from zone %s", shareID, zoneID)
}

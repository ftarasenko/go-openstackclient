package dns

import (
	"context"
	"fmt"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/dns/v2/recordsets"
	"github.com/gophercloud/gophercloud/v2/openstack/dns/v2/zones"
)

// withTrailingDot returns the DNS-canonical form of a name (designate zone and
// recordset names are fully qualified and end with a dot).
func withTrailingDot(name string) string {
	if name == "" || strings.HasSuffix(name, ".") {
		return name
	}
	return name + "."
}

// resolveZoneID turns a user-supplied zone reference (a zone name such as
// "example.com." or an ID) into a zone ID. Zones are addressed by ID in the
// designate API, so a name is resolved by listing zones and matching on name
// (tolerating a missing trailing dot) or on an exact ID.
func resolveZoneID(ctx context.Context, client *gophercloud.ServiceClient, ref string) (string, error) {
	pages, err := zones.List(client, zones.ListOpts{}).AllPages(ctx)
	if err != nil {
		return "", fmt.Errorf("resolving zone %q: %w", ref, err)
	}
	all, err := zones.ExtractZones(pages)
	if err != nil {
		return "", fmt.Errorf("resolving zone %q: %w", ref, err)
	}
	want := withTrailingDot(ref)
	for _, z := range all {
		if z.ID == ref || z.Name == ref || z.Name == want {
			return z.ID, nil
		}
	}
	return "", fmt.Errorf("zone %q not found", ref)
}

// resolveRecordSetID turns a recordset reference (a name or an ID) into a
// recordset ID within the given zone, matching on name (tolerating a missing
// trailing dot) or on an exact ID.
func resolveRecordSetID(ctx context.Context, client *gophercloud.ServiceClient, zoneID, ref string) (string, error) {
	pages, err := recordsets.ListByZone(client, zoneID, recordsets.ListOpts{}).AllPages(ctx)
	if err != nil {
		return "", fmt.Errorf("resolving recordset %q: %w", ref, err)
	}
	all, err := recordsets.ExtractRecordSets(pages)
	if err != nil {
		return "", fmt.Errorf("resolving recordset %q: %w", ref, err)
	}
	want := withTrailingDot(ref)
	for _, rs := range all {
		if rs.ID == ref || rs.Name == ref || rs.Name == want {
			return rs.ID, nil
		}
	}
	return "", fmt.Errorf("recordset %q not found in zone %s", ref, zoneID)
}

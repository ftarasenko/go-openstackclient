package dns

import (
	"context"
	"fmt"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/dns/v2/recordsets"
	"github.com/gophercloud/gophercloud/v2/openstack/dns/v2/zones"

	"github.com/ftarasenko/go-openstackclient/internal/cli/resolve"
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
// recordset ID within the given zone. It mirrors the shared name→ID policy in
// internal/cli/resolve: a UUID is returned untouched with no API call, and a
// name is resolved with a server-side name filter (not by listing the whole
// zone) so a targeted verb such as "recordset delete" acts on exactly the named
// recordset.
//
// A recordset name is *not* unique within a zone — an A and an AAAA record can
// both be named "www.example.com." — so a name that matches more than one
// recordset is rejected rather than silently resolving to an arbitrary match;
// the caller must disambiguate with the recordset ID.
func resolveRecordSetID(ctx context.Context, client *gophercloud.ServiceClient, zoneID, ref string) (string, error) {
	if resolve.IsUUID(ref) {
		return ref, nil
	}
	want := withTrailingDot(ref)
	pages, err := recordsets.ListByZone(client, zoneID, recordsets.ListOpts{Name: want}).AllPages(ctx)
	if err != nil {
		return "", fmt.Errorf("resolving recordset %q: %w", ref, err)
	}
	all, err := recordsets.ExtractRecordSets(pages)
	if err != nil {
		return "", fmt.Errorf("resolving recordset %q: %w", ref, err)
	}
	// designate's ?name= filter can match loosely; keep only exact-name hits.
	var matches []recordsets.RecordSet
	for _, rs := range all {
		if rs.Name == want || rs.Name == ref {
			matches = append(matches, rs)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("recordset %q not found in zone %s", ref, zoneID)
	case 1:
		return matches[0].ID, nil
	default:
		types := make([]string, 0, len(matches))
		for _, rs := range matches {
			types = append(types, rs.Type)
		}
		return "", fmt.Errorf("recordset name %q is ambiguous in zone %s (matches types %s); specify the recordset ID instead",
			ref, zoneID, strings.Join(types, ", "))
	}
}

package dns

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/dns/v2/zones"

	"github.com/ftarasenko/go-openstackclient/internal/cli/resolve"
	dnsapi "github.com/ftarasenko/go-openstackclient/pkg/openstack/dns"
	osresolve "github.com/ftarasenko/go-openstackclient/pkg/openstack/resolve"
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
	z, err := resolveZone(ctx, client, ref)
	if err != nil {
		return "", err
	}
	return z.ID, nil
}

// qualifyName makes a recordset name absolute within zoneName the way
// python-designateclient's _canonicalize_record_name does: a name without a
// trailing dot is relative to the zone ("www" in "example.com." is
// "www.example.com."); one with a trailing dot is already fully qualified.
func qualifyName(name, zoneName string) string {
	if name == "" || strings.HasSuffix(name, ".") {
		return name
	}
	return name + "." + withTrailingDot(zoneName)
}

// resolveZone is resolveZoneID returning the whole zone, for callers that
// also need its name.
func resolveZone(ctx context.Context, client *gophercloud.ServiceClient, ref string) (*zones.Zone, error) {
	z, err := dnsapi.FindZone(ctx, client, ref)
	var notFound *osresolve.NotFoundError
	switch {
	case err == nil:
		return z, nil
	case errors.As(err, &notFound):
		return nil, fmt.Errorf("zone %q not found", ref)
	}
	return nil, relabel(err, "resolving zone %q", ref)
}

// resolveRecordSetID turns a recordset reference (a name or an ID) into a
// recordset ID within the given zone. A UUID is returned untouched with no API
// call, and a name is resolved with a server-side name filter (not by listing
// the whole zone) so a targeted verb such as "recordset delete" acts on
// exactly the named recordset.
//
// A recordset name is *not* unique within a zone — an A and an AAAA record can
// both be named "www.example.com." — so a name that matches more than one
// recordset is rejected rather than silently resolving to an arbitrary match;
// the caller must disambiguate with the recordset ID.
func resolveRecordSetID(ctx context.Context, client *gophercloud.ServiceClient, zoneID, ref string) (string, error) {
	if resolve.IsUUID(ref) {
		return ref, nil
	}
	matches, err := dnsapi.FindRecordSets(ctx, client, zoneID, ref)
	if err != nil {
		return "", relabel(err, "resolving recordset %q", ref)
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

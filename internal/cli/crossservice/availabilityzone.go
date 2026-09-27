package crossservice

import (
	"context"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"

	"github.com/gophercloud/gophercloud/v2"
	volumeaz "github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/availabilityzones"
	computeaz "github.com/gophercloud/gophercloud/v2/openstack/compute/v2/availabilityzones"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// `availability zone list` merges three separate endpoints — nova's
// /os-availability-zone, cinder's /os-availability-zone and neutron's
// /v2.0/availability_zones — into one listing, the way upstream does.
//
// Flag names follow upstream OSC (`openstack availability zone list`).
// UNVERIFIED against KeyStack docs (https://docs.keystack.ru/ returned HTTP 403
// at implementation time); falls back to upstream OSC semantics.

type availabilityZoneFlags struct {
	compute bool
	volume  bool
	network bool
	long    bool
}

func newAvailabilityZoneListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &availabilityZoneFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List availability zones across compute, volume and network",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := a.NewSession(ctx)
			if err != nil {
				return err
			}
			return runAvailabilityZoneList(ctx, client, o, f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&f.compute, "compute", false, "list compute availability zones")
	fl.BoolVar(&f.volume, "volume", false, "list volume availability zones")
	fl.BoolVar(&f.network, "network", false, "list network availability zones")
	fl.BoolVar(&f.long, "long", false, "list additional fields in output")
	return cmd
}

// availabilityZone is the merged row: a zone name can appear in more than one
// service, and the Zone Resource column is what distinguishes them. The host
// fields are set only for a compute zone listed with --long, one row per host
// service, as upstream does.
type availabilityZone struct {
	name     string
	resource string
	state    string

	host, service, serviceStatus string
}

func runAvailabilityZoneList(ctx context.Context, client *auth.Client, o *output.Options,
	f *availabilityZoneFlags, w io.Writer,
) error {
	// With no service flag, upstream lists all of them.
	all := !f.compute && !f.volume && !f.network
	var zones []availabilityZone

	if all || f.compute {
		sc, err := client.Compute()
		if err != nil {
			return err
		}
		got, err := computeAvailabilityZones(ctx, sc, f.long)
		if err != nil {
			return err
		}
		zones = append(zones, got...)
	}
	if all || f.volume {
		sc, err := client.Volume()
		if err != nil {
			return err
		}
		got, err := volumeAvailabilityZones(ctx, sc)
		if err != nil {
			return err
		}
		zones = append(zones, got...)
	}
	if all || f.network {
		sc, err := client.Network()
		if err != nil {
			return err
		}
		got, err := networkAvailabilityZones(ctx, sc)
		if err != nil {
			return err
		}
		zones = append(zones, got...)
	}

	return writeAvailabilityZones(o, zones, f.long, w)
}

// writeAvailabilityZones renders upstream's columns; koc also keeps Zone
// Resource without --long, and names it for nova's and cinder's zones too.
func writeAvailabilityZones(o *output.Options, zones []availabilityZone, long bool, w io.Writer) error {
	cols := []string{"Zone Name", "Zone Status", "Zone Resource"}
	if long {
		cols = append(cols, "Host Name", "Service Name", "Service Status")
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(zones))}
	for _, z := range zones {
		row := []any{z.name, z.state, z.resource}
		if long {
			row = append(row, z.host, z.service, z.serviceStatus)
		}
		t.Rows = append(t.Rows, row)
	}
	return o.WriteList(w, t)
}

func computeAvailabilityZones(ctx context.Context, sc *gophercloud.ServiceClient, long bool) ([]availabilityZone, error) {
	// Like upstream, ask for the detail listing — the one that shows nova's
	// internal zone and the hosts --long breaks out — and fall back to the
	// plain one when policy keeps it from this user.
	pages, err := computeaz.ListDetail(sc).AllPages(ctx)
	if gophercloud.ResponseCodeIs(err, http.StatusForbidden) {
		pages, err = computeaz.List(sc).AllPages(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("listing compute availability zones: %w", err)
	}
	list, err := computeaz.ExtractAvailabilityZones(pages)
	if err != nil {
		return nil, fmt.Errorf("parsing the compute availability zone list: %w", err)
	}
	out := make([]availabilityZone, 0, len(list))
	for _, z := range list {
		zone := availabilityZone{name: z.ZoneName, resource: "compute", state: zoneState(z.ZoneState.Available)}
		if !long || len(z.Hosts) == 0 {
			out = append(out, zone)
			continue
		}
		for _, host := range slices.Sorted(maps.Keys(z.Hosts)) {
			svcs := z.Hosts[host]
			for _, svc := range slices.Sorted(maps.Keys(svcs)) {
				row := zone
				row.host, row.service, row.serviceStatus = host, svc, serviceStatus(svcs[svc])
				out = append(out, row)
			}
		}
	}
	return out, nil
}

// serviceStatus is upstream's "enabled :-) <updated_at>" cell.
func serviceStatus(s computeaz.ServiceState) string {
	enabled, alive := "disabled", "XXX"
	if s.Active {
		enabled = "enabled"
	}
	if s.Available {
		alive = ":-)"
	}
	return fmt.Sprintf("%s %s %s", enabled, alive, s.UpdatedAt.UTC().Format("2006-01-02T15:04:05.000000"))
}

func volumeAvailabilityZones(ctx context.Context, sc *gophercloud.ServiceClient) ([]availabilityZone, error) {
	pages, err := volumeaz.List(sc).AllPages(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing volume availability zones: %w", err)
	}
	list, err := volumeaz.ExtractAvailabilityZones(pages)
	if err != nil {
		return nil, fmt.Errorf("parsing the volume availability zone list: %w", err)
	}
	out := make([]availabilityZone, 0, len(list))
	for _, z := range list {
		out = append(out, availabilityZone{
			name:     z.ZoneName,
			resource: "volume",
			state:    zoneState(z.ZoneState.Available),
		})
	}
	return out, nil
}

// networkAvailabilityZones reads neutron's availability_zone extension.
// gophercloud v2.13.0 has no package for it, so this is a raw GET — a flat
// list with no pagination, which is why it needs no page walker. Replace with
// the typed call if one lands upstream.
//
// Neutron's zones carry a `resource` of their own ("network" or "router"),
// unlike nova's and cinder's, so it is reported rather than hardcoded.
func networkAvailabilityZones(ctx context.Context, sc *gophercloud.ServiceClient) ([]availabilityZone, error) {
	var doc struct {
		AvailabilityZones []struct {
			Name     string `json:"name"`
			Resource string `json:"resource"`
			State    string `json:"state"`
		} `json:"availability_zones"`
	}
	resp, err := sc.Get(ctx, sc.ServiceURL("availability_zones"), &doc, &gophercloud.RequestOpts{OkCodes: []int{200}})
	if resp != nil && resp.Body != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if err != nil {
		return nil, fmt.Errorf("listing network availability zones: %w", err)
	}
	out := make([]availabilityZone, 0, len(doc.AvailabilityZones))
	for _, z := range doc.AvailabilityZones {
		resource := z.Resource
		if resource == "" {
			resource = "network"
		}
		out = append(out, availabilityZone{name: z.Name, resource: resource, state: z.State})
	}
	return out, nil
}

// zoneState renders nova's and cinder's boolean into upstream's wording.
func zoneState(available bool) string {
	if available {
		return "available"
	}
	return "not available"
}

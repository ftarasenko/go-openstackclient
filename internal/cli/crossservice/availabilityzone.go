package crossservice

import (
	"context"
	"fmt"
	"io"

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
			client, err := a.Authenticate(ctx)
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
// service, and the Zone Resource column is what distinguishes them.
type availabilityZone struct {
	name     string
	resource string
	state    string
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

	t := output.Table{
		Columns: []string{"Zone Name", "Zone Status", "Zone Resource"},
		Rows:    make([][]any, 0, len(zones)),
	}
	for _, z := range zones {
		t.Rows = append(t.Rows, []any{z.name, z.state, z.resource})
	}
	return o.WriteList(w, t)
}

func computeAvailabilityZones(ctx context.Context, sc *gophercloud.ServiceClient, detail bool) ([]availabilityZone, error) {
	// The detail endpoint is admin-only and adds the host breakdown; the plain
	// one is what a regular user can read, so --long selects between them
	// rather than always paying for the admin call.
	pager := computeaz.List(sc)
	if detail {
		pager = computeaz.ListDetail(sc)
	}
	pages, err := pager.AllPages(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing compute availability zones: %w", err)
	}
	list, err := computeaz.ExtractAvailabilityZones(pages)
	if err != nil {
		return nil, fmt.Errorf("parsing the compute availability zone list: %w", err)
	}
	out := make([]availabilityZone, 0, len(list))
	for _, z := range list {
		out = append(out, availabilityZone{
			name:     z.ZoneName,
			resource: "compute",
			state:    zoneState(z.ZoneState.Available),
		})
	}
	return out, nil
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

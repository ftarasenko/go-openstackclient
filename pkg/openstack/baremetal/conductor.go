package baremetal

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/baremetal/v1/conductors"

	"github.com/ftarasenko/go-openstackclient/pkg/openstack/paging"
)

// ListConductors lists conductors, at most limit of them (0: all).
func ListConductors(ctx context.Context, c *gophercloud.ServiceClient, opts conductors.ListOptsBuilder,
	limit int,
) ([]conductors.Conductor, error) {
	all, err := paging.Collect(ctx, conductors.List(c, opts), limit, conductors.ExtractConductors)
	return all, wrap(err, "listing baremetal conductors")
}

// GetConductor reads one conductor by hostname.
func GetConductor(ctx context.Context, c *gophercloud.ServiceClient, hostname string) (*conductors.Conductor, error) {
	cd, err := conductors.Get(ctx, c, hostname).Extract()
	return cd, wrap(err, "showing baremetal conductor %s", hostname)
}

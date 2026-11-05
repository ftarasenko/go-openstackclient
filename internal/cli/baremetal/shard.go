package baremetal

import (
	"context"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
	bmapi "github.com/ftarasenko/go-openstackclient/pkg/openstack/baremetal"
)

// Shards partition nodes between consumers such as nova-compute services
// (API 1.82, /v1/shards). gophercloud has no package for them, so the list
// uses the raw helpers in raw.go. A shard exists while some node names it.

// shardFields are python-ironicclient's SHARD_RESOURCE.
var shardFields = []string{"name", "count"}

func newShardCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "shard",
		Short: "Baremetal node shards (requires ironic API 1.82)",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List baremetal shards and how many nodes each holds",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runShardList(ctx, client, o, cmd.OutOrStdout())
		},
	})
	return cmd
}

func runShardList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, w io.Writer) error {
	shards, err := bmapi.ListShards(ctx, client)
	if err != nil {
		return explained(err)
	}
	return o.WriteList(w, listTable(shards, shardFields))
}

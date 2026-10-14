package baremetal

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Shards partition nodes between consumers such as nova-compute services
// (API 1.82, /v1/shards). gophercloud has no package for them, so the list
// uses the raw helpers in raw.go. A shard exists while some node names it.

// featureShards dates from ironic 21.4, Zed's last point release and the
// 2023.1 release; earlier Zed point releases stop at 1.80.
var featureShards = ironicFeature{"baremetal shard list / node --shard", "1.82", "ironic 21.4, OpenStack 2023.1"}

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
	var body struct {
		Shards []map[string]any `json:"shards"`
	}
	if err := ironicGet(ctx, client, "shards", &body); err != nil {
		return fmt.Errorf("listing baremetal shards: %w", explainMicroversion(ctx, client, featureShards, err))
	}
	return o.WriteList(w, listTable(body.Shards, shardFields))
}

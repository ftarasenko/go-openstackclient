package volume

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/koc/internal/auth"
	"github.com/ftarasenko/koc/internal/output"
	volumeapi "github.com/ftarasenko/koc/pkg/openstack/volume"
)

// "koc block storage cluster list|set|show" — cinder's clustered-services view,
// mirroring upstream OSC's `block storage cluster …`. A cluster is the unit an
// active/active cinder-volume deployment schedules against: several hosts run
// the same backend under one cluster name, and it is the cluster, not the
// individual service, that is enabled or disabled.
//
// Flag names follow upstream OSC; the KeyStack reference (docs.keystack.ru)
// returned HTTP 403 at implementation time, so the surface is UNVERIFIED
// against KeyStack and falls back to upstream OSC.

// newBlockStorageCommand builds the "block storage …" group. Upstream models
// this as a three-word noun ("block storage cluster"), so it is nested parent
// commands rather than a leaf under "koc volume".
func newBlockStorageCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "block",
		Short: "Block storage (cinder) service-level commands",
	}
	storage := &cobra.Command{
		Use:   "storage",
		Short: "Block storage (cinder) service-level commands",
	}
	storage.AddCommand(newBlockStorageClusterCommand(a, o))
	cmd.AddCommand(storage)
	return cmd
}

func newBlockStorageClusterCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cluster",
		Short: "Manage block storage clusters (requires volume API 3.7)",
	}
	cmd.AddCommand(
		newClusterListCommand(a, o),
		newClusterShowCommand(a, o),
		newClusterSetCommand(a, o),
	)
	return cmd
}

// clusterListFlags holds the filters accepted by "block storage cluster list".
type clusterListFlags struct {
	name   string
	binary string
	long   bool
	// isUp and isDisabled are tri-state: nil leaves the filter off entirely,
	// which is not the same as filtering for false. --up/--down and
	// --enabled/--disabled are the two flag pairs that set them.
	isUp       *bool
	isDisabled *bool
	// A count filter of 0 is meaningful — "no hosts down" is exactly the
	// healthy cluster — so presence is tracked separately from the value.
	numHosts     *int
	numDownHosts *int
}

func newClusterListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &clusterListFlags{}
	var up, down, enabled, disabled bool
	var numHosts, numDownHosts int
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List block storage clusters (requires volume API 3.7)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			fl := cmd.Flags()
			switch {
			case fl.Changed("up"):
				f.isUp = &up
			case fl.Changed("down"):
				f.isUp = ptr(!down)
			}
			switch {
			case fl.Changed("disabled"):
				f.isDisabled = &disabled
			case fl.Changed("enabled"):
				f.isDisabled = ptr(!enabled)
			}
			if fl.Changed("num-hosts") {
				f.numHosts = &numHosts
			}
			if fl.Changed("num-down-hosts") {
				f.numDownHosts = &numDownHosts
			}
			ctx := cmd.Context()
			client, err := newVolumeClient(ctx, a)
			if err != nil {
				return err
			}
			return runClusterList(ctx, client, o, f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	// --cluster, not --name: upstream spells the cluster-name filter this way.
	fl.StringVar(&f.name, "cluster", "", "filter by cluster name; without a backend this lists every clustered service in the cluster")
	fl.StringVar(&f.binary, "binary", "", "filter by cluster binary, e.g. cinder-volume")
	fl.BoolVar(&up, "up", false, "list only clusters that are up")
	fl.BoolVar(&down, "down", false, "list only clusters that are down")
	fl.BoolVar(&disabled, "disabled", false, "list only disabled clusters")
	fl.BoolVar(&enabled, "enabled", false, "list only enabled clusters")
	fl.IntVar(&numHosts, "num-hosts", 0, "filter by number of hosts in the cluster")
	fl.IntVar(&numDownHosts, "num-down-hosts", 0, "filter by number of hosts that are down")
	fl.BoolVar(&f.long, "long", false, "list additional fields in output")
	cmd.MarkFlagsMutuallyExclusive("up", "down")
	cmd.MarkFlagsMutuallyExclusive("enabled", "disabled")
	return cmd
}

func ptr[T any](v T) *T { return &v }

func runClusterList(ctx context.Context, client *gophercloud.ServiceClient,
	o *output.Options, f *clusterListFlags, w io.Writer,
) error {
	if err := requireVolumeMicroversion(client, volumeapi.ClusterMicroversion, "block storage cluster list"); err != nil {
		return err
	}
	// The summary listing carries only the four base columns; everything --long
	// renders comes from /clusters/detail, so the flag picks the endpoint as
	// well as the column set.
	clusters, err := volumeapi.ListClusters(ctx, client, volumeapi.ClusterListOpts{
		Name: f.name, Binary: f.binary, IsUp: f.isUp, Disabled: f.isDisabled,
		NumHosts: f.numHosts, NumDownHosts: f.numDownHosts, Detail: f.long,
	})
	if err != nil {
		return err
	}
	return o.WriteList(w, clusterListTable(clusters, f.long))
}

// clusterListTable renders the cluster listing, column-for-column with
// upstream's.
func clusterListTable(clusters []volumeapi.Cluster, long bool) output.Table {
	cols := []string{"Name", "Binary", "State", "Status"}
	if long {
		cols = append(cols, "Num Hosts", "Num Down Hosts", "Last Heartbeat", "Disabled Reason", "Created At", "Updated At")
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(clusters))}
	for _, c := range clusters {
		row := []any{c.Name, c.Binary, c.State, c.Status}
		if long {
			row = append(row, c.NumHosts, c.NumDownHosts, c.LastHeartbeat, c.DisabledReason, c.CreatedAt, c.UpdatedAt)
		}
		t.Rows = append(t.Rows, row)
	}
	return t
}

func newClusterShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var binary string
	cmd := &cobra.Command{
		Use:   "show <cluster>",
		Short: "Show details of a block storage cluster (requires volume API 3.7)",
		Long: "Show details of a block storage cluster (requires volume API 3.7).\n\n" +
			"<cluster> is the cluster name as it appears in the Cluster column of " +
			"\"koc volume service list\" — \"<cluster>\" for the whole cluster, or " +
			"\"<cluster>@<backend>\" for one of its backends.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newVolumeClient(ctx, a)
			if err != nil {
				return err
			}
			return runClusterShow(ctx, client, o, args[0], binary, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&binary, "binary", "", "service binary, when one cluster name serves more than one")
	return cmd
}

func runClusterShow(ctx context.Context, client *gophercloud.ServiceClient,
	o *output.Options, name, binary string, w io.Writer,
) error {
	if err := requireVolumeMicroversion(client, volumeapi.ClusterMicroversion, "block storage cluster show"); err != nil {
		return err
	}
	// Upstream OSC declares --binary on this verb and then drops it before the
	// request, so the flag silently does nothing there; cinder does accept it as
	// a query parameter, so koc sends it.
	c, err := volumeapi.GetCluster(ctx, client, name, binary)
	if err != nil {
		return err
	}
	return writeCluster(o, *c, w)
}

type clusterSetFlags struct {
	binary        string
	enable        bool
	disable       bool
	disableReason string
}

func newClusterSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &clusterSetFlags{}
	cmd := &cobra.Command{
		Use:   "set <cluster>",
		Short: "Enable or disable a block storage cluster (requires volume API 3.7)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			// Upstream treats "neither flag" as --enable, so a bare
			// "block storage cluster set <name>" silently re-enables a cluster an
			// operator disabled. koc requires the verb to be named, as
			// "koc volume service set" already does.
			if !f.enable && !f.disable {
				return fmt.Errorf("nothing to do: pass --enable or --disable")
			}
			if f.disableReason != "" && !f.disable {
				return fmt.Errorf("--disable-reason requires --disable")
			}
			ctx := cmd.Context()
			client, err := newVolumeClient(ctx, a)
			if err != nil {
				return err
			}
			return runClusterSet(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	// Cinder keys the enable/disable body on name *and* binary; upstream defaults
	// the binary to cinder-volume, which is the only clustered binary there is.
	fl.StringVar(&f.binary, "binary", "cinder-volume", "binary of the clustered service")
	fl.BoolVar(&f.enable, "enable", false, "enable the cluster")
	fl.BoolVar(&f.disable, "disable", false, "disable the cluster")
	fl.StringVar(&f.disableReason, "disable-reason", "", "reason for disabling the cluster (requires --disable)")
	cmd.MarkFlagsMutuallyExclusive("enable", "disable")
	return cmd
}

func runClusterSet(ctx context.Context, client *gophercloud.ServiceClient,
	o *output.Options, name string, f *clusterSetFlags, w io.Writer,
) error {
	if err := o.CheckColumns(clusterColumns...); err != nil {
		return err
	}
	if err := requireVolumeMicroversion(client, volumeapi.ClusterMicroversion, "block storage cluster set"); err != nil {
		return err
	}
	var c *volumeapi.Cluster
	var err error
	if f.disable {
		c, err = volumeapi.DisableCluster(ctx, client, name, f.binary, f.disableReason)
	} else {
		c, err = volumeapi.EnableCluster(ctx, client, name, f.binary)
	}
	if err != nil {
		return err
	}
	// Both verbs answer with the updated cluster, and upstream renders it; so
	// does koc, which makes the new status the command's own confirmation.
	return writeCluster(o, *c, w)
}

// clusterColumns is upstream's detailed cluster format.
var clusterColumns = []string{
	"Name", "Binary", "State", "Status", "Disabled Reason",
	"Hosts", "Down Hosts", "Last Heartbeat", "Created At", "Updated At",
	"Replication Status", "Frozen", "Active Backend ID",
}

// writeCluster renders one cluster as a Field/Value view. The field set and its
// order are upstream's detailed cluster format, including its own inconsistency:
// the listing's --long calls these counts "Num Hosts"/"Num Down Hosts" while the
// single-cluster view calls them "Hosts"/"Down Hosts".
func writeCluster(o *output.Options, c volumeapi.Cluster, w io.Writer) error {
	values := []any{
		c.Name, c.Binary, c.State, c.Status, c.DisabledReason,
		c.NumHosts, c.NumDownHosts, c.LastHeartbeat, c.CreatedAt, c.UpdatedAt,
		c.ReplicationStatus, c.Frozen, c.ActiveBackendID,
	}
	return o.WriteSingle(w, clusterColumns, values)
}

package volume

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strconv"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// "koc block storage cluster list|set|show" — cinder's clustered-services view,
// mirroring upstream OSC's `block storage cluster …`. A cluster is the unit an
// active/active cinder-volume deployment schedules against: several hosts run
// the same backend under one cluster name, and it is the cluster, not the
// individual service, that is enabled or disabled.
//
// gophercloud has no blockstorage/v3/clusters package, so these are raw
// ServiceClient calls (an AGENTS.md-sanctioned fallback) decoding into the DTO
// below. The URLs and bodies follow cinder's own API: GET /clusters and
// /clusters/detail, GET /clusters/<name>?binary=…, and PUT /clusters/enable |
// /clusters/disable with a {"name", "binary", "disabled_reason"} body — the
// same routes python-cinderclient's ClusterManager and openstacksdk's
// block_storage.v3.cluster resource drive.
//
// Flag names follow upstream OSC; the KeyStack reference (docs.keystack.ru)
// returned HTTP 403 at implementation time, so the surface is UNVERIFIED
// against KeyStack and falls back to upstream OSC.

// clusterMicroversion gates the whole /clusters resource: cinder added
// clustered services in 3.7, and below it the endpoint does not exist. Zed caps
// cinder at 3.70, so every supported cloud has it.
const clusterMicroversion = "3.7"

// cluster is one entry of cinder's /clusters response. The fields after Status
// are returned only by the detail listing and by show/enable/disable.
//
// Timestamps stay strings: these are raw decodes, so cinder's own bytes are
// passed through rather than round-tripped via time.Time, as everywhere else in
// koc that decodes an API without a typed package.
type cluster struct {
	Name              string `json:"name"`
	Binary            string `json:"binary"`
	State             string `json:"state"`
	Status            string `json:"status"`
	DisabledReason    string `json:"disabled_reason"`
	NumHosts          int    `json:"num_hosts"`
	NumDownHosts      int    `json:"num_down_hosts"`
	LastHeartbeat     string `json:"last_heartbeat"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
	ReplicationStatus string `json:"replication_status"`
	Frozen            bool   `json:"frozen"`
	ActiveBackendID   string `json:"active_backend_id"`
}

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
	if err := requireVolumeMicroversion(client, clusterMicroversion, "block storage cluster list"); err != nil {
		return err
	}
	q := url.Values{}
	if f.name != "" {
		q.Set("name", f.name)
	}
	if f.binary != "" {
		q.Set("binary", f.binary)
	}
	if f.isUp != nil {
		q.Set("is_up", strconv.FormatBool(*f.isUp))
	}
	if f.isDisabled != nil {
		q.Set("disabled", strconv.FormatBool(*f.isDisabled))
	}
	if f.numHosts != nil {
		q.Set("num_hosts", strconv.Itoa(*f.numHosts))
	}
	if f.numDownHosts != nil {
		q.Set("num_down_hosts", strconv.Itoa(*f.numDownHosts))
	}

	// The summary listing carries only the four base columns; everything --long
	// renders comes from /clusters/detail, so the flag picks the endpoint as
	// well as the column set.
	u := client.ServiceURL("clusters")
	if f.long {
		u = client.ServiceURL("clusters", "detail")
	}
	if encoded := q.Encode(); encoded != "" {
		u += "?" + encoded
	}
	var resp struct {
		Clusters []cluster `json:"clusters"`
	}
	r, err := client.Get(ctx, u, &resp, &gophercloud.RequestOpts{OkCodes: []int{200}})
	if r != nil {
		_ = r.Body.Close()
	}
	if err != nil {
		return fmt.Errorf("listing block storage clusters: %w", err)
	}
	return o.WriteList(w, clusterListTable(resp.Clusters, f.long))
}

// clusterListTable renders the cluster listing, column-for-column with
// upstream's.
func clusterListTable(clusters []cluster, long bool) output.Table {
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
	if err := requireVolumeMicroversion(client, clusterMicroversion, "block storage cluster show"); err != nil {
		return err
	}
	u := client.ServiceURL("clusters", name)
	// Upstream OSC declares --binary on this verb and then drops it before the
	// request, so the flag silently does nothing there; cinder does accept it as
	// a query parameter, so koc sends it.
	if binary != "" {
		u += "?" + url.Values{"binary": {binary}}.Encode()
	}
	var resp struct {
		Cluster cluster `json:"cluster"`
	}
	r, err := client.Get(ctx, u, &resp, &gophercloud.RequestOpts{OkCodes: []int{200}})
	if r != nil {
		_ = r.Body.Close()
	}
	if err != nil {
		return fmt.Errorf("showing block storage cluster %q: %w", name, err)
	}
	return writeCluster(o, resp.Cluster, w)
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
	if err := requireVolumeMicroversion(client, clusterMicroversion, "block storage cluster set"); err != nil {
		return err
	}
	action, verb := "enable", "enabling"
	body := map[string]any{"name": name}
	if f.binary != "" {
		body["binary"] = f.binary
	}
	if f.disable {
		action, verb = "disable", "disabling"
		if f.disableReason != "" {
			body["disabled_reason"] = f.disableReason
		}
	}
	var resp struct {
		Cluster cluster `json:"cluster"`
	}
	r, err := client.Put(ctx, client.ServiceURL("clusters", action), body, &resp,
		&gophercloud.RequestOpts{OkCodes: []int{200}})
	if r != nil {
		_ = r.Body.Close()
	}
	if err != nil {
		return fmt.Errorf("%s block storage cluster %q: %w", verb, name, err)
	}
	// Both verbs answer with the updated cluster, and upstream renders it; so
	// does koc, which makes the new status the command's own confirmation.
	return writeCluster(o, resp.Cluster, w)
}

// writeCluster renders one cluster as a Field/Value view. The field set and its
// order are upstream's detailed cluster format, including its own inconsistency:
// the listing's --long calls these counts "Num Hosts"/"Num Down Hosts" while the
// single-cluster view calls them "Hosts"/"Down Hosts".
func writeCluster(o *output.Options, c cluster, w io.Writer) error {
	fields := []string{
		"Name", "Binary", "State", "Status", "Disabled Reason",
		"Hosts", "Down Hosts", "Last Heartbeat", "Created At", "Updated At",
		"Replication Status", "Frozen", "Active Backend ID",
	}
	values := []any{
		c.Name, c.Binary, c.State, c.Status, c.DisabledReason,
		c.NumHosts, c.NumDownHosts, c.LastHeartbeat, c.CreatedAt, c.UpdatedAt,
		c.ReplicationStatus, c.Frozen, c.ActiveBackendID,
	}
	return o.WriteSingle(w, fields, values)
}

package keyvrm

import (
	"context"
	"io"
	"strconv"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

func newHostAggregateConfigCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "host-aggregate-config", Short: "Manage KeyVRM host-aggregate configuration"}
	cmd.AddCommand(
		newHAList(a, o),
		newHAShow(a, o),
		newHASet(a, o),
		newHAMarkers(a, o),
		newHAEventCommand(a, o),
	)
	return cmd
}

type haListFlags struct {
	az, haName, marker string
	noOp               bool
	limit, offset      int
}

func newHAList(a *auth.Options, o *output.Options) *cobra.Command {
	f := &haListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List host-aggregate configurations",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			sc, err := newKeyVRMClient(cmd.Context(), a)
			if err != nil {
				return err
			}
			opts := listOpts{Limit: f.limit, Offset: f.offset, filters: map[string]string{
				"availability_zone_name": f.az,
				"host_aggregate_name":    f.haName,
				"marker":                 f.marker,
			}}
			if cmd.Flags().Changed("no-op") {
				opts.filters["no_op_mode"] = strconv.FormatBool(f.noOp)
			}
			return runHAList(cmd.Context(), sc, o, opts, cmd.OutOrStdout())
		},
	}
	fs := cmd.Flags()
	fs.StringVar(&f.az, "az", "", "filter by availability zone")
	fs.StringVar(&f.haName, "ha-name", "", "filter by host aggregate name")
	fs.StringVar(&f.marker, "marker", "", "filter by marker (LB, HA, HA+LB)")
	fs.BoolVar(&f.noOp, "no-op", false, "filter by no-op mode")
	fs.IntVar(&f.limit, "limit", 50, "page limit")
	fs.IntVar(&f.offset, "offset", 0, "page offset")
	return cmd
}

func runHAList(ctx context.Context, sc *gophercloud.ServiceClient, o *output.Options, opts listOpts, w io.Writer) error {
	p, err := listHostAggregates(ctx, sc, opts)
	if err != nil {
		return err
	}
	rows := make([][]any, len(p.Data))
	for i, h := range p.Data {
		rows[i] = haConfigRow(h)
	}
	writeTotal(p.Total, p.Limit, p.Offset)
	return o.WriteList(w, output.Table{Columns: haConfigColumns, Rows: rows})
}

func newHAShow(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <ha-id>",
		Short: "Show a host-aggregate configuration",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			sc, err := newKeyVRMClient(cmd.Context(), a)
			if err != nil {
				return err
			}
			return runHAShow(cmd.Context(), sc, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runHAShow(ctx context.Context, sc *gophercloud.ServiceClient, o *output.Options, id string, w io.Writer) error {
	h, err := getHostAggregate(ctx, sc, id)
	if err != nil {
		return err
	}
	fields, values := haConfigView(h)
	return o.WriteSingle(w, fields, values)
}

var haSetSpec = []flagSpec{
	{"marker", "marker", kindStr},
	{"ha-reservation-ratio-cpu", "ha_reservation_ratio_cpu", kindFloat},
	{"ha-reservation-ratio-ram", "ha_reservation_ratio_ram", kindFloat},
	{"lb-cpu-weight", "lb_cpu_weight", kindFloat},
	{"lb-ram-weight", "lb_ram_weight", kindFloat},
	{"lb-network-weight", "lb_network_weight", kindFloat},
	{"lb-recommendations-auto-run", "lb_recommendations_auto_run", kindBool},
	{"lb-threshold-overload", "lb_threshold_overload", kindInt},
	{"lb-threshold-limit", "lb_threshold_limit", kindInt},
	{"lb-period", "lb_period", kindInt},
	{"no-op-mode", "no_op_mode", kindBool},
	{"no-op-mode-reason", "no_op_mode_reason", kindStr},
}

func newHASet(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set <ha-id>",
		Short: "Update a host-aggregate configuration",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			body := buildBody(cmd, haSetSpec)
			if len(body) == 0 {
				return errNoUpdateFields
			}
			sc, err := newKeyVRMClient(cmd.Context(), a)
			if err != nil {
				return err
			}
			return runHASet(cmd.Context(), sc, o, args[0], body, cmd.OutOrStdout())
		},
	}
	fs := cmd.Flags()
	fs.String("marker", "", "marker (LB, HA, HA+LB)")
	fs.Float64("ha-reservation-ratio-cpu", 0, "HA CPU reservation ratio")
	fs.Float64("ha-reservation-ratio-ram", 0, "HA RAM reservation ratio")
	fs.Float64("lb-cpu-weight", 0, "load-balancer CPU weight")
	fs.Float64("lb-ram-weight", 0, "load-balancer RAM weight")
	fs.Float64("lb-network-weight", 0, "load-balancer network weight")
	fs.Bool("lb-recommendations-auto-run", false, "auto-run recommendations")
	fs.Int("lb-threshold-overload", 0, "overload threshold")
	fs.Int("lb-threshold-limit", 0, "limit threshold")
	fs.Int("lb-period", 0, "load-balancer period")
	fs.Bool("no-op-mode", false, "enable/disable no-op mode")
	fs.String("no-op-mode-reason", "", "reason for no-op mode")
	return cmd
}

func runHASet(ctx context.Context, sc *gophercloud.ServiceClient, o *output.Options, id string, body map[string]any, w io.Writer) error {
	h, err := updateHostAggregate(ctx, sc, id, body)
	if err != nil {
		return err
	}
	fields, values := haConfigView(h)
	return o.WriteSingle(w, fields, values)
}

func newHAMarkers(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "markers",
		Short: "List available host-aggregate markers",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			sc, err := newKeyVRMClient(cmd.Context(), a)
			if err != nil {
				return err
			}
			markers, err := getMarkers(cmd.Context(), sc)
			if err != nil {
				return err
			}
			rows := make([][]any, len(markers))
			for i, m := range markers {
				rows[i] = []any{m}
			}
			return o.WriteList(cmd.OutOrStdout(), output.Table{Columns: []string{"Marker"}, Rows: rows})
		},
	}
}

// host-aggregate-config event list <ha-id>
func newHAEventCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "event", Short: "Host-aggregate events"}
	f := &eventListFlags{}
	list := &cobra.Command{
		Use:   "list <ha-id>",
		Short: "List events for a host aggregate",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			sc, err := newKeyVRMClient(cmd.Context(), a)
			if err != nil {
				return err
			}
			opts := listOpts{Limit: f.limit, Offset: f.offset, filters: map[string]string{"status": f.status}}
			p, err := listHostAggregateEvents(cmd.Context(), sc, args[0], opts)
			if err != nil {
				return err
			}
			rows := make([][]any, len(p.Data))
			for i, e := range p.Data {
				rows[i] = eventRow(e)
			}
			writeTotal(p.Total, p.Limit, p.Offset)
			return o.WriteList(cmd.OutOrStdout(), output.Table{Columns: eventColumns, Rows: rows})
		},
	}
	list.Flags().StringVar(&f.status, "status", "", "filter by event status")
	list.Flags().IntVar(&f.limit, "limit", 50, "page limit")
	list.Flags().IntVar(&f.offset, "offset", 0, "page offset")
	cmd.AddCommand(list)
	return cmd
}

type eventListFlags struct {
	status        string
	limit, offset int
}

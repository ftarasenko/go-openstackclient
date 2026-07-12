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

func newAvailabilityZoneCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "availability-zone", Short: "KeyVRM availability zones"}
	cmd.AddCommand(newAZList(a, o), newAZHostAggregateCommand(a, o))
	return cmd
}

func newAZList(a *auth.Options, o *output.Options) *cobra.Command {
	var limit, offset int
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List availability zones",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			sc, err := newKeyVRMClient(cmd.Context(), a)
			if err != nil {
				return err
			}
			return runAZList(cmd.Context(), sc, o, listOpts{Limit: limit, Offset: offset}, cmd.OutOrStdout())
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 50, "page limit")
	cmd.Flags().IntVar(&offset, "offset", 0, "page offset")
	return cmd
}

func runAZList(ctx context.Context, sc *gophercloud.ServiceClient, o *output.Options, opts listOpts, w io.Writer) error {
	p, err := listAvailabilityZones(ctx, sc, opts)
	if err != nil {
		return err
	}
	rows := make([][]any, len(p.Data))
	for i, z := range p.Data {
		rows[i] = azRow(z)
	}
	writeTotal(p.Total, p.Limit, p.Offset)
	return o.WriteList(w, output.Table{Columns: azColumns, Rows: rows})
}

// availability-zone host-aggregate list <az-name>
func newAZHostAggregateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "host-aggregate", Short: "Host aggregates within a zone"}
	var haName string
	var noOp bool
	var limit, offset int
	list := &cobra.Command{
		Use:   "list <az-name>",
		Short: "List host aggregates in an availability zone",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			sc, err := newKeyVRMClient(cmd.Context(), a)
			if err != nil {
				return err
			}
			opts := listOpts{Limit: limit, Offset: offset, filters: map[string]string{"host_aggregate_name": haName}}
			if cmd.Flags().Changed("no-op") {
				opts.filters["no_op_mode"] = strconv.FormatBool(noOp)
			}
			p, err := listZoneHostAggregates(cmd.Context(), sc, args[0], opts)
			if err != nil {
				return err
			}
			rows := make([][]any, len(p.Data))
			for i, h := range p.Data {
				rows[i] = haConfigRow(h)
			}
			writeTotal(p.Total, p.Limit, p.Offset)
			return o.WriteList(cmd.OutOrStdout(), output.Table{Columns: haConfigColumns, Rows: rows})
		},
	}
	list.Flags().StringVar(&haName, "ha-name", "", "filter by host aggregate name")
	list.Flags().BoolVar(&noOp, "no-op", false, "filter by no-op mode")
	list.Flags().IntVar(&limit, "limit", 50, "page limit")
	list.Flags().IntVar(&offset, "offset", 0, "page offset")
	cmd.AddCommand(list)
	return cmd
}

package keyvrm

import (
	"context"
	"fmt"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

func newRecommendationCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "recommendation", Short: "KeyVRM recommendations"}
	cmd.AddCommand(
		newRecList(a, o),
		newRecShow(a, o),
		newRecOperationCommand(a, o),
		newRecAction(a, o, "run", "Run a recommendation", runRecommendation, "started"),
		newRecAction(a, o, "stop", "Stop a recommendation", stopRecommendation, "stopped"),
	)
	return cmd
}

func newRecList(a *auth.Options, o *output.Options) *cobra.Command {
	var eventID, status string
	var limit, offset int
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List recommendations",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			sc, err := newKeyVRMClient(cmd.Context(), a)
			if err != nil {
				return err
			}
			opts := listOpts{Limit: limit, Offset: offset, filters: map[string]string{
				"host_aggregate_event_id": eventID,
				"status":                  status,
			}}
			p, err := listRecommendations(cmd.Context(), sc, opts)
			if err != nil {
				return err
			}
			rows := make([][]any, len(p.Data))
			for i, r := range p.Data {
				rows[i] = recRow(r)
			}
			writeTotal(p.Total, p.Limit, p.Offset)
			return o.WriteList(cmd.OutOrStdout(), output.Table{Columns: recColumns, Rows: rows})
		},
	}
	cmd.Flags().StringVar(&eventID, "event-id", "", "filter by host-aggregate event ID")
	cmd.Flags().StringVar(&status, "status", "", "filter by status")
	cmd.Flags().IntVar(&limit, "limit", 50, "page limit")
	cmd.Flags().IntVar(&offset, "offset", 0, "page offset")
	return cmd
}

func newRecShow(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <rec-id>",
		Short: "Show a recommendation",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			sc, err := newKeyVRMClient(cmd.Context(), a)
			if err != nil {
				return err
			}
			r, err := getRecommendation(cmd.Context(), sc, args[0])
			if err != nil {
				return err
			}
			fields, values := recView(r)
			return o.WriteSingle(cmd.OutOrStdout(), fields, values)
		},
	}
}

// recommendation operation list <rec-id>
func newRecOperationCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "operation", Short: "Operations executed for a recommendation"}
	var status string
	var limit, offset int
	list := &cobra.Command{
		Use:   "list <rec-id>",
		Short: "List operations for a recommendation",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			sc, err := newKeyVRMClient(cmd.Context(), a)
			if err != nil {
				return err
			}
			opts := listOpts{Limit: limit, Offset: offset, filters: map[string]string{"status": status}}
			p, err := listRecommendationOperations(cmd.Context(), sc, args[0], opts)
			if err != nil {
				return err
			}
			rows := make([][]any, len(p.Data))
			for i, op := range p.Data {
				rows[i] = opRow(op)
			}
			writeTotal(p.Total, p.Limit, p.Offset)
			return o.WriteList(cmd.OutOrStdout(), output.Table{Columns: opColumns, Rows: rows})
		},
	}
	list.Flags().StringVar(&status, "status", "", "filter by operation status")
	list.Flags().IntVar(&limit, "limit", 50, "page limit")
	list.Flags().IntVar(&offset, "offset", 0, "page offset")
	cmd.AddCommand(list)
	return cmd
}

// newRecAction builds a simple run/stop trigger command.
func newRecAction(a *auth.Options, _ *output.Options, use, short string,
	action func(context.Context, *gophercloud.ServiceClient, string) error, past string,
) *cobra.Command {
	return &cobra.Command{
		Use:   use + " <rec-id>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sc, err := newKeyVRMClient(cmd.Context(), a)
			if err != nil {
				return err
			}
			if err := action(cmd.Context(), sc, args[0]); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Recommendation %s %s.\n", args[0], past)
			return err
		},
	}
}

package keyvrm

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

func newEventCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "event", Short: "KeyVRM host-aggregate events"}
	cmd.AddCommand(newEventShow(a, o), newEventRecommendationCommand(a, o))
	return cmd
}

func newEventShow(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <event-id>",
		Short: "Show a host-aggregate event",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			sc, err := newKeyVRMClient(cmd.Context(), a)
			if err != nil {
				return err
			}
			e, err := getEvent(cmd.Context(), sc, args[0])
			if err != nil {
				return err
			}
			fields, values := eventView(e)
			return o.WriteSingle(cmd.OutOrStdout(), fields, values)
		},
	}
}

// event recommendation list <event-id> | run <event-id>
func newEventRecommendationCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "recommendation", Short: "Recommendations for an event"}

	f := &eventListFlags{}
	list := &cobra.Command{
		Use:   "list <event-id>",
		Short: "List recommendations generated for an event",
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
			p, err := listEventRecommendations(cmd.Context(), sc, args[0], opts)
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
	list.Flags().StringVar(&f.status, "status", "", "filter by recommendation status")
	list.Flags().IntVar(&f.limit, "limit", 50, "page limit")
	list.Flags().IntVar(&f.offset, "offset", 0, "page offset")

	run := &cobra.Command{
		Use:   "run <event-id>",
		Short: "Run all recommendations for an event",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sc, err := newKeyVRMClient(cmd.Context(), a)
			if err != nil {
				return err
			}
			if err := runEventRecommendations(cmd.Context(), sc, args[0]); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "All recommendations for event %s triggered.\n", args[0])
			return err
		},
	}

	cmd.AddCommand(list, run)
	return cmd
}

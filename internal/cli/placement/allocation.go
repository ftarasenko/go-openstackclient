package placement

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/placement/v1/allocations"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

func newProviderAllocationDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <consumer_uuid> [<consumer_uuid> ...]",
		Short: "Delete all allocations for a consumer",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newPlacementClient(ctx, a)
			if err != nil {
				return err
			}
			return runProviderAllocationDelete(ctx, client, args, cmd.OutOrStdout())
		},
	}
	return cmd
}

func runProviderAllocationDelete(ctx context.Context, client *gophercloud.ServiceClient, consumers []string, w io.Writer) error {
	var errs []error
	for _, c := range consumers {
		if err := allocations.Delete(ctx, client, c).ExtractErr(); err != nil {
			errs = append(errs, fmt.Errorf("deleting allocations for consumer %s: %w", c, err))
			continue
		}
		if _, err := fmt.Fprintf(w, "Deleted allocations for consumer %s\n", c); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

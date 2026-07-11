package server

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/hypervisors"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// newHypervisorCommand builds the "hypervisor" command group.
func newHypervisorCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hypervisor",
		Short: "Compute hypervisor commands",
	}
	cmd.AddCommand(newHypervisorListCommand(a, o))
	return cmd
}

func newHypervisorListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List hypervisors",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newComputeClient(ctx, a)
			if err != nil {
				return err
			}
			return runHypervisorList(ctx, client, o, cmd.OutOrStdout())
		},
	}
	return cmd
}

func runHypervisorList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, w io.Writer) error {
	pages, err := hypervisors.List(client, nil).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing hypervisors: %w", err)
	}
	all, err := hypervisors.ExtractHypervisors(pages)
	if err != nil {
		return fmt.Errorf("parsing hypervisor list: %w", err)
	}
	t := output.Table{
		Columns: []string{"ID", "Hypervisor Hostname", "Type", "Host IP", "State", "Status"},
		Rows:    make([][]any, 0, len(all)),
	}
	for _, h := range all {
		t.Rows = append(t.Rows, []any{h.ID, h.HypervisorHostname, h.HypervisorType, h.HostIP, h.State, h.Status})
	}
	return o.WriteList(w, t)
}

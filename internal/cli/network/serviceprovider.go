package network

import (
	"context"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
	networkapi "github.com/ftarasenko/go-openstackclient/pkg/openstack/network"
)

// newServiceProviderCommand builds "network service provider ...", upstream's
// network_service_provider.py. Neutron's service-providers resource is
// read-only and list-only.
func newServiceProviderCommand(a *auth.Options, o *output.Options) *cobra.Command {
	provider := &cobra.Command{
		Use:   "provider",
		Short: "Show neutron service providers",
	}
	provider.AddCommand(newServiceProviderListCommand(a, o))
	service := &cobra.Command{
		Use:   "service",
		Short: "Show neutron service providers",
	}
	service.AddCommand(provider)
	return service
}

func newServiceProviderListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List service providers",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			return runServiceProviderList(ctx, client, o, cmd.OutOrStdout())
		},
	}
}

func runServiceProviderList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, w io.Writer) error {
	all, err := networkapi.ListServiceProviders(ctx, client)
	if err != nil {
		return err
	}
	t := output.Table{Columns: []string{"Service Type", "Name", "Default"}, Rows: make([][]any, 0, len(all))}
	for _, sp := range all {
		t.Rows = append(t.Rows, []any{sp.ServiceType, sp.Name, sp.Default})
	}
	return o.WriteList(w, t)
}

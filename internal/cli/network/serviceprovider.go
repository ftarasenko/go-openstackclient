package network

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
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

type serviceProvider struct {
	ServiceType string `json:"service_type"`
	Name        string `json:"name"`
	Default     bool   `json:"default"`
}

// listServiceProviders is GET /service-providers. gophercloud has no package
// for the resource, so it is one raw call, isolated here.
func listServiceProviders(ctx context.Context, client *gophercloud.ServiceClient) ([]serviceProvider, error) {
	var body struct {
		ServiceProviders []serviceProvider `json:"service_providers"`
	}
	resp, err := client.Get(ctx, client.ServiceURL("service-providers"), &body, nil)
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if _, _, err = gophercloud.ParseResponse(resp, err); err != nil {
		return nil, err
	}
	return body.ServiceProviders, nil
}

func runServiceProviderList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, w io.Writer) error {
	all, err := listServiceProviders(ctx, client)
	if err != nil {
		return fmt.Errorf("listing service providers: %w", err)
	}
	t := output.Table{Columns: []string{"Service Type", "Name", "Default"}, Rows: make([][]any, 0, len(all))}
	for _, sp := range all {
		t.Rows = append(t.Rows, []any{sp.ServiceType, sp.Name, sp.Default})
	}
	return o.WriteList(w, t)
}

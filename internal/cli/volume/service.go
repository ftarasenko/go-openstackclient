package volume

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/services"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// newServiceCommand builds "volume service ...".
//
// Flag names follow upstream OSC (`openstack volume service list`); the KeyStack
// reference (docs.keystack.ru) returned HTTP 403 at implementation time, so the
// surface is UNVERIFIED against KeyStack and falls back to upstream OSC.
func newServiceCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Manage block storage services",
	}
	cmd.AddCommand(newServiceListCommand(a, o))
	return cmd
}

type serviceListFlags struct {
	host    string
	service string
}

func newServiceListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &serviceListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List block storage services",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newVolumeClient(ctx, a)
			if err != nil {
				return err
			}
			return runServiceList(ctx, client, o, f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.host, "host", "", "filter by service host")
	fl.StringVar(&f.service, "service", "", "filter by service binary name (e.g. cinder-volume)")
	return cmd
}

func runServiceList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *serviceListFlags, w io.Writer) error {
	opts := services.ListOpts{
		Host:   f.host,
		Binary: f.service,
	}
	pages, err := services.List(client, opts).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing volume services: %w", err)
	}
	all, err := services.ExtractServices(pages)
	if err != nil {
		return fmt.Errorf("parsing volume service list: %w", err)
	}
	t := output.Table{Columns: []string{"Binary", "Host", "Zone", "Status", "State", "Updated At"}}
	for _, s := range all {
		t.Rows = append(t.Rows, []any{s.Binary, s.Host, s.Zone, s.Status, s.State, s.UpdatedAt})
	}
	return o.WriteList(w, t)
}

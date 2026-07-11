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
	cmd.AddCommand(newServiceListCommand(a, o), newServiceSetCommand(a, o))
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

type serviceSetFlags struct {
	enable        bool
	disable       bool
	disableReason string
}

func newServiceSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &serviceSetFlags{}
	cmd := &cobra.Command{
		Use:   "set <host> <binary>",
		Short: "Enable or disable a block storage service",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			if f.enable && f.disable {
				return fmt.Errorf("--enable and --disable are mutually exclusive")
			}
			if !f.enable && !f.disable {
				return fmt.Errorf("nothing to do: pass --enable or --disable")
			}
			ctx := cmd.Context()
			client, err := newVolumeClient(ctx, a)
			if err != nil {
				return err
			}
			return runServiceSet(ctx, client, args[0], args[1], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&f.enable, "enable", false, "enable the service")
	fl.BoolVar(&f.disable, "disable", false, "disable the service")
	fl.StringVar(&f.disableReason, "disable-reason", "", "reason for disabling the service (implies --disable)")
	return cmd
}

// runServiceSet toggles a cinder service. gophercloud v2's blockstorage/v3/services
// package only exposes List, so this uses the raw os-services enable/disable
// endpoints directly (isolated here so it is easy to replace with a typed call
// if one is added upstream).
func runServiceSet(ctx context.Context, client *gophercloud.ServiceClient, host, binary string, f *serviceSetFlags, w io.Writer) error {
	body := map[string]any{"host": host, "binary": binary}
	action := "enable"
	if f.disable || f.disableReason != "" {
		action = "disable"
		if f.disableReason != "" {
			action = "disable-log-reason"
			body["disabled_reason"] = f.disableReason
		}
	}

	url := client.ServiceURL("os-services", action)
	resp, err := client.Put(ctx, url, body, nil, &gophercloud.RequestOpts{OkCodes: []int{200}})
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if _, _, err = gophercloud.ParseResponse(resp, err); err != nil {
		return fmt.Errorf("setting block storage service %s/%s: %w", host, binary, err)
	}
	if _, err := fmt.Fprintf(w, "Updated block storage service %s on host %s\n", binary, host); err != nil {
		return err
	}
	return nil
}

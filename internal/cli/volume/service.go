package volume

import (
	"context"
	"fmt"
	"io"
	"slices"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/services"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/koc/internal/auth"
	"github.com/ftarasenko/koc/internal/output"
	volumeapi "github.com/ftarasenko/koc/pkg/openstack/volume"
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
	long    bool
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
	fl.BoolVar(&f.long, "long", false, "list additional fields in output")
	fl.StringVar(&f.host, "host", "", "filter by service host")
	fl.StringVar(&f.service, "service", "", "filter by service binary name (e.g. cinder-volume)")
	return cmd
}

func runServiceList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *serviceListFlags, w io.Writer) error {
	opts := services.ListOpts{
		Host:   f.host,
		Binary: f.service,
	}
	all, err := volumeapi.ListServices(ctx, client, opts)
	if err != nil {
		return err
	}
	cols := serviceColumns{
		cluster:      volumeSupportsMicroversion(client, volumeapi.ServiceClusterMicroversion),
		backendState: volumeSupportsMicroversion(client, volumeapi.ServiceBackendStateMicroversion),
		long:         f.long,
	}
	return o.WriteList(w, serviceListTable(all, cols))
}

// serviceColumns says which of the listing's conditional columns to render.
type serviceColumns struct {
	cluster      bool
	backendState bool
	long         bool
}

// serviceListTable renders the block-storage service listing.
//
// Cluster and Backend State follow upstream OSC: they are gated on the
// negotiated microversion (3.7 and 3.49), not on --long, because below those
// versions cinder does not report the field at all and above them it always
// does.
//
// Disabled Reason is upstream's only --long column. koc also shows it whenever
// a listed service actually carries one, matching what "koc compute service
// list" does for nova and for the same reason: it is the read side of
// "koc volume service set --disable-reason", and a reason you cannot read back
// without knowing to pass --long is easy to miss on a host an HA agent
// disabled. When nothing is disabled the column would be a blank strip, so it
// stays out and the vanilla listing is unchanged.
func serviceListTable(list []volumeapi.Service, cols serviceColumns) output.Table {
	columns := []string{"Binary", "Host", "Zone", "Status", "State", "Updated At"}
	if cols.cluster {
		columns = append(columns, "Cluster")
	}
	if cols.backendState {
		columns = append(columns, "Backend State")
	}
	reasons := cols.long || slices.ContainsFunc(list, func(s volumeapi.Service) bool {
		return s.DisabledReason != ""
	})
	if reasons {
		columns = append(columns, "Disabled Reason")
	}
	t := output.Table{Columns: columns, Rows: make([][]any, 0, len(list))}
	for _, s := range list {
		row := []any{s.Binary, s.Host, s.Zone, s.Status, s.State, s.UpdatedAt}
		if cols.cluster {
			row = append(row, s.Cluster)
		}
		if cols.backendState {
			row = append(row, s.BackendState)
		}
		if reasons {
			row = append(row, s.DisabledReason)
		}
		t.Rows = append(t.Rows, row)
	}
	return t
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

// runServiceSet toggles a cinder service.
func runServiceSet(ctx context.Context, client *gophercloud.ServiceClient, host, binary string, f *serviceSetFlags, w io.Writer) error {
	err := volumeapi.UpdateService(ctx, client, volumeapi.ServiceUpdateOpts{
		Host: host, Binary: binary, Disabled: f.disable, DisabledReason: f.disableReason,
	})
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Updated block storage service %s on host %s\n", binary, host); err != nil {
		return err
	}
	return nil
}

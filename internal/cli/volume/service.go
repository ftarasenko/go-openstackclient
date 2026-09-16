package volume

import (
	"context"
	"fmt"
	"io"
	"slices"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/services"
	"github.com/gophercloud/gophercloud/v2/pagination"
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

// Microversions that gate two of the listing's columns. Both are well inside
// the Zed cap of 3.70, and koc negotiates "latest" by default, so the common
// path carries them; an operator who pins --os-volume-api-version below either
// one gets the same listing upstream OSC would show there.
const (
	// serviceClusterMicroversion gates the cluster name (cinder 3.7 added
	// clustered services).
	serviceClusterMicroversion = "3.7"
	// serviceBackendStateMicroversion gates backend_state, the driver's own
	// up/down view of the storage behind a cinder-volume service.
	serviceBackendStateMicroversion = "3.49"
)

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
	pages, err := services.List(client, opts).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing volume services: %w", err)
	}
	all, err := services.ExtractServices(pages)
	if err != nil {
		return fmt.Errorf("parsing volume service list: %w", err)
	}
	// gophercloud's Service type has no backend_state field, so it is pulled
	// raw and aligned by index (same "services" array, same order).
	ext, err := extractServiceExt(pages)
	if err != nil {
		return fmt.Errorf("parsing volume service list: %w", err)
	}
	cols := serviceColumns{
		cluster:      volumeSupportsMicroversion(client, serviceClusterMicroversion),
		backendState: volumeSupportsMicroversion(client, serviceBackendStateMicroversion),
		long:         f.long,
	}
	return o.WriteList(w, serviceListTable(all, ext, cols))
}

// serviceExt carries the os-services response fields gophercloud's Service type
// drops.
type serviceExt struct {
	BackendState string `json:"backend_state"`
}

func extractServiceExt(page pagination.Page) ([]serviceExt, error) {
	var s struct {
		Services []serviceExt `json:"services"`
	}
	sp, ok := page.(services.ServicePage)
	if !ok {
		return nil, fmt.Errorf("extractServiceExt: unexpected page type %T", page)
	}
	err := sp.ExtractInto(&s)
	return s.Services, err
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
func serviceListTable(list []services.Service, ext []serviceExt, cols serviceColumns) output.Table {
	columns := []string{"Binary", "Host", "Zone", "Status", "State", "Updated At"}
	if cols.cluster {
		columns = append(columns, "Cluster")
	}
	if cols.backendState {
		columns = append(columns, "Backend State")
	}
	reasons := cols.long || slices.ContainsFunc(list, func(s services.Service) bool {
		return s.DisabledReason != ""
	})
	if reasons {
		columns = append(columns, "Disabled Reason")
	}
	t := output.Table{Columns: columns, Rows: make([][]any, 0, len(list))}
	for i, s := range list {
		row := []any{s.Binary, s.Host, s.Zone, s.Status, s.State, s.UpdatedAt}
		if cols.cluster {
			row = append(row, s.Cluster)
		}
		if cols.backendState {
			var e serviceExt
			if i < len(ext) {
				e = ext[i]
			}
			row = append(row, e.BackendState)
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

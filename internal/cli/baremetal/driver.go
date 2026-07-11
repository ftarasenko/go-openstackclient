package baremetal

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/baremetal/v1/drivers"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// newDriverCommand builds "baremetal driver ...".
func newDriverCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "driver",
		Short: "Manage baremetal drivers",
	}
	cmd.AddCommand(newDriverListCommand(a, o))
	return cmd
}

// driverListFlags holds the filters accepted by "driver list".
//
// Flag names follow upstream OSC (`openstack baremetal driver list`). The
// KeyStack command reference at https://docs.keystack.ru/ was not reachable at
// implementation time (HTTP 403), so these are UNVERIFIED against KeyStack and
// fall back to upstream OSC semantics.
type driverListFlags struct {
	long bool
	typ  string
}

func newDriverListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &driverListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List baremetal drivers",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runDriverList(ctx, client, o, f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&f.long, "long", false, "list additional fields in output")
	fl.StringVar(&f.typ, "type", "", "limit to drivers of this type (classic or dynamic)")
	return cmd
}

func runDriverList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *driverListFlags, w io.Writer) error {
	opts := drivers.ListDriversOpts{Type: f.typ}
	if f.long {
		opts.Detail = true
	}
	pages, err := drivers.ListDrivers(client, opts).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing baremetal drivers: %w", err)
	}
	all, err := drivers.ExtractDrivers(pages)
	if err != nil {
		return fmt.Errorf("parsing baremetal driver list: %w", err)
	}
	return o.WriteList(w, driverListTable(all, f.long))
}

func driverListTable(list []drivers.Driver, long bool) output.Table {
	cols := []string{"Supported driver(s)", "Active host(s)"}
	if long {
		cols = append(cols, "Type", "Default Deploy Interface", "Default Boot Interface")
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(list))}
	for _, d := range list {
		row := []any{d.Name, d.Hosts}
		if long {
			row = append(row, d.Type, d.DefaultDeployInterface, d.DefaultBootInterface)
		}
		t.Rows = append(t.Rows, row)
	}
	return t
}

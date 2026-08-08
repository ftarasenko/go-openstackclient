package baremetal

import (
	"context"
	"fmt"
	"io"
	"sort"

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
	cmd.AddCommand(newDriverShowCommand(a, o))
	cmd.AddCommand(newDriverPropertyCommand(a, o))
	cmd.AddCommand(newDriverRAIDCommand(a, o))
	return cmd
}

func newDriverShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <driver>",
		Short: "Show baremetal driver details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runDriverShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
	return cmd
}

func runDriverShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, name string, w io.Writer) error {
	d, err := drivers.GetDriverDetails(ctx, client, name).Extract()
	if err != nil {
		return fmt.Errorf("showing baremetal driver %s: %w", name, err)
	}
	fields, values := driverShowFields(d)
	return o.WriteSingle(w, fields, values)
}

// driverShowFields is the Field/Value view of a single driver, mirroring
// `openstack baremetal driver show`: the hosts running it plus every
// default_*_interface / enabled_*_interfaces pair.
func driverShowFields(d *drivers.Driver) ([]string, []any) {
	fields := []string{"name", "hosts", "type"}
	values := []any{d.Name, d.Hosts, d.Type}
	// Interface families, in the order upstream renders them. Each contributes a
	// default_<x>_interface / enabled_<x>_interfaces pair.
	ifaces := []struct {
		name    string
		def     string
		enabled []string
	}{
		{"bios", d.DefaultBiosInterface, d.EnabledBiosInterfaces},
		{"boot", d.DefaultBootInterface, d.EnabledBootInterfaces},
		{"console", d.DefaultConsoleInterface, d.EnabledConsoleInterface},
		{"deploy", d.DefaultDeployInterface, d.EnabledDeployInterfaces},
		{"firmware", d.DefaultFirmwareInterface, d.EnabledFirmwareInterfaces},
		{"inspect", d.DefaultInspectInterface, d.EnabledInspectInterfaces},
		{"management", d.DefaultManagementInterface, d.EnabledManagementInterfaces},
		{"network", d.DefaultNetworkInterface, d.EnabledNetworkInterfaces},
		{"power", d.DefaultPowerInterface, d.EnabledPowerInterfaces},
		{"raid", d.DefaultRaidInterface, d.EnabledRaidInterfaces},
		{"rescue", d.DefaultRescueInterface, d.EnabledRescueInterfaces},
		{"storage", d.DefaultStorageInterface, d.EnabledStorageInterfaces},
		{"vendor", d.DefaultVendorInterface, d.EnabledVendorInterfaces},
	}
	for _, i := range ifaces {
		fields = append(fields, "default_"+i.name+"_interface", "enabled_"+i.name+"_interfaces")
		values = append(values, i.def, i.enabled)
	}
	fields = append(fields, "properties")
	values = append(values, d.Properties)
	return fields, values
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

// --- driver property list / raid property list -------------------------------

// newDriverPropertyCommand builds "baremetal driver property list", and
// newDriverRAIDCommand "baremetal driver raid property list". Both render the
// same shape — ironic returns a flat {property: description} object — so they
// share a renderer and differ only in the endpoint.
func newDriverPropertyCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "property",
		Short: "Inspect the driver_info properties a driver expects",
	}
	cmd.AddCommand(newDriverPropertyListCommand(a, o))
	return cmd
}

func newDriverPropertyListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "list <driver>",
		Short: "List the driver_info properties a driver expects",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runDriverPropertyList(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runDriverPropertyList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, name string, w io.Writer) error {
	props, err := drivers.GetDriverProperties(ctx, client, name).Extract()
	if err != nil {
		return fmt.Errorf("listing properties of baremetal driver %s: %w", name, err)
	}
	return o.WriteList(w, propertyTable(*props))
}

func newDriverRAIDCommand(a *auth.Options, o *output.Options) *cobra.Command {
	property := &cobra.Command{
		Use:   "property",
		Short: "Inspect the target_raid_config properties a driver expects",
	}
	property.AddCommand(&cobra.Command{
		Use:   "list <driver>",
		Short: "List the target_raid_config properties a driver expects",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runDriverRAIDPropertyList(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	})
	cmd := &cobra.Command{
		Use:   "raid",
		Short: "Inspect a driver's RAID capabilities",
	}
	cmd.AddCommand(property)
	return cmd
}

func runDriverRAIDPropertyList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, name string, w io.Writer) error {
	props, err := drivers.GetDriverDiskProperties(ctx, client, name).Extract()
	if err != nil {
		return fmt.Errorf("listing RAID properties of baremetal driver %s: %w", name, err)
	}
	return o.WriteList(w, propertyTable(*props))
}

// propertyTable renders ironic's flat {property: description} object as a
// two-column listing, sorted by property name — the API returns a JSON object,
// whose member order is not meaningful, so an arbitrary order would make the
// output differ run to run.
func propertyTable[M ~map[string]any](props M) output.Table {
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	sort.Strings(names)
	t := output.Table{Columns: []string{"Property", "Description"}, Rows: make([][]any, 0, len(names))}
	for _, name := range names {
		t.Rows = append(t.Rows, []any{name, props[name]})
	}
	return t
}

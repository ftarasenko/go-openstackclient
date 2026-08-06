package baremetal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/baremetal/v1/nodes"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// newNodeInventoryCommand builds "baremetal node inventory ...", the ironic-native
// stored-inspection-data reads (API >= 1.81).
func newNodeInventoryCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "inventory",
		Short: "Show or save a node's stored hardware inventory",
	}
	cmd.AddCommand(newNodeInventorySaveCommand(a, o))
	cmd.AddCommand(newNodeInventoryShowCommand(a, o))
	return cmd
}

// nodeInventorySaveFlags holds the options accepted by "node inventory save".
//
// Flag names follow upstream OSC / python-ironicclient
// (`openstack baremetal node inventory save`). The KeyStack command reference at
// https://docs.keystack.ru/ was not reachable at implementation time (HTTP 403),
// so these are UNVERIFIED against KeyStack and fall back to upstream semantics.
type nodeInventorySaveFlags struct {
	file string
}

func newNodeInventorySaveCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &nodeInventorySaveFlags{}
	cmd := &cobra.Command{
		Use:   "save <node>",
		Short: "Save a node's stored inventory as JSON to a file or stdout",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// "inventory save" emits raw JSON rather than a formatted table, but the
			// (unused) format flag is still validated for consistency.
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runNodeInventorySave(ctx, client, args[0], f, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&f.file, "file", "", "write the inventory JSON to this path (default: stdout)")
	return cmd
}

// runNodeInventorySave streams the node's raw inventory document to --file or,
// when unset, to w (stdout). The raw gophercloud result body is re-encoded so
// nothing the API returns is dropped by koc's typed structs.
func runNodeInventorySave(ctx context.Context, client *gophercloud.ServiceClient, id string, f *nodeInventorySaveFlags, w io.Writer) (err error) {
	res := nodes.GetInventory(ctx, client, id)
	if res.Err != nil {
		return fmt.Errorf("getting inventory for node %s: %w", id, res.Err)
	}

	dst := w
	if f.file != "" {
		out, cerr := os.Create(f.file)
		if cerr != nil {
			return fmt.Errorf("creating output file %q: %w", f.file, cerr)
		}
		defer func() {
			if closeErr := out.Close(); closeErr != nil && err == nil {
				err = fmt.Errorf("closing output file %q: %w", f.file, closeErr)
			}
		}()
		dst = out
	}

	enc := json.NewEncoder(dst)
	enc.SetIndent("", "  ")
	if cerr := enc.Encode(res.Body); cerr != nil {
		return fmt.Errorf("writing inventory JSON: %w", cerr)
	}
	return nil
}

func newNodeInventoryShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <node>",
		Short: "Show a summary of a node's stored inventory",
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
			return runNodeInventoryShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
	return cmd
}

func runNodeInventoryShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, id string, w io.Writer) error {
	data, err := nodes.GetInventory(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("getting inventory for node %s: %w", id, err)
	}
	inv := data.Inventory
	fields := []string{
		"hostname", "bmc_address", "cpu_architecture", "cpu_count", "cpu_model_name",
		"memory_physical_mb", "system_vendor", "system_product", "system_serial",
		"boot_mode", "boot_pxe_interface", "disk_count", "interface_count",
	}
	values := []any{
		inv.Hostname, inv.BmcAddress, inv.CPU.Architecture, inv.CPU.Count, inv.CPU.ModelName,
		inv.Memory.PhysicalMb, inv.SystemVendor.Manufacturer, inv.SystemVendor.ProductName,
		inv.SystemVendor.SerialNumber, inv.Boot.CurrentBootMode, inv.Boot.PXEInterface,
		len(inv.Disks), len(inv.Interfaces),
	}
	return o.WriteSingle(w, fields, values)
}

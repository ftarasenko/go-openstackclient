package baremetal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/baremetalintrospection/v1/introspection"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/paging"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// newIntrospectionClient derives the ironic-inspector client. The inspector is a
// separate Keystone catalog entry ("baremetal-introspection") from ironic, so
// these commands cannot reuse newBaremetalClient.
func newIntrospectionClient(ctx context.Context, a *auth.Options) (*gophercloud.ServiceClient, error) {
	return a.NewServiceClient(ctx, (*auth.Client).Introspection)
}

// newIntrospectionCommand builds "baremetal introspection ...".
//
// Verb names follow upstream python-ironicclient
// (`openstack baremetal introspection …`). The KeyStack command reference at
// https://docs.keystack.ru/ was not reachable at implementation time (HTTP 403),
// so these are UNVERIFIED against KeyStack and fall back to upstream semantics.
func newIntrospectionCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "introspection",
		Short: "Manage baremetal introspection (ironic-inspector)",
	}
	cmd.AddCommand(newIntrospectionStartCommand(a, o))
	cmd.AddCommand(newIntrospectionStatusCommand(a, o))
	cmd.AddCommand(newIntrospectionListCommand(a, o))
	cmd.AddCommand(newIntrospectionAbortCommand(a, o))
	cmd.AddCommand(newIntrospectionDataCommand(a, o))
	cmd.AddCommand(newIntrospectionInterfaceCommand(a, o))
	return cmd
}

// --- start -----------------------------------------------------------------

type introspectionStartFlags struct {
	manageBoot   bool
	noManageBoot bool
}

func newIntrospectionStartCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &introspectionStartFlags{}
	cmd := &cobra.Command{
		Use:   "start <node>",
		Short: "Start hardware introspection of a node",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			if f.manageBoot && f.noManageBoot {
				return fmt.Errorf("--manage-boot and --no-manage-boot are mutually exclusive")
			}
			ctx := cmd.Context()
			client, err := newIntrospectionClient(ctx, a)
			if err != nil {
				return err
			}
			return runIntrospectionStart(ctx, client, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&f.manageBoot, "manage-boot", false, "let the inspector manage PXE booting of the node")
	fl.BoolVar(&f.noManageBoot, "no-manage-boot", false, "the node's boot is managed externally")
	return cmd
}

func runIntrospectionStart(ctx context.Context, client *gophercloud.ServiceClient, id string, f *introspectionStartFlags, w io.Writer) error {
	opts := introspection.StartOpts{}
	switch {
	case f.manageBoot:
		opts.ManageBoot = gophercloud.Enabled
	case f.noManageBoot:
		opts.ManageBoot = gophercloud.Disabled
	}
	if err := startIntrospectionRaw(ctx, client, id, opts); err != nil {
		return fmt.Errorf("starting introspection of node %s: %w", id, err)
	}
	if _, err := fmt.Fprintf(w, "Started introspection of node %s\n", id); err != nil {
		return err
	}
	return nil
}

// startIntrospectionRaw POSTs to the inspector's introspection endpoint with the
// manage_boot query parameter actually attached.
//
// It replaces introspection.StartIntrospection, which at gophercloud v2.13.0
// builds the query string from StartOpts and then throws it away — it calls
// ToStartIntrospectionQuery only to surface an encoding error and POSTs to the
// bare URL, so --manage-boot / --no-manage-boot would be silently dropped.
// Delete this helper and go back to the typed call once that is fixed upstream.
func startIntrospectionRaw(ctx context.Context, client *gophercloud.ServiceClient, nodeID string, opts introspection.StartOpts) error {
	query, err := opts.ToStartIntrospectionQuery()
	if err != nil {
		return err
	}
	url := client.ServiceURL("introspection", nodeID) + query
	resp, err := client.Post(ctx, url, nil, nil, &gophercloud.RequestOpts{OkCodes: []int{202}})
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	_, _, err = gophercloud.ParseResponse(resp, err)
	return err
}

// --- status ----------------------------------------------------------------

func newIntrospectionStatusCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "status <node>",
		Short: "Show the introspection status of a node",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newIntrospectionClient(ctx, a)
			if err != nil {
				return err
			}
			return runIntrospectionStatus(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runIntrospectionStatus(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, id string, w io.Writer) error {
	st, err := introspection.GetIntrospectionStatus(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("getting introspection status of node %s: %w", id, err)
	}
	fields, values := introspectionFields(st)
	return o.WriteSingle(w, fields, values)
}

func introspectionFields(i *introspection.Introspection) ([]string, []any) {
	fields := []string{"uuid", "state", "finished", "started_at", "finished_at", "error"}
	values := []any{i.UUID, i.State, i.Finished, i.StartedAt, i.FinishedAt, i.Error}
	return fields, values
}

// --- list ------------------------------------------------------------------

type introspectionListFlags struct {
	limit  int
	marker string
}

func newIntrospectionListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &introspectionListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List introspection statuses",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newIntrospectionClient(ctx, a)
			if err != nil {
				return err
			}
			return runIntrospectionList(ctx, client, o, f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.IntVar(&f.limit, "limit", 0, "maximum number of introspections to return")
	fl.StringVar(&f.marker, "marker", "", "list introspections after this node UUID")
	return cmd
}

func runIntrospectionList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *introspectionListFlags, w io.Writer) error {
	opts := introspection.ListIntrospectionsOpts{Limit: f.limit, Marker: f.marker}
	// The inspector treats "limit" only as a page size, so --limit is enforced
	// as a hard result cap; Collect also stops paging once it is met.
	all, err := paging.Collect(ctx, introspection.ListIntrospections(client, opts), f.limit, introspection.ExtractIntrospections)
	if err != nil {
		return fmt.Errorf("listing introspections: %w", err)
	}

	t := output.Table{
		Columns: []string{"UUID", "Started at", "Finished at", "Error"},
		Rows:    make([][]any, 0, len(all)),
	}
	for _, i := range all {
		t.Rows = append(t.Rows, []any{i.UUID, i.StartedAt, i.FinishedAt, i.Error})
	}
	return o.WriteList(w, t)
}

// --- abort -----------------------------------------------------------------

func newIntrospectionAbortCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "abort <node>",
		Short: "Abort a running introspection",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newIntrospectionClient(ctx, a)
			if err != nil {
				return err
			}
			return runIntrospectionAbort(ctx, client, args[0], cmd.OutOrStdout())
		},
	}
}

func runIntrospectionAbort(ctx context.Context, client *gophercloud.ServiceClient, id string, w io.Writer) error {
	if err := introspection.AbortIntrospection(ctx, client, id).ExtractErr(); err != nil {
		return fmt.Errorf("aborting introspection of node %s: %w", id, err)
	}
	if _, err := fmt.Fprintf(w, "Aborted introspection of node %s\n", id); err != nil {
		return err
	}
	return nil
}

// --- data save -------------------------------------------------------------

func newIntrospectionDataCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "data",
		Short: "Access stored introspection data",
	}
	cmd.AddCommand(newIntrospectionDataSaveCommand(a, o))
	return cmd
}

type introspectionDataSaveFlags struct {
	file string
}

func newIntrospectionDataSaveCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &introspectionDataSaveFlags{}
	cmd := &cobra.Command{
		Use:   "save <node>",
		Short: "Save stored introspection data as JSON to a file or stdout",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Emits raw JSON rather than a formatted table, but the (unused) format
			// flag is still validated for consistency.
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newIntrospectionClient(ctx, a)
			if err != nil {
				return err
			}
			return runIntrospectionDataSave(ctx, client, args[0], f, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&f.file, "file", "", "write the introspection data to this path (default: stdout)")
	return cmd
}

// runIntrospectionDataSave dumps the untyped introspection blob so plugin-specific
// keys koc has no struct for survive the round trip.
func runIntrospectionDataSave(ctx context.Context, client *gophercloud.ServiceClient, id string, f *introspectionDataSaveFlags, w io.Writer) (err error) {
	res := introspection.GetIntrospectionData(ctx, client, id)
	if res.Err != nil {
		return fmt.Errorf("getting introspection data for node %s: %w", id, res.Err)
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
		return fmt.Errorf("writing introspection data: %w", cerr)
	}
	return nil
}

// --- interface list --------------------------------------------------------

func newIntrospectionInterfaceCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "interface",
		Short: "Inspect interfaces discovered by introspection",
	}
	cmd.AddCommand(newIntrospectionInterfaceListCommand(a, o))
	return cmd
}

type introspectionInterfaceListFlags struct {
	long bool
}

func newIntrospectionInterfaceListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &introspectionInterfaceListFlags{}
	cmd := &cobra.Command{
		Use:   useListNode,
		Short: "List interfaces discovered by introspection of a node",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newIntrospectionClient(ctx, a)
			if err != nil {
				return err
			}
			return runIntrospectionInterfaceList(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&f.long, "long", false, "add the LLDP switch chassis/port columns")
	return cmd
}

// runIntrospectionInterfaceList projects the interface view out of the stored
// introspection blob: the inspector has no interface endpoint of its own, so
// upstream reads /data and reshapes it the same way.
//
// all_interfaces carries every NIC the ramdisk saw (with processed LLDP),
// interfaces only the subset the inspector kept. The listing walks
// all_interfaces so operators see NICs that were filtered out, and marks
// which ones ironic ports were created for.
func runIntrospectionInterfaceList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	id string, f *introspectionInterfaceListFlags, w io.Writer,
) error {
	data, err := introspection.GetIntrospectionData(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("getting introspection data for node %s: %w", id, err)
	}

	src := data.AllInterfaces
	if len(src) == 0 {
		src = data.Interfaces
	}
	names := make([]string, 0, len(src))
	for name := range src {
		names = append(names, name)
	}
	sort.Strings(names)

	cols := []string{"Interface", "MAC Address", "Switch Port VLAN IDs", "Switch Chassis ID", "Switch Port ID"}
	if f.long {
		cols = []string{
			"Interface", "MAC Address", "Switch Port VLAN IDs", "Switch Chassis ID",
			"Switch Port ID", "Switch Port MAU Type", "Switch Port Link Aggregation Enabled",
			"IP Address", "PXE Enabled", "Node Port Created",
		}
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(names))}
	for _, name := range names {
		i := src[name]
		row := []any{
			name, i.MAC,
			lldpValue(i.LLDPProcessed, "switch_port_vlans"),
			lldpValue(i.LLDPProcessed, "switch_chassis_id"),
			lldpValue(i.LLDPProcessed, "switch_port_id"),
		}
		if f.long {
			_, kept := data.Interfaces[name]
			row = append(row,
				lldpValue(i.LLDPProcessed, "switch_port_mau_type"),
				lldpValue(i.LLDPProcessed, "switch_port_link_aggregation_enabled"),
				i.IP, i.PXE, kept,
			)
		}
		t.Rows = append(t.Rows, row)
	}
	return o.WriteList(w, t)
}

// lldpValue reads one processed-LLDP key, returning "" when the switch did not
// advertise it (common on NICs with no LLDP neighbour).
func lldpValue(lldp map[string]any, key string) any {
	if lldp == nil {
		return ""
	}
	v, ok := lldp[key]
	if !ok || v == nil {
		return ""
	}
	return v
}

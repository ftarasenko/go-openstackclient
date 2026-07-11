package compute

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/flavors"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// newFlavorCommand builds "flavor ...".
func newFlavorCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "flavor",
		Short: "Manage compute flavors",
	}
	cmd.AddCommand(
		newFlavorListCommand(a, o),
		newFlavorShowCommand(a, o),
		newFlavorCreateCommand(a, o),
		newFlavorDeleteCommand(a, o),
		newFlavorSetCommand(a, o),
		newFlavorUnsetCommand(a, o),
	)
	return cmd
}

// Flag names and semantics below follow upstream python-openstackclient
// (`openstack flavor ...`). The KeyStack command reference at
// https://docs.keystack.ru/ was not reachable at implementation time (HTTP
// 403), so these are UNVERIFIED against KeyStack and fall back to upstream OSC
// semantics — see the PR description.

// ---------------------------------------------------------------------------
// flavor list
// ---------------------------------------------------------------------------

type flavorListFlags struct {
	long   bool
	public bool // only public flavors (default view)
	all    bool // all flavors, public and private (admin)
}

func newFlavorListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &flavorListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List flavors",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newComputeClient(ctx, a)
			if err != nil {
				return err
			}
			return runFlavorList(ctx, client, o, f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&f.long, "long", false, "list additional fields in output")
	fl.BoolVar(&f.public, "public", false, "list only public flavors (default)")
	fl.BoolVar(&f.all, "all", false, "list all flavors, whether public or private (admin only)")
	return cmd
}

func runFlavorList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *flavorListFlags, w io.Writer) error {
	opts := flavors.ListOpts{}
	switch {
	case f.all:
		opts.AccessType = flavors.AllAccess
	case f.public:
		opts.AccessType = flavors.PublicAccess
	}

	pages, err := flavors.ListDetail(client, opts).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing flavors: %w", err)
	}
	all, err := flavors.ExtractFlavors(pages)
	if err != nil {
		return fmt.Errorf("parsing flavor list: %w", err)
	}
	return o.WriteList(w, flavorListTable(all, f.long))
}

func flavorListTable(list []flavors.Flavor, long bool) output.Table {
	cols := []string{"ID", "Name", "RAM", "Disk", "Ephemeral", "VCPUs", "Is Public"}
	if long {
		cols = append(cols, "Swap", "RXTX Factor", "Properties")
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(list))}
	for _, fl := range list {
		row := []any{fl.ID, fl.Name, fl.RAM, fl.Disk, fl.Ephemeral, fl.VCPUs, fl.IsPublic}
		if long {
			row = append(row, fl.Swap, fl.RxTxFactor, fl.ExtraSpecs)
		}
		t.Rows = append(t.Rows, row)
	}
	return t
}

// ---------------------------------------------------------------------------
// flavor show
// ---------------------------------------------------------------------------

func newFlavorShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <flavor>",
		Short: "Display flavor details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newComputeClient(ctx, a)
			if err != nil {
				return err
			}
			return runFlavorShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
	return cmd
}

func runFlavorShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, w io.Writer) error {
	id, err := resolveFlavorID(ctx, client, ref)
	if err != nil {
		return err
	}
	fl, err := flavors.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("showing flavor %q: %w", ref, err)
	}
	fields, values := flavorSingle(fl)
	return o.WriteSingle(w, fields, values)
}

func flavorSingle(fl *flavors.Flavor) ([]string, []any) {
	fields := []string{"ID", "Name", "RAM", "Disk", "Ephemeral", "VCPUs", "Swap", "RXTX Factor", "Is Public", "Description", "Properties"}
	values := []any{fl.ID, fl.Name, fl.RAM, fl.Disk, fl.Ephemeral, fl.VCPUs, fl.Swap, fl.RxTxFactor, fl.IsPublic, fl.Description, fl.ExtraSpecs}
	return fields, values
}

// ---------------------------------------------------------------------------
// flavor create
// ---------------------------------------------------------------------------

type flavorCreateFlags struct {
	ram        int
	disk       int
	vcpus      int
	id         string
	ephemeral  int
	swap       int
	rxtxFactor float64
	public     bool
	private    bool
}

func newFlavorCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &flavorCreateFlags{public: true}
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new flavor",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newComputeClient(ctx, a)
			if err != nil {
				return err
			}
			return runFlavorCreate(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.IntVar(&f.ram, "ram", 256, "memory size in MB")
	fl.IntVar(&f.disk, "disk", 0, "root disk size in GB")
	fl.IntVar(&f.vcpus, "vcpus", 1, "number of vcpus")
	fl.StringVar(&f.id, "id", "", "unique flavor ID; 'auto' or empty lets nova assign a UUID")
	fl.IntVar(&f.ephemeral, "ephemeral", 0, "ephemeral disk size in GB")
	fl.IntVar(&f.swap, "swap", 0, "swap space size in MB")
	fl.Float64Var(&f.rxtxFactor, "rxtx-factor", 0, "RX/TX factor (default server-side 1.0)")
	fl.BoolVar(&f.public, "public", true, "flavor is available to all projects (default)")
	fl.BoolVar(&f.private, "private", false, "flavor is available only to the current project")
	return cmd
}

func runFlavorCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, name string, f *flavorCreateFlags, w io.Writer) error {
	disk := f.disk
	opts := flavors.CreateOpts{
		Name:       name,
		RAM:        f.ram,
		VCPUs:      f.vcpus,
		Disk:       &disk,
		ID:         f.id,
		RxTxFactor: f.rxtxFactor,
	}
	if f.ephemeral != 0 {
		eph := f.ephemeral
		opts.Ephemeral = &eph
	}
	if f.swap != 0 {
		swap := f.swap
		opts.Swap = &swap
	}
	// --private wins when explicitly requested; --public=false also yields a
	// private flavor. Public only when requested and not overridden by --private.
	isPublic := f.public && !f.private
	opts.IsPublic = &isPublic

	fl, err := flavors.Create(ctx, client, opts).Extract()
	if err != nil {
		return fmt.Errorf("creating flavor %q: %w", name, err)
	}
	fields, values := flavorSingle(fl)
	return o.WriteSingle(w, fields, values)
}

// ---------------------------------------------------------------------------
// flavor delete
// ---------------------------------------------------------------------------

func newFlavorDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <flavor> [<flavor> ...]",
		Short: "Delete flavor(s)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newComputeClient(ctx, a)
			if err != nil {
				return err
			}
			return runFlavorDelete(ctx, client, args, cmd.OutOrStdout())
		},
	}
	return cmd
}

func runFlavorDelete(ctx context.Context, client *gophercloud.ServiceClient, refs []string, _ io.Writer) error {
	for _, ref := range refs {
		id, err := resolveFlavorID(ctx, client, ref)
		if err != nil {
			return err
		}
		if err := flavors.Delete(ctx, client, id).ExtractErr(); err != nil {
			return fmt.Errorf("deleting flavor %q: %w", ref, err)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// flavor set / unset (extra specs, a.k.a. properties)
// ---------------------------------------------------------------------------

func newFlavorSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var props []string
	cmd := &cobra.Command{
		Use:   "set <flavor>",
		Short: "Set flavor properties (extra specs)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newComputeClient(ctx, a)
			if err != nil {
				return err
			}
			return runFlavorSet(ctx, client, args[0], props, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringArrayVar(&props, "property", nil, "property to add or change, as key=value (repeatable)")
	return cmd
}

func runFlavorSet(ctx context.Context, client *gophercloud.ServiceClient, ref string, props []string, _ io.Writer) error {
	specs, err := parseProperties(props)
	if err != nil {
		return err
	}
	if len(specs) == 0 {
		return nil
	}
	id, err := resolveFlavorID(ctx, client, ref)
	if err != nil {
		return err
	}
	if _, err := flavors.CreateExtraSpecs(ctx, client, id, flavors.ExtraSpecsOpts(specs)).Extract(); err != nil {
		return fmt.Errorf("setting properties on flavor %q: %w", ref, err)
	}
	return nil
}

func newFlavorUnsetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var props []string
	cmd := &cobra.Command{
		Use:   "unset <flavor>",
		Short: "Unset flavor properties (extra specs)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newComputeClient(ctx, a)
			if err != nil {
				return err
			}
			return runFlavorUnset(ctx, client, args[0], props, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringArrayVar(&props, "property", nil, "property to remove, as key (repeatable)")
	return cmd
}

func runFlavorUnset(ctx context.Context, client *gophercloud.ServiceClient, ref string, keys []string, _ io.Writer) error {
	if len(keys) == 0 {
		return nil
	}
	id, err := resolveFlavorID(ctx, client, ref)
	if err != nil {
		return err
	}
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if err := flavors.DeleteExtraSpec(ctx, client, id, key).ExtractErr(); err != nil {
			return fmt.Errorf("unsetting property %q on flavor %q: %w", key, ref, err)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// parseProperties splits repeated key=value flags into a map.
func parseProperties(props []string) (map[string]string, error) {
	if len(props) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(props))
	for _, p := range props {
		k, v, ok := strings.Cut(p, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			return nil, fmt.Errorf("invalid property %q: expected key=value", p)
		}
		out[k] = v
	}
	return out, nil
}

// resolveFlavorID turns a flavor reference (ID or name) into a flavor ID. The
// nova flavor API keys on ID, so a name is resolved by listing flavors and
// matching. An exact ID match is preferred so lookups stay cheap and
// unambiguous when the caller already passes an ID.
func resolveFlavorID(ctx context.Context, client *gophercloud.ServiceClient, ref string) (string, error) {
	pages, err := flavors.ListDetail(client, flavors.ListOpts{}).AllPages(ctx)
	if err != nil {
		return "", fmt.Errorf("resolving flavor %q: %w", ref, err)
	}
	all, err := flavors.ExtractFlavors(pages)
	if err != nil {
		return "", fmt.Errorf("resolving flavor %q: %w", ref, err)
	}
	var byName []string
	for _, fl := range all {
		if fl.ID == ref {
			return fl.ID, nil
		}
		if fl.Name == ref {
			byName = append(byName, fl.ID)
		}
	}
	switch len(byName) {
	case 0:
		return "", fmt.Errorf("no flavor found with name or ID %q", ref)
	case 1:
		return byName[0], nil
	default:
		return "", fmt.Errorf("multiple flavors match name %q; specify the ID instead", ref)
	}
}

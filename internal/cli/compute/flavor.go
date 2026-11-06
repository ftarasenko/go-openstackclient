package compute

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/flavors"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/koc/internal/auth"
	"github.com/ftarasenko/koc/internal/cli/batchdelete"
	"github.com/ftarasenko/koc/internal/cli/resolve"
	"github.com/ftarasenko/koc/internal/output"
	computeapi "github.com/ftarasenko/koc/pkg/openstack/compute"
	osresolve "github.com/ftarasenko/koc/pkg/openstack/resolve"
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
	long    bool
	public  bool // only public flavors (default view)
	private bool // only private flavors, across all projects (admin)
	all     bool // all flavors, public and private (admin)
	minDisk int
	minRAM  int
	marker  string
	limit   int
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
	fl.BoolVar(&f.private, "private", false, "list only private flavors (admin only)")
	fl.BoolVar(&f.all, "all", false, "list all flavors, whether public or private (admin only)")
	fl.IntVar(&f.minDisk, "min-disk", 0, "filter flavors by a minimum root disk size, in GB")
	fl.IntVar(&f.minRAM, "min-ram", 0, "filter flavors by a minimum memory size, in MB")
	fl.StringVar(&f.marker, "marker", "", "list flavors after this flavor ID (pagination marker)")
	fl.IntVar(&f.limit, "limit", 0, "maximum number of flavors to return")
	// The three access views select mutually exclusive values of one query
	// parameter, so let cobra reject the combination rather than silently
	// picking one, as upstream's mutually-exclusive argparse group does.
	cmd.MarkFlagsMutuallyExclusive("public", "private", "all")
	return cmd
}

func runFlavorList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *flavorListFlags, w io.Writer) error {
	opts := flavors.ListOpts{
		MinDisk: f.minDisk,
		MinRAM:  f.minRAM,
		Marker:  f.marker,
		Limit:   f.limit,
	}
	switch {
	case f.all:
		opts.AccessType = flavors.AllAccess
	case f.private:
		opts.AccessType = flavors.PrivateAccess
	case f.public:
		opts.AccessType = flavors.PublicAccess
	}

	// Nova treats limit only as a page size; ListFlavors enforces --limit as a
	// hard result cap.
	all, err := computeapi.ListFlavors(ctx, client, opts)
	if err != nil {
		return err
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
			row = append(row, fl.Swap, rxtxFactor(fl.RxTxFactor), fl.ExtraSpecs)
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
	fl, err := computeapi.GetFlavor(ctx, client, id)
	if err != nil {
		return relabel(err, id, ref)
	}
	fields, values := flavorSingle(fl)
	// A private flavor's whole point is which projects may boot it, and that
	// list lives on a separate endpoint. Upstream renders the column
	// unconditionally (empty for a public flavor, which has no access list at
	// all — nova 404s on the endpoint), so the field set stays stable for -c.
	var projects []string
	if !fl.IsPublic {
		if projects, err = flavorAccessProjectIDs(ctx, client, fl.ID, ref); err != nil {
			return err
		}
	}
	fields = append(fields, "Access Project IDs")
	values = append(values, projects)
	return o.WriteSingle(w, fields, values)
}

// flavorAccessProjectIDs lists the projects granted access to a private flavor.
func flavorAccessProjectIDs(ctx context.Context, client *gophercloud.ServiceClient, id, ref string) ([]string, error) {
	access, err := computeapi.ListFlavorAccess(ctx, client, id)
	if err != nil {
		return nil, relabel(err, id, ref)
	}
	projects := make([]string, 0, len(access))
	for _, a := range access {
		projects = append(projects, a.TenantID)
	}
	return projects, nil
}

// flavorColumns are the fields of a single flavor.
var flavorColumns = []string{"ID", "Name", "RAM", "Disk", "Ephemeral", "VCPUs", "Swap", "RXTX Factor", "Is Public", "Description", "Properties"}

func flavorSingle(fl *flavors.Flavor) ([]string, []any) {
	fields := flavorColumns
	values := []any{fl.ID, fl.Name, fl.RAM, fl.Disk, fl.Ephemeral, fl.VCPUs, fl.Swap, rxtxFactor(fl.RxTxFactor), fl.IsPublic, fl.Description, fl.ExtraSpecs}
	return fields, values
}

// rxtxFactor is null when nova sent none: from 2.102 flavors carry no
// rxtx_factor, gophercloud then decodes 0, and nova never allows a factor of 0.
func rxtxFactor(f float64) any {
	if f == 0 {
		return nil
	}
	return f
}

// ---------------------------------------------------------------------------
// flavor create
// ---------------------------------------------------------------------------

// flavorRxTxRemovedMicroversion is the compute microversion that removed the
// rxtx_factor field from the flavor API. Above it nova rejects the key, so the
// flag is refused here with the reason rather than letting nova 400.
const flavorRxTxRemovedMicroversion = "2.102"

type flavorCreateFlags struct {
	ram            int
	disk           int
	vcpus          int
	id             string
	ephemeral      int
	swap           int
	rxtxFactor     float64
	rxtxFactorSet  bool
	public         bool
	private        bool
	properties     []string
	project        string
	projectDomain  string
	description    string
	descriptionSet bool
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
			// Both fields are only meaningful when the user asked for them: an
			// empty --description is still a description, and --rxtx-factor 0
			// must not be mistaken for "unset" when refusing it above 2.101.
			f.descriptionSet = cmd.Flags().Changed("description")
			f.rxtxFactorSet = cmd.Flags().Changed("rxtx-factor")
			ctx := cmd.Context()
			client, session, err := newComputeSession(ctx, a)
			if err != nil {
				return err
			}
			// Nova's access list only exists for a private flavor, so reject the
			// combination before creating anything — otherwise the flavor lands
			// and only the access grant fails, leaving half the command applied.
			if f.project != "" && f.public && !f.private {
				return fmt.Errorf("--project requires --private: a public flavor is already reachable by every project")
			}
			// --project names a keystone project, so it is resolved here rather
			// than inside the seam, which stays a pure nova call — same split as
			// "flavor set".
			projectID := ""
			if f.project != "" {
				identity, ierr := session.Identity()
				if ierr != nil {
					return ierr
				}
				projectID, ierr = resolve.ProjectIDInDomain(ctx, identity, f.project, f.projectDomain)
				if ierr != nil {
					return ierr
				}
			}
			return runFlavorCreate(ctx, client, o, args[0], f, projectID, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.IntVar(&f.ram, "ram", 256, "memory size in MB")
	fl.IntVar(&f.disk, "disk", 0, "root disk size in GB")
	fl.IntVar(&f.vcpus, "vcpus", 1, "number of vcpus")
	fl.StringVar(&f.id, "id", "", "unique flavor ID; 'auto' or empty lets nova assign a UUID")
	fl.IntVar(&f.ephemeral, "ephemeral", 0, "ephemeral disk size in GB")
	fl.IntVar(&f.swap, "swap", 0, "swap space size in MB")
	fl.Float64Var(&f.rxtxFactor, "rxtx-factor", 0, "RX/TX factor (default server-side 1.0; removed from the API at nova "+flavorRxTxRemovedMicroversion+")")
	fl.BoolVar(&f.public, "public", true, "flavor is available to all projects (default)")
	fl.BoolVar(&f.private, "private", false, "flavor is available only to the projects granted access")
	fl.StringArrayVar(&f.properties, "property", nil, "property to set on the new flavor, as key=value (repeatable)")
	fl.StringVar(&f.project, "project", "", "grant this project access to the new flavor (name or ID; requires --private)")
	fl.StringVar(&f.projectDomain, flagProjectDomain, "", helpProjectDomain)
	fl.StringVar(&f.description, "description", "", "flavor description (nova "+flavorDescriptionMicroversion+"+)")
	cmd.MarkFlagsMutuallyExclusive("public", "private")
	return cmd
}

func runFlavorCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, name string, f *flavorCreateFlags, projectID string, w io.Writer) error {
	if err := o.CheckColumns(flavorColumns...); err != nil {
		return err
	}
	specs, err := parseProperties(f.properties)
	if err != nil {
		return err
	}
	if f.descriptionSet && !computeSupportsMicroversion(client, flavorDescriptionMicroversion) {
		return fmt.Errorf("--description requires compute API microversion %s or later (--os-compute-api-version)",
			flavorDescriptionMicroversion)
	}
	if f.rxtxFactorSet && computePinnedAtOrAbove(client, flavorRxTxRemovedMicroversion) {
		return fmt.Errorf("--rxtx-factor is only supported up to compute API microversion 2.101; lower --os-compute-api-version to use it")
	}

	disk := f.disk
	opts := flavors.CreateOpts{
		Name:        name,
		RAM:         f.ram,
		VCPUs:       f.vcpus,
		Disk:        &disk,
		ID:          flavorCreateID(f.id),
		RxTxFactor:  f.rxtxFactor,
		Description: f.description,
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

	fl, err := computeapi.CreateFlavor(ctx, client, opts)
	if err != nil {
		return err
	}
	// Nova has no way to create a flavor with its access list or extra specs in
	// the same call, so both follow the POST — as they do upstream. A failure
	// here is reported rather than logged: the flavor exists but is not the one
	// that was asked for, and an exit code is the only way a script sees that.
	if projectID != "" {
		if _, aerr := computeapi.AddFlavorAccess(ctx, client, fl.ID, projectID); aerr != nil {
			return relabel(aerr, fl.ID, name)
		}
	}
	if len(specs) > 0 {
		created, serr := computeapi.SetFlavorExtraSpecs(ctx, client, fl.ID, specs)
		if serr != nil {
			return relabel(serr, fl.ID, name)
		}
		// The create response predates the extra specs, so fold them in rather
		// than rendering a flavor whose Properties column is empty.
		fl.ExtraSpecs = created
	}
	fields, values := flavorSingle(fl)
	return o.WriteSingle(w, fields, values)
}

// flavorCreateID maps the --id value onto nova's request field. novaclient
// aliased "auto" to "generate a UUID for me" and upstream OSC still honours it
// (with a deprecation warning), so an "auto" that reached nova verbatim would
// create a flavor literally named by that ID.
func flavorCreateID(id string) string {
	if id == "auto" {
		return ""
	}
	return id
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
	return batchdelete.Each(refs, func(ref string) error {
		id, err := resolveFlavorID(ctx, client, ref)
		if err != nil {
			return err
		}
		return relabel(computeapi.DeleteFlavor(ctx, client, id), id, ref)
	})
}

// ---------------------------------------------------------------------------
// flavor set / unset (extra specs, a.k.a. properties)
// ---------------------------------------------------------------------------

// flavorDescriptionMicroversion is the compute microversion that added the
// flavor description. Zed's nova caps at 2.93 so every supported cloud has it;
// a client pinned below it gets a flag error naming the requirement instead of
// nova's 400.
const flavorDescriptionMicroversion = computeapi.FlavorDescriptionMicroversion

// flavorSetFlags holds the flags of "flavor set". Upstream OSC puts three
// unrelated nova mutations behind this one verb: the extra specs
// (--property/--no-property), the flavor's project access list (--project, a
// POST on the flavor's action endpoint, private flavors only) and its
// description (--description, a PUT on the flavor itself).
type flavorSetFlags struct {
	properties     []string
	noProperty     bool
	project        string
	projectDomain  string
	description    string
	descriptionSet bool
}

func newFlavorSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &flavorSetFlags{}
	cmd := &cobra.Command{
		Use:   "set <flavor>",
		Short: "Set flavor properties, project access and description",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			// An explicit empty --description clears the field, so the flag's
			// presence — not its value — decides whether to send the update.
			f.descriptionSet = cmd.Flags().Changed("description")
			ctx := cmd.Context()
			client, session, err := newComputeSession(ctx, a)
			if err != nil {
				return err
			}
			// --project names a keystone project, so it is resolved here rather
			// than inside the seam, which stays a pure nova call — same split as
			// "keypair list" and its owner filters.
			projectID := ""
			if f.project != "" {
				identity, ierr := session.Identity()
				if ierr != nil {
					return ierr
				}
				projectID, ierr = resolve.ProjectIDInDomain(ctx, identity, f.project, f.projectDomain)
				if ierr != nil {
					return ierr
				}
			}
			return runFlavorSet(ctx, client, args[0], f, projectID, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringArrayVar(&f.properties, "property", nil, "property to add or change, as key=value (repeatable)")
	fl.BoolVar(&f.noProperty, "no-property", false, "remove all properties from the flavor; with --property, replace them")
	fl.StringVar(&f.project, "project", "", "grant this project access to the flavor (name or ID; private flavors only, admin)")
	fl.StringVar(&f.projectDomain, flagProjectDomain, "", helpProjectDomain)
	fl.StringVar(&f.description, "description", "", "new flavor description; empty clears it (nova "+flavorDescriptionMicroversion+"+)")
	return cmd
}

func runFlavorSet(ctx context.Context, client *gophercloud.ServiceClient, ref string, f *flavorSetFlags, projectID string, _ io.Writer) error {
	specs, err := parseProperties(f.properties)
	if err != nil {
		return err
	}
	if f.descriptionSet && !computeSupportsMicroversion(client, flavorDescriptionMicroversion) {
		return fmt.Errorf("--description requires compute API microversion %s or later (--os-compute-api-version)",
			flavorDescriptionMicroversion)
	}
	if !f.noProperty && len(specs) == 0 && projectID == "" && !f.descriptionSet {
		return nil
	}
	fl, err := resolveFlavor(ctx, client, ref)
	if err != nil {
		return err
	}
	// Nova refuses an access-list entry on a public flavor (it is already
	// reachable by every project). Rejecting it here, before any of the other
	// mutations is issued, keeps a mixed "flavor set" from applying half its
	// flags — the reason validateServerSetFlags exists for "server set".
	if projectID != "" && fl.IsPublic {
		return fmt.Errorf("cannot grant project access to flavor %q: it is public, and access lists apply to private flavors only", ref)
	}

	// Order matters: --no-property clears the current extra specs before
	// --property writes the new ones, so passing both replaces the set outright.
	// That combination is exactly what upstream documents the pair for.
	if f.noProperty {
		if cerr := computeapi.ClearFlavorExtraSpecs(ctx, client, fl.ID); cerr != nil {
			return relabel(cerr, fl.ID, ref)
		}
	}
	if len(specs) > 0 {
		if _, serr := computeapi.SetFlavorExtraSpecs(ctx, client, fl.ID, specs); serr != nil {
			return relabel(serr, fl.ID, ref)
		}
	}
	if projectID != "" {
		if _, aerr := computeapi.AddFlavorAccess(ctx, client, fl.ID, projectID); aerr != nil {
			return relabel(aerr, fl.ID, ref)
		}
	}
	if f.descriptionSet {
		if derr := computeapi.UpdateFlavorDescription(ctx, client, fl.ID, f.description); derr != nil {
			return relabel(derr, fl.ID, ref)
		}
	}
	return nil
}

// flavorUnsetFlags holds the flags of "flavor unset", the inverse of
// "flavor set": --property removes extra specs by key, --project revokes a
// project's access to a private flavor.
type flavorUnsetFlags struct {
	properties    []string
	project       string
	projectDomain string
}

func newFlavorUnsetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &flavorUnsetFlags{}
	cmd := &cobra.Command{
		Use:   "unset <flavor>",
		Short: "Unset flavor properties and project access",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, session, err := newComputeSession(ctx, a)
			if err != nil {
				return err
			}
			// Resolved here rather than inside the seam, for the reason given in
			// newFlavorSetCommand.
			projectID := ""
			if f.project != "" {
				identity, ierr := session.Identity()
				if ierr != nil {
					return ierr
				}
				projectID, ierr = resolve.ProjectIDInDomain(ctx, identity, f.project, f.projectDomain)
				if ierr != nil {
					return ierr
				}
			}
			return runFlavorUnset(ctx, client, args[0], f, projectID, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringArrayVar(&f.properties, "property", nil, "property to remove, as key (repeatable)")
	fl.StringVar(&f.project, "project", "", "revoke this project's access to the flavor (name or ID; private flavors only, admin)")
	fl.StringVar(&f.projectDomain, flagProjectDomain, "", helpProjectDomain)
	return cmd
}

func runFlavorUnset(ctx context.Context, client *gophercloud.ServiceClient, ref string, f *flavorUnsetFlags, projectID string, _ io.Writer) error {
	keys := make([]string, 0, len(f.properties))
	for _, key := range f.properties {
		if key = strings.TrimSpace(key); key != "" {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 && projectID == "" {
		return nil
	}
	fl, err := resolveFlavor(ctx, client, ref)
	if err != nil {
		return err
	}
	// A public flavor has no access list to remove an entry from; reject it
	// before the first DELETE, as "flavor set --project" does.
	if projectID != "" && fl.IsPublic {
		return fmt.Errorf("cannot revoke project access to flavor %q: it is public, and access lists apply to private flavors only", ref)
	}

	for _, key := range keys {
		if err := computeapi.DeleteFlavorExtraSpec(ctx, client, fl.ID, key); err != nil {
			return relabel(err, fl.ID, ref)
		}
	}
	if projectID != "" {
		if _, rerr := computeapi.RemoveFlavorAccess(ctx, client, fl.ID, projectID); rerr != nil {
			return relabel(rerr, fl.ID, ref)
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

// resolveFlavor turns a flavor reference (ID or name) into the flavor itself,
// through computeapi.FindFlavor (which also finds an admin's private flavors),
// keeping this command's wording for no match and several.
func resolveFlavor(ctx context.Context, client *gophercloud.ServiceClient, ref string) (*flavors.Flavor, error) {
	fl, err := computeapi.FindFlavor(ctx, client, ref)
	var notFound *osresolve.NotFoundError
	var ambiguous *osresolve.AmbiguousError
	switch {
	case errors.As(err, &notFound):
		return nil, fmt.Errorf("no flavor found with name or ID %q", ref)
	case errors.As(err, &ambiguous):
		return nil, fmt.Errorf("multiple flavors match name %q; specify the ID instead", ref)
	}
	return fl, err
}

// relabel rewords an error from pkg/openstack/compute the way this command
// has always printed it. The public functions name the flavor by the ID they
// were given; these verbs name it by the reference the operator typed (or the
// name being created), so the quoted ID — the last one in the message — is
// swapped for it around the same underlying error.
func relabel(err error, id, ref string) error {
	inner := errors.Unwrap(err)
	if inner == nil || id == ref {
		return err
	}
	outer := strings.TrimSuffix(err.Error(), ": "+inner.Error())
	quoted := strconv.Quote(id)
	i := strings.LastIndex(outer, quoted)
	if i < 0 {
		return err
	}
	return fmt.Errorf("%s%s%s: %w", outer[:i], strconv.Quote(ref), outer[i+len(quoted):], inner)
}

// resolveFlavorID is resolveFlavor for the callers that only need the ID.
func resolveFlavorID(ctx context.Context, client *gophercloud.ServiceClient, ref string) (string, error) {
	fl, err := resolveFlavor(ctx, client, ref)
	if err != nil {
		return "", err
	}
	return fl.ID, nil
}

// computeSupportsMicroversion reports whether the compute client's negotiated
// microversion is at least want. "latest" (koc's default) supports everything;
// an unset microversion is nova's 2.1 baseline and supports nothing newer.
//
// It backs the *lower* bounds — a feature nova added — where "latest" resolving
// to something older on an old cloud is harmless: nova answers with its own
// error and no flag is refused that the cloud would have taken.
func computeSupportsMicroversion(client *gophercloud.ServiceClient, want string) bool {
	return client.Microversion == "latest" || computePinnedAtOrAbove(client, want)
}

// computePinnedAtOrAbove reports whether the client is *pinned* to a
// microversion at or above want, answering false for "latest".
//
// It backs the *upper* bounds — a field nova removed — and the asymmetry with
// computeSupportsMicroversion is deliberate. "latest" is resolved by nova, not
// here, and koc supports clouds back to Zed, where it means 2.93; reading it as
// "the newest microversion that exists" would refuse --rxtx-factor on every
// cloud in the supported range that still accepts it. Only an explicit pin is
// evidence that the field is gone.
func computePinnedAtOrAbove(client *gophercloud.ServiceClient, want string) bool {
	hMaj, hMin, ok := parseMicroversion(client.Microversion)
	if !ok {
		return false
	}
	wMaj, wMin, _ := parseMicroversion(want)
	if hMaj != wMaj {
		return hMaj > wMaj
	}
	return hMin >= wMin
}

func parseMicroversion(v string) (major, minor int, ok bool) {
	majStr, minStr, found := strings.Cut(v, ".")
	if !found {
		return 0, 0, false
	}
	major, err1 := strconv.Atoi(majStr)
	minor, err2 := strconv.Atoi(minStr)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return major, minor, true
}

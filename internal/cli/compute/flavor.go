package compute

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/flavors"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
	"github.com/ftarasenko/go-openstackclient/internal/cli/resolve"
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
	return batchdelete.Each(refs, func(ref string) error {
		id, err := resolveFlavorID(ctx, client, ref)
		if err != nil {
			return err
		}
		if err := flavors.Delete(ctx, client, id).ExtractErr(); err != nil {
			return fmt.Errorf("deleting flavor %q: %w", ref, err)
		}
		return nil
	})
}

// ---------------------------------------------------------------------------
// flavor set / unset (extra specs, a.k.a. properties)
// ---------------------------------------------------------------------------

// flavorDescriptionMicroversion is the compute microversion that added the
// flavor description — and with it PUT /flavors/{id}, the only way to change
// one. Zed's nova caps at 2.93 so every supported cloud has it; a client pinned
// below it gets a flag error naming the requirement instead of nova's 400.
const flavorDescriptionMicroversion = "2.55"

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
	fl.StringVar(&f.projectDomain, "project-domain", "", "domain owning --project, to disambiguate the name (name or ID)")
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
		if cerr := clearFlavorExtraSpecs(ctx, client, fl.ID, ref); cerr != nil {
			return cerr
		}
	}
	if len(specs) > 0 {
		if _, serr := flavors.CreateExtraSpecs(ctx, client, fl.ID, flavors.ExtraSpecsOpts(specs)).Extract(); serr != nil {
			return fmt.Errorf("setting properties on flavor %q: %w", ref, serr)
		}
	}
	if projectID != "" {
		if _, aerr := flavors.AddAccess(ctx, client, fl.ID, flavors.AddAccessOpts{Tenant: projectID}).Extract(); aerr != nil {
			return fmt.Errorf("granting project %q access to flavor %q: %w", projectID, ref, aerr)
		}
	}
	if f.descriptionSet {
		if derr := updateFlavorDescription(ctx, client, fl.ID, ref, f.description); derr != nil {
			return derr
		}
	}
	return nil
}

// clearFlavorExtraSpecs backs --no-property. Nova has no bulk delete for extra
// specs, so the current set is listed and removed one key at a time; the keys are
// sorted so the request sequence is deterministic.
func clearFlavorExtraSpecs(ctx context.Context, client *gophercloud.ServiceClient, id, ref string) error {
	specs, err := flavors.ListExtraSpecs(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("listing properties of flavor %q: %w", ref, err)
	}
	keys := make([]string, 0, len(specs))
	for k := range specs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if err := flavors.DeleteExtraSpec(ctx, client, id, k).ExtractErr(); err != nil {
			return fmt.Errorf("unsetting property %q on flavor %q: %w", k, ref, err)
		}
	}
	return nil
}

// updateFlavorDescription sets — or, for an empty value, clears — a flavor's
// description via PUT /flavors/{id} (nova 2.55+).
//
// This is a raw call because gophercloud's flavors.UpdateOpts tags Description
// `omitempty` and so cannot express the clear: an empty string would send
// {"flavor": {}} and nova 400s on the empty body. The raw PUT sends an explicit
// null, which is how nova unsets the field. Replace it with flavors.Update once
// the typed opts model a nullable description.
func updateFlavorDescription(ctx context.Context, client *gophercloud.ServiceClient, id, ref, description string) error {
	var value any
	if description != "" {
		value = description
	}
	body := map[string]any{"flavor": map[string]any{"description": value}}
	resp, err := client.Put(ctx, client.ServiceURL("flavors", id), body, nil, &gophercloud.RequestOpts{
		OkCodes: []int{200},
	})
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if err != nil {
		return fmt.Errorf("setting description on flavor %q: %w", ref, err)
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
	fl.StringVar(&f.projectDomain, "project-domain", "", "domain owning --project, to disambiguate the name (name or ID)")
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
		if err := flavors.DeleteExtraSpec(ctx, client, fl.ID, key).ExtractErr(); err != nil {
			return fmt.Errorf("unsetting property %q on flavor %q: %w", key, ref, err)
		}
	}
	if projectID != "" {
		if _, rerr := flavors.RemoveAccess(ctx, client, fl.ID, flavors.RemoveAccessOpts{Tenant: projectID}).Extract(); rerr != nil {
			return fmt.Errorf("revoking project %q access to flavor %q: %w", projectID, ref, rerr)
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

// resolveFlavor turns a flavor reference (ID or name) into the flavor itself.
// The nova flavor API keys on ID, so a name is resolved by listing flavors and
// matching. An exact ID match is preferred so lookups stay cheap and unambiguous
// when the caller already passes an ID.
//
// Nova's flavor listing defaults to is_public=true — even for an admin token —
// so a private flavor is invisible in the default view. That view is tried first
// (the common case, one call) and only a miss retries with is_public=None, which
// nova ignores for a non-admin token rather than rejecting. The retry is what
// lets an admin name a private flavor, the only kind "flavor set --project"
// applies to.
func resolveFlavor(ctx context.Context, client *gophercloud.ServiceClient, ref string) (*flavors.Flavor, error) {
	for _, access := range []flavors.AccessType{"", flavors.AllAccess} {
		fl, err := findFlavor(ctx, client, ref, access)
		if err != nil || fl != nil {
			return fl, err
		}
	}
	return nil, fmt.Errorf("no flavor found with name or ID %q", ref)
}

// findFlavor returns the flavor matching ref within one access view, or nil when
// that view holds no match.
func findFlavor(ctx context.Context, client *gophercloud.ServiceClient, ref string, access flavors.AccessType) (*flavors.Flavor, error) {
	pages, err := flavors.ListDetail(client, flavors.ListOpts{AccessType: access}).AllPages(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolving flavor %q: %w", ref, err)
	}
	all, err := flavors.ExtractFlavors(pages)
	if err != nil {
		return nil, fmt.Errorf("resolving flavor %q: %w", ref, err)
	}
	var byName []int
	for i := range all {
		if all[i].ID == ref {
			return &all[i], nil
		}
		if all[i].Name == ref {
			byName = append(byName, i)
		}
	}
	switch len(byName) {
	case 0:
		return nil, nil
	case 1:
		return &all[byName[0]], nil
	default:
		return nil, fmt.Errorf("multiple flavors match name %q; specify the ID instead", ref)
	}
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
func computeSupportsMicroversion(client *gophercloud.ServiceClient, want string) bool {
	if client.Microversion == "latest" {
		return true
	}
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

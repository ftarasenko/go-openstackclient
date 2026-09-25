package network

import (
	"context"
	"fmt"
	"io"
	"slices"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/subnetpools"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// newSubnetPoolCommand builds "subnet pool ...". It is a child of the existing
// "subnet" noun, matching upstream's two-word `openstack subnet pool ...`.
//
// Flag names follow upstream OSC. UNVERIFIED against KeyStack docs
// (https://docs.keystack.ru/ returned HTTP 403 at implementation time); falls
// back to upstream OSC semantics.
// flagSubnetPoolNoAddressScope is "subnet pool set --no-address-scope".
const flagSubnetPoolNoAddressScope = "no-address-scope"

func newSubnetPoolCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pool",
		Short: "Manage subnet pools",
	}
	cmd.AddCommand(
		newSubnetPoolListCommand(a, o),
		newSubnetPoolShowCommand(a, o),
		newSubnetPoolCreateCommand(a, o),
		newSubnetPoolSetCommand(a, o),
		newSubnetPoolDeleteCommand(a, o),
		newSubnetPoolUnsetCommand(a, o),
	)
	return cmd
}

// resolveSubnetPoolID resolves a subnet pool name or ID to an ID, following the
// shared neutron name-or-ID policy (see helpers.go pickID).
func resolveSubnetPoolID(ctx context.Context, client *gophercloud.ServiceClient, nameOrID string) (string, error) {
	return resolveByName(client, "subnet pool", nameOrID, func(c *gophercloud.ServiceClient) ([]subnetpools.SubnetPool, error) {
		pages, err := subnetpools.List(c, subnetpools.ListOpts{Name: nameOrID}).AllPages(ctx)
		if err != nil {
			return nil, err
		}
		return subnetpools.ExtractSubnetPools(pages)
	}, func(p subnetpools.SubnetPool) string { return p.ID })
}

func subnetPoolFields(p *subnetpools.SubnetPool) ([]string, []any) {
	fields := []string{
		"id", "name", "project_id", "prefixes", "default_prefixlen", "min_prefixlen",
		"max_prefixlen", "default_quota", "address_scope_id", "ip_version", "shared",
		"is_default", "description", "tags", "revision_number", "created_at", "updated_at",
	}
	values := []any{
		p.ID, p.Name, p.ProjectID, p.Prefixes, p.DefaultPrefixLen, p.MinPrefixLen,
		p.MaxPrefixLen, p.DefaultQuota, p.AddressScopeID, p.IPversion, p.Shared,
		p.IsDefault, p.Description, p.Tags, p.RevisionNumber, p.CreatedAt, p.UpdatedAt,
	}
	return fields, values
}

// --- list ------------------------------------------------------------------

type subnetPoolListFlags struct {
	name          string
	project       string
	projectDomain string
	addressScope  string
	ipVersion     int
	long          bool
	tagFilterFlags

	// share/noShare and defaultOnly/notDefaultOnly are the raw flag pair; RunE
	// folds each into the tri-state pointer the neutron filter takes, so the run
	// seam never has to re-read the flag set.
	share          bool
	noShare        bool
	defaultOnly    bool
	notDefaultOnly bool
	shared         *bool
	isDefault      *bool
}

func newSubnetPoolListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &subnetPoolListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List subnet pools",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			fl := cmd.Flags()
			if err := mutuallyExclusive(fl, flagShare, flagNoShare); err != nil {
				return err
			}
			if err := mutuallyExclusive(fl, flagDefault, flagNoDefault); err != nil {
				return err
			}
			f.shared = enableDisable(fl, f.share, f.noShare, flagShare, flagNoShare)
			f.isDefault = enableDisable(fl, f.defaultOnly, f.notDefaultOnly, flagDefault, flagNoDefault)
			ctx := cmd.Context()
			client, session, err := newNetworkSession(ctx, a)
			if err != nil {
				return err
			}
			projectID, err := resolveProjectRef(ctx, session, f.project, f.projectDomain)
			if err != nil {
				return err
			}
			return runSubnetPoolList(ctx, client, o, f, projectID, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "filter by subnet pool name")
	fl.StringVar(&f.project, flagProject, "", "filter by owning project (name or ID)")
	fl.StringVar(&f.projectDomain, flagProjectDomain, "", projectDomainHelp)
	fl.StringVar(&f.addressScope, flagAddressScope, "", "filter by address scope (name or ID)")
	fl.IntVar(&f.ipVersion, "ip-version", 0, "filter by IP version (4 or 6)")
	fl.BoolVar(&f.share, flagShare, false, "list only shared subnet pools")
	fl.BoolVar(&f.noShare, flagNoShare, false, "list only non-shared subnet pools")
	fl.BoolVar(&f.defaultOnly, flagDefault, false, "list only the default subnet pools")
	fl.BoolVar(&f.notDefaultOnly, flagNoDefault, false, "list only non-default subnet pools")
	fl.BoolVar(&f.long, "long", false, "list additional fields in output")
	bindTagFilterFlags(fl, &f.tagFilterFlags, "subnet pools")
	return cmd
}

func runSubnetPoolList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	f *subnetPoolListFlags, projectID string, w io.Writer,
) error {
	opts := subnetpools.ListOpts{
		Name:      f.name,
		ProjectID: projectID,
		IPVersion: f.ipVersion,
		Shared:    f.shared,
		IsDefault: f.isDefault,
	}
	f.apply(&opts.Tags, &opts.TagsAny, &opts.NotTags, &opts.NotTagsAny)
	if f.addressScope != "" {
		scopeID, err := resolveAddressScopeID(ctx, client, f.addressScope)
		if err != nil {
			return err
		}
		opts.AddressScopeID = scopeID
	}
	pages, err := subnetpools.List(client, opts).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing subnet pools: %w", err)
	}
	all, err := subnetpools.ExtractSubnetPools(pages)
	if err != nil {
		return fmt.Errorf("parsing subnet pool list: %w", err)
	}

	// Upstream's columns; --long ends with koc's "Project", kept for scripts
	// that already select it.
	cols := []string{"ID", "Name", "Prefixes"}
	if f.long {
		cols = append(cols, "Default Prefix Length", "Address Scope", "Default Subnet Pool", "Shared", "Tags", "Project")
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(all))}
	for _, p := range all {
		row := []any{p.ID, p.Name, p.Prefixes}
		if f.long {
			row = append(row, p.DefaultPrefixLen, p.AddressScopeID, p.IsDefault, p.Shared, p.Tags, p.ProjectID)
		}
		t.Rows = append(t.Rows, row)
	}
	return o.WriteList(w, t)
}

// --- show ------------------------------------------------------------------

func newSubnetPoolShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <subnet-pool>",
		Short: "Show subnet pool details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			return runSubnetPoolShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runSubnetPoolShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, w io.Writer) error {
	id, err := resolveSubnetPoolID(ctx, client, ref)
	if err != nil {
		return err
	}
	p, err := subnetpools.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("showing subnet pool %q: %w", ref, err)
	}
	fields, values := subnetPoolFields(p)
	return o.WriteSingle(w, fields, values)
}

// --- create ----------------------------------------------------------------

type subnetPoolWriteFlags struct {
	prefixes         []string
	defaultPrefixLen int
	minPrefixLen     int
	maxPrefixLen     int
	defaultQuota     int
	addressScope     string
	description      string
	project          string
	projectDomain    string
	name             string
	noAddressScope   bool
	extraProperty    []string
	share            bool
	noShare          bool
	defaultPool      bool
	noDefaultPool    bool

	// Tri-states resolved from the pairs above in RunE.
	shared    *bool
	isDefault *bool

	tagWriteFlags
}

func (f *subnetPoolWriteFlags) register(cmd *cobra.Command, isCreate bool) {
	fl := cmd.Flags()
	fl.StringSliceVar(&f.prefixes, "pool-prefix", nil, "prefix to add to the pool, e.g. 10.0.0.0/8 (repeatable)")
	fl.IntVar(&f.defaultPrefixLen, "default-prefix-length", 0, "default prefix length allocated from this pool")
	fl.IntVar(&f.minPrefixLen, "min-prefix-length", 0, "smallest prefix length allocatable from this pool")
	fl.IntVar(&f.maxPrefixLen, "max-prefix-length", 0, "largest prefix length allocatable from this pool")
	fl.IntVar(&f.defaultQuota, "default-quota", 0, "per-project quota on the prefix space, in addresses")
	fl.StringVar(&f.addressScope, flagAddressScope, "", "address scope to associate with the pool (name or ID)")
	fl.StringVar(&f.description, flagDescription, "", "subnet pool description")
	fl.BoolVar(&f.defaultPool, flagDefault, false, "make this the default subnet pool for its IP version")
	fl.BoolVar(&f.noDefaultPool, flagNoDefault, false, "do not make this the default subnet pool")
	bindExtraPropertyFlag(fl, &f.extraProperty)
	if isCreate {
		fl.StringVar(&f.project, flagProject, "", "owning project (name or ID)")
		fl.StringVar(&f.projectDomain, flagProjectDomain, "", projectDomainHelp)
		bindTagCreateFlags(cmd, &f.tagWriteFlags, "subnet pool")
		// --share is create-only: neutron's subnetpool PUT has no "shared"
		// attribute (gophercloud's UpdateOpts has no field for it either), so
		// offering it on "set" would silently do nothing. Sharing an existing pool
		// is an RBAC-policy operation, not an attribute update.
		fl.BoolVar(&f.share, flagShare, false, "share the subnet pool across projects")
		fl.BoolVar(&f.noShare, flagNoShare, false, "do not share the subnet pool")
		return
	}
	fl.StringVar(&f.name, "name", "", "new subnet pool name")
	fl.BoolVar(&f.noAddressScope, flagSubnetPoolNoAddressScope, false, "detach the pool from its address scope")
	bindTagSetFlags(fl, &f.tagWriteFlags, "subnet pool")
	cmd.MarkFlagsMutuallyExclusive(flagAddressScope, flagSubnetPoolNoAddressScope)
}

// check rejects contradictory flag pairs and folds each pair into its tri-state
// pointer, so the run seams take resolved data rather than re-reading flags.
func (f *subnetPoolWriteFlags) check(fl *pflag.FlagSet) error {
	if err := mutuallyExclusive(fl, flagDefault, flagNoDefault); err != nil {
		return err
	}
	f.isDefault = enableDisable(fl, f.defaultPool, f.noDefaultPool, flagDefault, flagNoDefault)
	// --share exists on create only (see register), so guard it only when defined.
	if fl.Lookup(flagShare) != nil {
		if err := mutuallyExclusive(fl, flagShare, flagNoShare); err != nil {
			return err
		}
		f.shared = enableDisable(fl, f.share, f.noShare, flagShare, flagNoShare)
	}
	return nil
}

func newSubnetPoolCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &subnetPoolWriteFlags{}
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new subnet pool",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			fl := cmd.Flags()
			if err := f.check(fl); err != nil {
				return err
			}
			if len(f.prefixes) == 0 {
				return fmt.Errorf("--pool-prefix is required: a subnet pool with no prefixes cannot allocate")
			}
			ctx := cmd.Context()
			client, session, err := newNetworkSession(ctx, a)
			if err != nil {
				return err
			}
			projectID, err := resolveProjectRef(ctx, session, f.project, f.projectDomain)
			if err != nil {
				return err
			}
			return runSubnetPoolCreate(ctx, client, o, args[0], f, projectID, cmd.OutOrStdout())
		},
	}
	f.register(cmd, true)
	return cmd
}

func runSubnetPoolCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	name string, f *subnetPoolWriteFlags, projectID string, w io.Writer,
) error {
	opts := subnetpools.CreateOpts{
		Name:             name,
		Prefixes:         f.prefixes,
		DefaultPrefixLen: f.defaultPrefixLen,
		MinPrefixLen:     f.minPrefixLen,
		MaxPrefixLen:     f.maxPrefixLen,
		DefaultQuota:     f.defaultQuota,
		Description:      f.description,
		ProjectID:        projectID,
	}
	if f.addressScope != "" {
		scopeID, err := resolveAddressScopeID(ctx, client, f.addressScope)
		if err != nil {
			return err
		}
		opts.AddressScopeID = scopeID
	}
	// CreateOpts.Shared and IsDefault are plain bools tagged omitempty, so a false
	// is dropped from the request body rather than sent. That is harmless here:
	// neutron already defaults both to false, so --no-share / --no-default reach
	// the same end state by saying nothing.
	if f.shared != nil {
		opts.Shared = *f.shared
	}
	if f.isDefault != nil {
		opts.IsDefault = *f.isDefault
	}
	extra, err := parseExtraProperties(f.extraProperty, false)
	if err != nil {
		return err
	}
	p, err := subnetpools.Create(ctx, client, withSubnetPoolCreateAttrs(opts, extra)).Extract()
	if err != nil {
		return fmt.Errorf("creating subnet pool %q: %w", name, err)
	}
	// Tags cannot ride on the create; upstream sets them afterwards too.
	if p.Tags, err = applyTagsForSet(ctx, client, tagResourceSubnetPools, p.ID, p.Tags, &f.tagWriteFlags); err != nil {
		return err
	}
	fields, values := subnetPoolFields(p)
	return o.WriteSingle(w, fields, values)
}

// --- set -------------------------------------------------------------------

func newSubnetPoolSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &subnetPoolWriteFlags{}
	cmd := &cobra.Command{
		Use:   "set <subnet-pool>",
		Short: "Update a subnet pool",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			fl := cmd.Flags()
			if err := f.check(fl); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			return runSubnetPoolSet(ctx, client, o, args[0], f, fl, cmd.OutOrStdout())
		},
	}
	f.register(cmd, false)
	return cmd
}

// runSubnetPoolSet builds a sparse UpdateOpts: only attributes whose flags were
// actually given are sent, so an unrelated `set --description x` cannot reset
// the pool's prefixes or quota. --pool-prefix ADDS to the pool's prefixes, as
// upstream does (neutron requires the existing prefixes to stay a subset).
// When tags are the only change the pool PUT is skipped.
func runSubnetPoolSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	ref string, f *subnetPoolWriteFlags, changed interface{ Changed(string) bool }, w io.Writer,
) error {
	opts, touched := subnetPoolSetScalars(f, changed)
	var attrs map[string]any
	if f.noAddressScope {
		attrs = map[string]any{"address_scope_id": nil}
	}
	extra, err := parseExtraProperties(f.extraProperty, false)
	if err != nil {
		return err
	}
	attrs = mergeAttrs(attrs, extra)
	touched = touched || len(attrs) > 0 || changed.Changed(flagAddressScope) || changed.Changed("pool-prefix")
	if !touched && !f.given() {
		return fmt.Errorf("nothing to set: pass at least one attribute flag")
	}

	// Resolved after the emptiness check so a no-op invocation costs no round trip.
	id, err := resolveSubnetPoolID(ctx, client, ref)
	if err != nil {
		return err
	}
	if changed.Changed(flagAddressScope) {
		scopeID, err := resolveAddressScopeID(ctx, client, f.addressScope)
		if err != nil {
			return err
		}
		opts.AddressScopeID = &scopeID
	}
	if changed.Changed("pool-prefix") {
		current, err := subnetpools.Get(ctx, client, id).Extract()
		if err != nil {
			return fmt.Errorf("reading subnet pool %q before set: %w", ref, err)
		}
		opts.Prefixes = append(slices.Clone(f.prefixes), current.Prefixes...)
	}
	var p *subnetpools.SubnetPool
	if touched {
		if p, err = subnetpools.Update(ctx, client, id, withSubnetPoolUpdateAttrs(opts, attrs)).Extract(); err != nil {
			return fmt.Errorf("updating subnet pool %q: %w", ref, err)
		}
	} else if p, err = subnetpools.Get(ctx, client, id).Extract(); err != nil {
		// Tags are the only change: read the pool for its current tags instead.
		return fmt.Errorf("getting subnet pool %q: %w", ref, err)
	}
	if p.Tags, err = applyTagsForSet(ctx, client, tagResourceSubnetPools, id, p.Tags, &f.tagWriteFlags); err != nil {
		return err
	}
	fields, values := subnetPoolFields(p)
	return o.WriteSingle(w, fields, values)
}

// subnetPoolSetScalars sets the attributes that need no lookup or read.
func subnetPoolSetScalars(f *subnetPoolWriteFlags, changed interface{ Changed(string) bool }) (subnetpools.UpdateOpts, bool) {
	var opts subnetpools.UpdateOpts
	touched := false
	if changed.Changed("name") {
		opts.Name = f.name
		touched = true
	}
	if changed.Changed("default-prefix-length") {
		opts.DefaultPrefixLen = f.defaultPrefixLen
		touched = true
	}
	if changed.Changed("min-prefix-length") {
		opts.MinPrefixLen = f.minPrefixLen
		touched = true
	}
	if changed.Changed("max-prefix-length") {
		opts.MaxPrefixLen = f.maxPrefixLen
		touched = true
	}
	if changed.Changed("default-quota") {
		quota := f.defaultQuota
		opts.DefaultQuota = &quota
		touched = true
	}
	if changed.Changed(flagDescription) {
		desc := f.description
		opts.Description = &desc
		touched = true
	}
	if f.isDefault != nil {
		opts.IsDefault = f.isDefault
		touched = true
	}
	return opts, touched
}

// --- unset -----------------------------------------------------------------

// newSubnetPoolUnsetCommand is upstream's UnsetSubnetPool: tags are the only
// attribute it clears.
func newSubnetPoolUnsetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &tagWriteFlags{}
	cmd := &cobra.Command{
		Use:   "unset <subnet-pool>",
		Short: "Unset subnet pool properties",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			return runSubnetPoolUnset(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	bindTagUnsetFlags(cmd, f, "subnet pool")
	return cmd
}

func runSubnetPoolUnset(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	ref string, f *tagWriteFlags, w io.Writer,
) error {
	if !f.given() {
		return fmt.Errorf("subnet pool unset requires --tag or --all-tag")
	}
	id, err := resolveSubnetPoolID(ctx, client, ref)
	if err != nil {
		return err
	}
	p, err := subnetpools.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("getting subnet pool %q: %w", ref, err)
	}
	// Tags are a sub-resource: the pool itself is never written.
	if p.Tags, err = applyTagsForUnset(ctx, client, tagResourceSubnetPools, id, p.Tags, f); err != nil {
		return err
	}
	fields, values := subnetPoolFields(p)
	return o.WriteSingle(w, fields, values)
}

// --- delete ----------------------------------------------------------------

func newSubnetPoolDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <subnet-pool> [<subnet-pool>...]",
		Short: "Delete one or more subnet pools",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			return runSubnetPoolDelete(ctx, client, args, cmd.OutOrStdout())
		},
	}
}

func runSubnetPoolDelete(ctx context.Context, client *gophercloud.ServiceClient, refs []string, w io.Writer) error {
	return batchdelete.Each(refs, func(ref string) error {
		id, err := resolveSubnetPoolID(ctx, client, ref)
		if err != nil {
			return err
		}
		if err := subnetpools.Delete(ctx, client, id).ExtractErr(); err != nil {
			return fmt.Errorf("deleting subnet pool %q: %w", ref, err)
		}
		if _, err := fmt.Fprintf(w, "Deleted subnet pool %s\n", ref); err != nil {
			return err
		}
		return nil
	})
}

package baremetal

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Runbooks are named, reusable step lists that `node clean --runbook` and
// `node service --runbook` run. gophercloud has no package for them, so these
// verbs use the raw helpers in raw.go (/v1/runbooks, API 1.92). Below 1.112 a
// runbook's name is the trait a node must carry to run it; from 1.112 the
// name is free-form and the traits are a separate list (/traits).
//
// Flag names follow python-ironicclient (`openstack baremetal runbook`).
// UNVERIFIED against KeyStack (docs.keystack.ru unreachable, HTTP 403).

var (
	featureRunbooks           = ironicFeature{"baremetal runbook", "1.92", releaseDalmatian}
	featureRunbookDescription = ironicFeature{"baremetal runbook --description", "1.112", releaseHibiscus}
	featureRunbookTraits      = ironicFeature{"baremetal runbook trait", "1.112", releaseHibiscus}
)

// runbookDetailFields are python-ironicclient's RUNBOOK_DETAILED_RESOURCE;
// runbookFields the short list's.
var (
	runbookDetailFields = []string{
		"uuid", "name", "description", "traits", "disable_ramdisk", "owner", "public",
		"steps", "extra", "created_at", "updated_at",
	}
	runbookFields  = []string{"uuid", "name"}
	runbookColumns = knownColumns(runbookDetailFields)
)

// newRunbookCommand builds "baremetal runbook ...".
func newRunbookCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "runbook",
		Short:   "Manage baremetal runbooks (requires ironic API 1.92, OpenStack 2024.2)",
		Aliases: []string{"runbooks"},
	}
	trait := &cobra.Command{
		Use:   "trait",
		Short: "Manage runbook traits (requires ironic API 1.112, OpenStack 2026.2)",
	}
	trait.AddCommand(
		newRunbookTraitListCommand(a, o),
		newRunbookTraitAddCommand(a, o),
		newRunbookTraitRemoveCommand(a, o),
	)
	cmd.AddCommand(
		newRunbookListCommand(a, o),
		newRunbookShowCommand(a, o),
		newRunbookCreateCommand(a, o),
		newRunbookSetCommand(a, o),
		newRunbookUnsetCommand(a, o),
		newRunbookDeleteCommand(a, o),
		trait,
	)
	return cmd
}

// runbookRunE is the common RunE prologue: validate output, build the client.
func runbookRunE(a *auth.Options, o *output.Options,
	run func(ctx context.Context, client *gophercloud.ServiceClient, cmd *cobra.Command, args []string) error,
) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		if err := o.Validate(); err != nil {
			return err
		}
		ctx := cmd.Context()
		client, err := newBaremetalClient(ctx, a)
		if err != nil {
			return err
		}
		return run(ctx, client, cmd, args)
	}
}

func newRunbookListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &resourceListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List baremetal runbooks",
		Args:  cobra.NoArgs,
		RunE: runbookRunE(a, o, func(ctx context.Context, client *gophercloud.ServiceClient, cmd *cobra.Command, _ []string) error {
			return runRunbookList(ctx, client, o, f, cmd.OutOrStdout())
		}),
	}
	f.register(cmd, "runbook")
	addListFieldsAliases(cmd, o)
	return cmd
}

func runRunbookList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *resourceListFlags, w io.Writer) error {
	fields, path := runbookFields, "runbooks"
	if f.detail(o, runbookFields, runbookDetailFields) {
		fields, path = runbookDetailFields, "runbooks/detail"
	}
	all, err := ironicList(ctx, client, path, "runbooks", f.query(), f.limit)
	if err != nil {
		return fmt.Errorf("listing baremetal runbooks: %w", explainMicroversion(ctx, client, featureRunbooks, err))
	}
	return o.WriteList(w, listTable(all, fields))
}

func newRunbookShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <runbook>",
		Short: "Show details of a baremetal runbook",
		Args:  cobra.ExactArgs(1),
		RunE: runbookRunE(a, o, func(ctx context.Context, client *gophercloud.ServiceClient, cmd *cobra.Command, args []string) error {
			return runRunbookShow(ctx, client, o, args[0], cmd.OutOrStdout())
		}),
	}
	addFieldsAliases(cmd, o)
	return cmd
}

func runRunbookShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, w io.Writer) error {
	var rb map[string]any
	if err := ironicGet(ctx, client, joinPath("runbooks", ref), &rb); err != nil {
		return fmt.Errorf("getting baremetal runbook %s: %w", ref, explainMicroversion(ctx, client, featureRunbooks, err))
	}
	fields, values := showFields(rb, runbookDetailFields)
	return o.WriteSingle(w, fields, values)
}

// runbookFlags are the attributes create and set share. A boolean is sent
// only when its flag was given, so an explicit --public=false is sent too.
type runbookFlags struct {
	uuid           string
	name           string
	description    string
	owner          string
	steps          string
	extra          []string
	public         bool
	publicSet      bool
	disableRamdisk bool
	ramdiskSet     bool
}

func (f *runbookFlags) register(cmd *cobra.Command, verb string) {
	fl := cmd.Flags()
	fl.StringVar(&f.description, "description", "", "description of the runbook (requires ironic API 1.112, OpenStack 2026.2)")
	fl.StringVar(&f.owner, "owner", "", "owner (project ID) of the runbook")
	fl.BoolVar(&f.public, "public", false, "whether the runbook is public (--public=false makes it private)")
	fl.StringVar(&f.steps, "steps", "",
		"the runbook steps: a JSON or YAML string, a path to a JSON or YAML file, or '-' to read standard input")
	fl.StringArrayVar(&f.extra, "extra", nil, verb+" arbitrary metadata key=value (repeatable)")
	fl.BoolVar(&f.disableRamdisk, "disable-ramdisk", false,
		"do not boot ironic-python-agent when this runbook runs; only steps marked as not requiring it can run")
}

func (f *runbookFlags) changed(cmd *cobra.Command) {
	f.publicSet = cmd.Flags().Changed("public")
	f.ramdiskSet = cmd.Flags().Changed("disable-ramdisk")
}

// explain names the microversion a rejected request needed.
func (f *runbookFlags) explain(ctx context.Context, client *gophercloud.ServiceClient, err error) error {
	// Below 1.92 the description's requirement is the higher one, and still
	// true; from 1.112 neither applies and err is returned unchanged.
	if f.description != "" {
		return explainMicroversion(ctx, client, featureRunbookDescription, err)
	}
	return explainMicroversion(ctx, client, featureRunbooks, err)
}

func newRunbookCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &runbookFlags{}
	cmd := &cobra.Command{
		Use:   "create --name <name> --steps <steps>",
		Short: "Create a new baremetal runbook",
		Args:  cobra.NoArgs,
		RunE: runbookRunE(a, o, func(ctx context.Context, client *gophercloud.ServiceClient, cmd *cobra.Command, _ []string) error {
			f.changed(cmd)
			return runRunbookCreate(ctx, client, o, f, cmd.InOrStdin(), cmd.OutOrStdout())
		}),
	}
	cmd.Flags().StringVar(&f.name, "name", "", "unique name of the runbook; below ironic API 1.112 it must be a trait name (required)")
	cmd.Flags().StringVar(&f.uuid, "uuid", "", "UUID of the runbook")
	f.register(cmd, "record")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("steps")
	return cmd
}

func runRunbookCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	f *runbookFlags, stdin io.Reader, w io.Writer,
) error {
	if err := o.CheckColumns(runbookColumns...); err != nil {
		return err
	}
	steps, err := stepsArg(f.steps, stdin)
	if err != nil {
		return err
	}
	body := map[string]any{"name": f.name, "steps": steps}
	setIf(body, "uuid", f.uuid)
	setIf(body, "description", f.description)
	setIf(body, "owner", f.owner)
	if f.publicSet {
		body["public"] = f.public
	}
	if f.ramdiskSet {
		body["disable_ramdisk"] = f.disableRamdisk
	}
	extra, err := parseKeyValMap(f.extra)
	if err != nil {
		return fmt.Errorf("parsing --extra: %w", err)
	}
	if extra != nil {
		body["extra"] = extra
	}
	var rb map[string]any
	if err := ironicPost(ctx, client, "runbooks", body, &rb); err != nil {
		return fmt.Errorf("creating baremetal runbook: %w", f.explain(ctx, client, err))
	}
	fields, values := showFields(rb, runbookDetailFields)
	return o.WriteSingle(w, fields, values)
}

func newRunbookSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &runbookFlags{}
	cmd := &cobra.Command{
		Use:   "set <runbook>",
		Short: "Set baremetal runbook properties",
		Args:  cobra.ExactArgs(1),
		RunE: runbookRunE(a, o, func(ctx context.Context, client *gophercloud.ServiceClient, cmd *cobra.Command, args []string) error {
			f.changed(cmd)
			return runRunbookSet(ctx, client, o, args[0], f, cmd.InOrStdin(), cmd.OutOrStdout())
		}),
	}
	cmd.Flags().StringVar(&f.name, "name", "", "set the unique name of the runbook")
	f.register(cmd, "set")
	return cmd
}

func runRunbookSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	ref string, f *runbookFlags, stdin io.Reader, w io.Writer,
) error {
	if err := o.CheckColumns(runbookColumns...); err != nil {
		return err
	}
	// Upstream's order: name, description, owner, public, steps, extra,
	// disable_ramdisk.
	var ops patchBuilder
	for _, kv := range [][2]string{{"/name", f.name}, {"/description", f.description}, {"/owner", f.owner}} {
		if kv[1] != "" {
			ops.add(kv[0], kv[1])
		}
	}
	if f.publicSet {
		ops.add("/public", f.public)
	}
	if f.steps != "" {
		steps, err := stepsArg(f.steps, stdin)
		if err != nil {
			return err
		}
		ops.add("/steps", steps)
	}
	if err := ops.addKV("/extra", "extra", f.extra); err != nil {
		return err
	}
	if f.ramdiskSet {
		ops.add("/disable_ramdisk", f.disableRamdisk)
	}
	if len(ops) == 0 {
		return fmt.Errorf("runbook set requires at least one attribute flag")
	}
	var rb map[string]any
	if err := ironicPatch(ctx, client, joinPath("runbooks", ref), ops, &rb); err != nil {
		return fmt.Errorf("updating baremetal runbook %s: %w", ref, f.explain(ctx, client, err))
	}
	fields, values := showFields(rb, runbookDetailFields)
	return o.WriteSingle(w, fields, values)
}

type runbookUnsetFlags struct {
	name, description, public, owner, disableRamdisk bool
	extra                                            []string
}

func newRunbookUnsetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &runbookUnsetFlags{}
	cmd := &cobra.Command{
		Use:   "unset <runbook>",
		Short: "Unset baremetal runbook properties",
		Args:  cobra.ExactArgs(1),
		RunE: runbookRunE(a, o, func(ctx context.Context, client *gophercloud.ServiceClient, cmd *cobra.Command, args []string) error {
			return runRunbookUnset(ctx, client, o, args[0], f, cmd.OutOrStdout())
		}),
	}
	fl := cmd.Flags()
	fl.BoolVar(&f.name, "name", false, "unset the name of the runbook")
	fl.BoolVar(&f.description, "description", false, "unset the description of the runbook (requires ironic API 1.112)")
	fl.BoolVar(&f.public, "public", false, "make a public runbook private")
	fl.BoolVar(&f.owner, "owner", false, "unset the owner of the runbook")
	fl.BoolVar(&f.disableRamdisk, "disable-ramdisk", false, "unset disable_ramdisk of the runbook")
	fl.StringArrayVar(&f.extra, "extra", nil, "extra key to unset (repeatable)")
	return cmd
}

func runRunbookUnset(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	ref string, f *runbookUnsetFlags, w io.Writer,
) error {
	if err := o.CheckColumns(runbookColumns...); err != nil {
		return err
	}
	// Upstream's order: name, owner, public, disable_ramdisk, description, extra.
	var ops patchBuilder
	for _, u := range []struct {
		set  bool
		path string
	}{
		{f.name, "/name"}, {f.owner, "/owner"}, {f.public, "/public"},
		{f.disableRamdisk, "/disable_ramdisk"}, {f.description, "/description"},
	} {
		if u.set {
			ops.remove(u.path)
		}
	}
	ops.removeKeys("/extra", f.extra)
	if len(ops) == 0 {
		return fmt.Errorf("runbook unset requires at least one attribute flag")
	}
	var rb map[string]any
	if err := ironicPatch(ctx, client, joinPath("runbooks", ref), ops, &rb); err != nil {
		feature := featureRunbooks
		if f.description {
			feature = featureRunbookDescription
		}
		return fmt.Errorf("updating baremetal runbook %s: %w", ref, explainMicroversion(ctx, client, feature, err))
	}
	fields, values := showFields(rb, runbookDetailFields)
	return o.WriteSingle(w, fields, values)
}

func newRunbookDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <runbook> [<runbook> ...]",
		Short: "Delete baremetal runbook(s)",
		Args:  cobra.MinimumNArgs(1),
		RunE: runbookRunE(a, o, func(ctx context.Context, client *gophercloud.ServiceClient, cmd *cobra.Command, args []string) error {
			return runRunbookDelete(ctx, client, args, cmd.OutOrStdout())
		}),
	}
}

func runRunbookDelete(ctx context.Context, client *gophercloud.ServiceClient, refs []string, w io.Writer) error {
	return batchdelete.Each(refs, func(ref string) error {
		if err := ironicDelete(ctx, client, joinPath("runbooks", ref)); err != nil {
			return fmt.Errorf("deleting baremetal runbook %s: %w", ref, explainMicroversion(ctx, client, featureRunbooks, err))
		}
		_, err := fmt.Fprintf(w, "Deleted runbook %s\n", ref)
		return err
	})
}

func newRunbookTraitListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "list <runbook>",
		Short: "List a runbook's traits",
		Args:  cobra.ExactArgs(1),
		RunE: runbookRunE(a, o, func(ctx context.Context, client *gophercloud.ServiceClient, cmd *cobra.Command, args []string) error {
			return runRunbookTraitList(ctx, client, o, args[0], cmd.OutOrStdout())
		}),
	}
}

func runRunbookTraitList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, w io.Writer) error {
	var body struct {
		Traits []string `json:"traits"`
	}
	if err := ironicGet(ctx, client, joinPath("runbooks", ref, "traits"), &body); err != nil {
		return fmt.Errorf("listing traits of baremetal runbook %s: %w", ref, explainMicroversion(ctx, client, featureRunbookTraits, err))
	}
	t := output.Table{Columns: []string{"Traits"}, Rows: make([][]any, 0, len(body.Traits))}
	for _, tr := range body.Traits {
		t.Rows = append(t.Rows, []any{tr})
	}
	return o.WriteList(w, t)
}

func newRunbookTraitAddCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "add <runbook> <trait> [<trait> ...]",
		Short: "Add traits to a runbook",
		Args:  cobra.MinimumNArgs(2),
		RunE: runbookRunE(a, o, func(ctx context.Context, client *gophercloud.ServiceClient, cmd *cobra.Command, args []string) error {
			return runRunbookTraitAdd(ctx, client, args[0], args[1:], cmd.OutOrStdout())
		}),
	}
}

func runRunbookTraitAdd(ctx context.Context, client *gophercloud.ServiceClient, ref string, traits []string, w io.Writer) error {
	return batchdelete.Each(traits, func(trait string) error {
		if err := ironicPut(ctx, client, joinPath("runbooks", ref, "traits", trait), nil); err != nil {
			return fmt.Errorf("adding trait %s to baremetal runbook %s: %w", trait, ref,
				explainMicroversion(ctx, client, featureRunbookTraits, err))
		}
		_, err := fmt.Fprintf(w, "Added trait %s\n", trait)
		return err
	})
}

func newRunbookTraitRemoveCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "remove <runbook> (--all | <trait> [<trait> ...])",
		Short: "Remove traits from a runbook",
		Args:  cobra.MinimumNArgs(1),
		RunE: runbookRunE(a, o, func(ctx context.Context, client *gophercloud.ServiceClient, cmd *cobra.Command, args []string) error {
			return runRunbookTraitRemove(ctx, client, args[0], args[1:], all, cmd.OutOrStdout())
		}),
	}
	cmd.Flags().BoolVar(&all, "all", false, "remove all traits")
	return cmd
}

func runRunbookTraitRemove(ctx context.Context, client *gophercloud.ServiceClient, ref string, traits []string, all bool, w io.Writer) error {
	switch {
	case all && len(traits) > 0:
		return fmt.Errorf("--all and trait arguments are mutually exclusive")
	case !all && len(traits) == 0:
		return fmt.Errorf("specify the trait(s) to remove, or --all")
	case all:
		if err := ironicDelete(ctx, client, joinPath("runbooks", ref, "traits")); err != nil {
			return fmt.Errorf("removing traits from baremetal runbook %s: %w", ref,
				explainMicroversion(ctx, client, featureRunbookTraits, err))
		}
		_, err := fmt.Fprintf(w, "Removed all traits from runbook %s\n", ref)
		return err
	}
	return batchdelete.Each(traits, func(trait string) error {
		if err := ironicDelete(ctx, client, joinPath("runbooks", ref, "traits", trait)); err != nil {
			return fmt.Errorf("removing trait %s from baremetal runbook %s: %w", trait, ref,
				explainMicroversion(ctx, client, featureRunbookTraits, err))
		}
		_, err := fmt.Fprintf(w, "Removed trait %s\n", trait)
		return err
	})
}

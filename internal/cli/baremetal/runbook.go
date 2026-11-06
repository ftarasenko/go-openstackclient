package baremetal

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/koc/internal/auth"
	"github.com/ftarasenko/koc/internal/cli/batchdelete"
	"github.com/ftarasenko/koc/internal/output"
	bmapi "github.com/ftarasenko/koc/pkg/openstack/baremetal"
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
	// ironic lists these with ?detail=True; unlike nodes and ports there is
	// no /detail route.
	fields, q := runbookFields, f.query()
	if f.detail(o, runbookFields, runbookDetailFields) {
		fields = runbookDetailFields
		q.Set("detail", "True")
	}
	all, err := bmapi.ListRunbooks(ctx, client, q, f.limit)
	if err != nil {
		return explained(err)
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
	rb, err := bmapi.GetRunbook(ctx, client, ref)
	if err != nil {
		return explained(err)
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
	fl.BoolVar(&f.disableRamdisk, flagDisableRamdisk, false,
		"do not boot ironic-python-agent when this runbook runs; only steps marked as not requiring it can run")
}

func (f *runbookFlags) changed(cmd *cobra.Command) {
	f.publicSet = cmd.Flags().Changed("public")
	f.ramdiskSet = cmd.Flags().Changed(flagDisableRamdisk)
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
	rb, err := bmapi.CreateRunbook(ctx, client, body)
	if err != nil {
		return explained(err)
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
	var ops bmapi.Patch
	for _, kv := range [][2]string{{"/name", f.name}, {"/description", f.description}, {"/owner", f.owner}} {
		if kv[1] != "" {
			ops.Add(kv[0], kv[1])
		}
	}
	if f.publicSet {
		ops.Add("/public", f.public)
	}
	if f.steps != "" {
		steps, err := stepsArg(f.steps, stdin)
		if err != nil {
			return err
		}
		ops.Add("/steps", steps)
	}
	if err := addKV(&ops, pathExtra, "extra", f.extra); err != nil {
		return err
	}
	if f.ramdiskSet {
		ops.Add("/disable_ramdisk", f.disableRamdisk)
	}
	if len(ops) == 0 {
		return fmt.Errorf("runbook set requires at least one attribute flag")
	}
	rb, err := bmapi.UpdateRunbook(ctx, client, ref, ops)
	if err != nil {
		return explained(err)
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
	fl.BoolVar(&f.disableRamdisk, flagDisableRamdisk, false, "unset disable_ramdisk of the runbook")
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
	var ops bmapi.Patch
	for _, u := range []struct {
		set  bool
		path string
	}{
		{f.name, "/name"}, {f.owner, "/owner"}, {f.public, "/public"},
		{f.disableRamdisk, "/disable_ramdisk"}, {f.description, "/description"},
	} {
		if u.set {
			ops.Remove(u.path)
		}
	}
	removeKeys(&ops, pathExtra, f.extra)
	if len(ops) == 0 {
		return fmt.Errorf("runbook unset requires at least one attribute flag")
	}
	rb, err := bmapi.UpdateRunbook(ctx, client, ref, ops)
	if err != nil {
		return explained(err)
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
		if err := bmapi.DeleteRunbook(ctx, client, ref); err != nil {
			return explained(err)
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
	traits, err := bmapi.ListRunbookTraits(ctx, client, ref)
	if err != nil {
		return explained(err)
	}
	t := output.Table{Columns: []string{"Traits"}, Rows: make([][]any, 0, len(traits))}
	for _, tr := range traits {
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
		if err := bmapi.AddRunbookTrait(ctx, client, ref, trait); err != nil {
			return explained(err)
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
		if err := bmapi.RemoveRunbookTraits(ctx, client, ref); err != nil {
			return explained(err)
		}
		_, err := fmt.Fprintf(w, "Removed all traits from runbook %s\n", ref)
		return err
	}
	return batchdelete.Each(traits, func(trait string) error {
		if err := bmapi.RemoveRunbookTrait(ctx, client, ref, trait); err != nil {
			return explained(err)
		}
		_, err := fmt.Fprintf(w, "Removed trait %s\n", trait)
		return err
	})
}

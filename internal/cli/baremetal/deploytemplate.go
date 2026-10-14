package baremetal

import (
	"context"
	"fmt"
	"io"
	"net/url"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Deploy templates bind deploy steps to a trait: a node scheduled to a flavor
// requiring CUSTOM_X runs the steps of the template named CUSTOM_X. gophercloud
// has no package for them, so these verbs use the raw helpers in raw.go
// (/v1/deploy_templates, API 1.55).
//
// Flag names follow python-ironicclient (`openstack baremetal deploy
// template`). UNVERIFIED against KeyStack (docs.keystack.ru unreachable, HTTP
// 403).

// deployTemplateDetailFields are python-ironicclient's
// DEPLOY_TEMPLATE_DETAILED_RESOURCE; deployTemplateFields the short list's.
var (
	deployTemplateDetailFields = []string{"uuid", "name", "steps", "extra", "created_at", "updated_at"}
	deployTemplateFields       = []string{"uuid", "name"}
	deployTemplateColumns      = knownColumns(deployTemplateDetailFields)
)

// newDeployCommand builds "baremetal deploy", the parent of upstream's
// two-word noun "deploy template".
func newDeployCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Manage baremetal deploy templates",
	}
	tmpl := &cobra.Command{
		Use:     "template",
		Short:   "Manage baremetal deploy templates (deploy steps bound to a trait)",
		Aliases: []string{"templates"},
	}
	tmpl.AddCommand(
		newDeployTemplateListCommand(a, o),
		newDeployTemplateShowCommand(a, o),
		newDeployTemplateCreateCommand(a, o),
		newDeployTemplateSetCommand(a, o),
		newDeployTemplateUnsetCommand(a, o),
		newDeployTemplateDeleteCommand(a, o),
	)
	cmd.AddCommand(tmpl)
	return cmd
}

// stepsArg reads a --steps value: a JSON or YAML string, a path to a JSON or
// YAML file, or '-' for standard input (python-ironicclient's
// handle_json_arg).
func stepsArg(arg string, stdin io.Reader) ([]map[string]any, error) {
	raw, err := readStepsDocument(arg, stdin)
	if err != nil {
		return nil, err
	}
	return parseSteps(raw)
}

type resourceListFlags struct {
	long    bool
	limit   int
	marker  string
	sortKey string
	sortDir string
}

func (f *resourceListFlags) register(cmd *cobra.Command, noun string) {
	fl := cmd.Flags()
	fl.BoolVar(&f.long, "long", false, "list additional fields in output")
	fl.IntVar(&f.limit, "limit", 0, "maximum number of "+noun+"s to return")
	fl.StringVar(&f.marker, "marker", "", "UUID of the last "+noun+" from the previous page")
	fl.StringVar(&f.sortKey, "sort-key", "", "sort output by this "+noun+" attribute")
	fl.StringVar(&f.sortDir, "sort-dir", "", "sort direction: asc or desc")
}

func (f *resourceListFlags) query() url.Values {
	return url.Values{"marker": {f.marker}, "sort_key": {f.sortKey}, "sort_dir": {f.sortDir}}
}

// detail reports whether the listing needs the detailed view: --long, or a
// -c naming a column the short listing lacks.
func (f *resourceListFlags) detail(o *output.Options, short, detailed []string) bool {
	return f.long || len(o.SelectedColumns(labels(detailed)...)) > len(o.SelectedColumns(labels(short)...))
}

func newDeployTemplateListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &resourceListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List baremetal deploy templates",
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
			return runDeployTemplateList(ctx, client, o, f, cmd.OutOrStdout())
		},
	}
	f.register(cmd, "deploy template")
	addListFieldsAliases(cmd, o)
	return cmd
}

func runDeployTemplateList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *resourceListFlags, w io.Writer) error {
	// ironic lists these with ?detail=True; unlike nodes and ports there is
	// no /detail route.
	fields, q := deployTemplateFields, f.query()
	if f.detail(o, deployTemplateFields, deployTemplateDetailFields) {
		fields = deployTemplateDetailFields
		q.Set("detail", "True")
	}
	all, err := ironicList(ctx, client, "deploy_templates", "deploy_templates", q, f.limit)
	if err != nil {
		return fmt.Errorf("listing baremetal deploy templates: %w", err)
	}
	return o.WriteList(w, listTable(all, fields))
}

func newDeployTemplateShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <template>",
		Short: "Show details of a baremetal deploy template",
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
			return runDeployTemplateShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
	addFieldsAliases(cmd, o)
	return cmd
}

func runDeployTemplateShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, w io.Writer) error {
	var t map[string]any
	if err := ironicGet(ctx, client, joinPath("deploy_templates", ref), &t); err != nil {
		return fmt.Errorf("getting baremetal deploy template %s: %w", ref, err)
	}
	fields, values := showFields(t, deployTemplateDetailFields)
	return o.WriteSingle(w, fields, values)
}

type deployTemplateFlags struct {
	uuid  string
	name  string
	steps string
	extra []string
}

func newDeployTemplateCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &deployTemplateFlags{}
	cmd := &cobra.Command{
		Use:   "create <name> --steps <steps>",
		Short: "Create a new baremetal deploy template",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			f.name = args[0]
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runDeployTemplateCreate(ctx, client, o, f, cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.uuid, "uuid", "", "UUID of the deploy template")
	fl.StringVar(&f.steps, "steps", "",
		"the deploy steps: a JSON or YAML string, a path to a JSON or YAML file, or '-' to read standard input (required)")
	fl.StringArrayVar(&f.extra, "extra", nil, "arbitrary metadata key=value (repeatable)")
	_ = cmd.MarkFlagRequired("steps")
	return cmd
}

func runDeployTemplateCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	f *deployTemplateFlags, stdin io.Reader, w io.Writer,
) error {
	if err := o.CheckColumns(deployTemplateColumns...); err != nil {
		return err
	}
	steps, err := stepsArg(f.steps, stdin)
	if err != nil {
		return err
	}
	body := map[string]any{"name": f.name, "steps": steps}
	setIf(body, "uuid", f.uuid)
	extra, err := parseKeyValMap(f.extra)
	if err != nil {
		return fmt.Errorf("parsing --extra: %w", err)
	}
	if extra != nil {
		body["extra"] = extra
	}
	var t map[string]any
	if err := ironicPost(ctx, client, "deploy_templates", body, &t); err != nil {
		return fmt.Errorf("creating baremetal deploy template: %w", err)
	}
	fields, values := showFields(t, deployTemplateDetailFields)
	return o.WriteSingle(w, fields, values)
}

func newDeployTemplateSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &deployTemplateFlags{}
	cmd := &cobra.Command{
		Use:   "set <template>",
		Short: "Set baremetal deploy template properties",
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
			return runDeployTemplateSet(ctx, client, o, args[0], f, cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "set the unique name of the deploy template; must be a valid trait name")
	fl.StringVar(&f.steps, "steps", "",
		"the deploy steps: a JSON or YAML string, a path to a JSON or YAML file, or '-' to read standard input")
	fl.StringArrayVar(&f.extra, "extra", nil, "set an extra metadata key=value (repeatable)")
	return cmd
}

func runDeployTemplateSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	ref string, f *deployTemplateFlags, stdin io.Reader, w io.Writer,
) error {
	if err := o.CheckColumns(deployTemplateColumns...); err != nil {
		return err
	}
	var ops patchBuilder
	if f.name != "" {
		ops.add("/name", f.name)
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
	if len(ops) == 0 {
		return fmt.Errorf("deploy template set requires at least one attribute flag")
	}
	return patchDeployTemplate(ctx, client, o, ref, ops, w)
}

func patchDeployTemplate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	ref string, ops patchBuilder, w io.Writer,
) error {
	var t map[string]any
	if err := ironicPatch(ctx, client, joinPath("deploy_templates", ref), ops, &t); err != nil {
		return fmt.Errorf("updating baremetal deploy template %s: %w", ref, err)
	}
	fields, values := showFields(t, deployTemplateDetailFields)
	return o.WriteSingle(w, fields, values)
}

func newDeployTemplateUnsetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var extra []string
	cmd := &cobra.Command{
		Use:   "unset <template>",
		Short: "Unset baremetal deploy template properties",
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
			return runDeployTemplateUnset(ctx, client, o, args[0], extra, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringArrayVar(&extra, "extra", nil, "extra key to unset (repeatable)")
	return cmd
}

func runDeployTemplateUnset(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	ref string, extra []string, w io.Writer,
) error {
	if err := o.CheckColumns(deployTemplateColumns...); err != nil {
		return err
	}
	var ops patchBuilder
	ops.removeKeys("/extra", extra)
	if len(ops) == 0 {
		return fmt.Errorf("deploy template unset requires at least one attribute flag")
	}
	return patchDeployTemplate(ctx, client, o, ref, ops, w)
}

func newDeployTemplateDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <template> [<template> ...]",
		Short: "Delete baremetal deploy template(s)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runDeployTemplateDelete(ctx, client, args, cmd.OutOrStdout())
		},
	}
}

func runDeployTemplateDelete(ctx context.Context, client *gophercloud.ServiceClient, refs []string, w io.Writer) error {
	return batchdelete.Each(refs, func(ref string) error {
		if err := ironicDelete(ctx, client, joinPath("deploy_templates", ref)); err != nil {
			return fmt.Errorf("deleting baremetal deploy template %s: %w", ref, err)
		}
		_, err := fmt.Fprintf(w, "Deleted deploy template %s\n", ref)
		return err
	})
}

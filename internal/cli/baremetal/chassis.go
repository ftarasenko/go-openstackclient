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

// A chassis groups nodes (a rack, an enclosure) for inventory: a node points at
// it with chassis_uuid. gophercloud has no chassis package, so these verbs use
// the raw helpers in raw.go (/v1/chassis, present since API 1.1).
//
// Flag names follow python-ironicclient (`openstack baremetal chassis`).
// UNVERIFIED against KeyStack (docs.keystack.ru unreachable, HTTP 403).

// chassisDetailFields are python-ironicclient's CHASSIS_DETAILED_RESOURCE;
// chassisFields the short list's.
var (
	chassisDetailFields = []string{"uuid", "description", "created_at", "updated_at", "extra"}
	chassisFields       = []string{"uuid", "description"}
	chassisColumns      = knownColumns(chassisDetailFields)
)

func newChassisCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "chassis",
		Short: "Manage baremetal chassis (groups of nodes)",
	}
	cmd.AddCommand(
		newChassisListCommand(a, o),
		newChassisShowCommand(a, o),
		newChassisCreateCommand(a, o),
		newChassisSetCommand(a, o),
		newChassisUnsetCommand(a, o),
		newChassisDeleteCommand(a, o),
	)
	return cmd
}

func newChassisListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &resourceListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List baremetal chassis",
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
			return runChassisList(ctx, client, o, f, cmd.OutOrStdout())
		},
	}
	f.register(cmd, "chassis")
	addListFieldsAliases(cmd, o)
	return cmd
}

func runChassisList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *resourceListFlags, w io.Writer) error {
	fields, path := chassisFields, "chassis"
	if f.detail(o, chassisFields, chassisDetailFields) {
		fields, path = chassisDetailFields, "chassis/detail"
	}
	all, err := ironicList(ctx, client, path, "chassis", f.query(), f.limit)
	if err != nil {
		return fmt.Errorf("listing baremetal chassis: %w", err)
	}
	return o.WriteList(w, listTable(all, fields))
}

func newChassisShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <chassis>",
		Short: "Show details of a baremetal chassis",
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
			return runChassisShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
	addFieldsAliases(cmd, o)
	return cmd
}

func runChassisShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, id string, w io.Writer) error {
	var c map[string]any
	if err := ironicGet(ctx, client, joinPath("chassis", id), &c); err != nil {
		return fmt.Errorf("getting baremetal chassis %s: %w", id, err)
	}
	fields, values := showFields(c, chassisDetailFields)
	return o.WriteSingle(w, fields, values)
}

type chassisFlags struct {
	uuid        string
	description string
	extra       []string
}

func newChassisCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &chassisFlags{}
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new baremetal chassis",
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
			return runChassisCreate(ctx, client, o, f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.description, "description", "", "description of the chassis")
	fl.StringArrayVar(&f.extra, "extra", nil, "arbitrary metadata key=value (repeatable)")
	fl.StringVar(&f.uuid, "uuid", "", "UUID of the chassis")
	return cmd
}

func runChassisCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *chassisFlags, w io.Writer) error {
	if err := o.CheckColumns(chassisColumns...); err != nil {
		return err
	}
	body := map[string]any{}
	setIf(body, "uuid", f.uuid)
	setIf(body, "description", f.description)
	extra, err := parseKeyValMap(f.extra)
	if err != nil {
		return fmt.Errorf("parsing --extra: %w", err)
	}
	if extra != nil {
		body["extra"] = extra
	}
	var c map[string]any
	if err := ironicPost(ctx, client, "chassis", body, &c); err != nil {
		return fmt.Errorf("creating baremetal chassis: %w", err)
	}
	fields, values := showFields(c, chassisDetailFields)
	return o.WriteSingle(w, fields, values)
}

func newChassisSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &chassisFlags{}
	cmd := &cobra.Command{
		Use:   "set <chassis>",
		Short: "Set baremetal chassis properties",
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
			return runChassisSet(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.description, "description", "", "set the description of the chassis")
	fl.StringArrayVar(&f.extra, "extra", nil, "set an extra metadata key=value (repeatable)")
	return cmd
}

func runChassisSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	id string, f *chassisFlags, w io.Writer,
) error {
	if err := o.CheckColumns(chassisColumns...); err != nil {
		return err
	}
	var ops patchBuilder
	if f.description != "" {
		ops.add("/description", f.description)
	}
	if err := ops.addKV(pathExtra, "extra", f.extra); err != nil {
		return err
	}
	if len(ops) == 0 {
		return fmt.Errorf("chassis set requires at least one attribute flag")
	}
	return patchChassis(ctx, client, o, id, ops, w)
}

func patchChassis(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	id string, ops patchBuilder, w io.Writer,
) error {
	var c map[string]any
	if err := ironicPatch(ctx, client, joinPath("chassis", id), ops, &c); err != nil {
		return fmt.Errorf("updating baremetal chassis %s: %w", id, err)
	}
	fields, values := showFields(c, chassisDetailFields)
	return o.WriteSingle(w, fields, values)
}

func newChassisUnsetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var description bool
	var extra []string
	cmd := &cobra.Command{
		Use:   "unset <chassis>",
		Short: "Unset baremetal chassis properties",
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
			return runChassisUnset(ctx, client, o, args[0], description, extra, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&description, "description", false, "clear the chassis description")
	fl.StringArrayVar(&extra, "extra", nil, "extra key to unset (repeatable)")
	return cmd
}

func runChassisUnset(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	id string, description bool, extra []string, w io.Writer,
) error {
	if err := o.CheckColumns(chassisColumns...); err != nil {
		return err
	}
	var ops patchBuilder
	if description {
		ops.remove("/description")
	}
	ops.removeKeys(pathExtra, extra)
	if len(ops) == 0 {
		return fmt.Errorf("chassis unset requires at least one attribute flag")
	}
	return patchChassis(ctx, client, o, id, ops, w)
}

func newChassisDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <chassis> [<chassis> ...]",
		Short: "Delete baremetal chassis",
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
			return runChassisDelete(ctx, client, args, cmd.OutOrStdout())
		},
	}
}

func runChassisDelete(ctx context.Context, client *gophercloud.ServiceClient, ids []string, w io.Writer) error {
	return batchdelete.Each(ids, func(id string) error {
		if err := ironicDelete(ctx, client, joinPath("chassis", id)); err != nil {
			return fmt.Errorf("deleting baremetal chassis %s: %w", id, err)
		}
		_, err := fmt.Fprintf(w, "Deleted chassis %s\n", id)
		return err
	})
}

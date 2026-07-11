package volume

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/volumetypes"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// newTypeCommand builds "volume type ...".
//
// Flag names follow upstream OSC (`openstack volume type ...`); the KeyStack
// reference (docs.keystack.ru) returned HTTP 403 at implementation time, so the
// surface is UNVERIFIED against KeyStack and falls back to upstream OSC.
func newTypeCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "type",
		Short: "Manage volume types",
	}
	cmd.AddCommand(newTypeListCommand(a, o))
	cmd.AddCommand(newTypeShowCommand(a, o))
	cmd.AddCommand(newTypeCreateCommand(a, o))
	cmd.AddCommand(newTypeDeleteCommand(a, o))
	return cmd
}

func typeShowFields(t *volumetypes.VolumeType) ([]string, []any) {
	fields := []string{"id", "name", "description", "is_public", "extra_specs", "qos_specs_id"}
	values := []any{t.ID, t.Name, t.Description, t.IsPublic, t.ExtraSpecs, t.QosSpecID}
	return fields, values
}

func newTypeListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List volume types",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newVolumeClient(ctx, a)
			if err != nil {
				return err
			}
			return runTypeList(ctx, client, o, cmd.OutOrStdout())
		},
	}
	return cmd
}

func runTypeList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, w io.Writer) error {
	pages, err := volumetypes.List(client, volumetypes.ListOpts{}).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing volume types: %w", err)
	}
	all, err := volumetypes.ExtractVolumeTypes(pages)
	if err != nil {
		return fmt.Errorf("parsing volume type list: %w", err)
	}
	t := output.Table{Columns: []string{"ID", "Name", "Is Public", "Description"}}
	for _, vt := range all {
		t.Rows = append(t.Rows, []any{vt.ID, vt.Name, vt.IsPublic, vt.Description})
	}
	return o.WriteList(w, t)
}

func newTypeShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <type>",
		Short: "Show volume type details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newVolumeClient(ctx, a)
			if err != nil {
				return err
			}
			return runTypeShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runTypeShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, w io.Writer) error {
	id, err := resolveVolumeTypeID(ctx, client, ref)
	if err != nil {
		return err
	}
	vt, err := volumetypes.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("getting volume type %q: %w", ref, err)
	}
	fields, values := typeShowFields(vt)
	return o.WriteSingle(w, fields, values)
}

type typeCreateFlags struct {
	description string
	public      bool
	private     bool
	property    []string
}

func newTypeCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &typeCreateFlags{}
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new volume type",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			if f.public && f.private {
				return fmt.Errorf("--public and --private are mutually exclusive")
			}
			ctx := cmd.Context()
			client, err := newVolumeClient(ctx, a)
			if err != nil {
				return err
			}
			visibilitySet := cmd.Flags().Changed("public") || cmd.Flags().Changed("private")
			return runTypeCreate(ctx, client, o, args[0], f, visibilitySet, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.description, "description", "", "volume type description")
	fl.BoolVar(&f.public, "public", false, "make the volume type public (default)")
	fl.BoolVar(&f.private, "private", false, "make the volume type private")
	fl.StringArrayVar(&f.property, "property", nil, "set an extra-spec key=value (repeatable)")
	return cmd
}

func runTypeCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, name string, f *typeCreateFlags, visibilitySet bool, w io.Writer) error {
	specs, err := parseKeyValMap(f.property)
	if err != nil {
		return fmt.Errorf("parsing --property: %w", err)
	}
	opts := volumetypes.CreateOpts{
		Name:        name,
		Description: f.description,
		ExtraSpecs:  specs,
	}
	// Only send is_public when the operator asked for a specific visibility.
	if visibilitySet {
		isPublic := !f.private
		opts.IsPublic = &isPublic
	}
	vt, err := volumetypes.Create(ctx, client, opts).Extract()
	if err != nil {
		return fmt.Errorf("creating volume type: %w", err)
	}
	fields, values := typeShowFields(vt)
	return o.WriteSingle(w, fields, values)
}

func newTypeDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <type>...",
		Short: "Delete one or more volume types",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newVolumeClient(ctx, a)
			if err != nil {
				return err
			}
			return runTypeDelete(ctx, client, args, cmd.OutOrStdout())
		},
	}
}

func runTypeDelete(ctx context.Context, client *gophercloud.ServiceClient, refs []string, w io.Writer) error {
	for _, ref := range refs {
		id, err := resolveVolumeTypeID(ctx, client, ref)
		if err != nil {
			return err
		}
		if err := volumetypes.Delete(ctx, client, id).ExtractErr(); err != nil {
			return fmt.Errorf("deleting volume type %q: %w", ref, err)
		}
		if _, err := fmt.Fprintf(w, "Deleted volume type: %s\n", ref); err != nil {
			return err
		}
	}
	return nil
}

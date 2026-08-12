package identity

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/services"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Flag names follow upstream OSC (`openstack service ...`). UNVERIFIED against
// KeyStack docs (https://docs.keystack.ru/ returned HTTP 403 at implementation
// time); falls back to upstream OSC semantics.

func newServiceCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "service", Short: "Manage identity catalog services"}
	cmd.AddCommand(
		newServiceListCommand(a, o),
		newServiceShowCommand(a, o),
		newServiceCreateCommand(a, o),
		newServiceSetCommand(a, o),
		newServiceDeleteCommand(a, o),
	)
	return cmd
}

func newServiceListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List services",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newIdentityClient(ctx, a)
			if err != nil {
				return err
			}
			return runServiceList(ctx, client, o, cmd.OutOrStdout())
		},
	}
}

func runServiceList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, w io.Writer) error {
	pages, err := services.List(client, services.ListOpts{}).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing services: %w", err)
	}
	all, err := services.ExtractServices(pages)
	if err != nil {
		return fmt.Errorf("parsing service list: %w", err)
	}
	t := output.Table{Columns: []string{"ID", "Name", "Type", "Enabled", "Description"}, Rows: make([][]any, 0, len(all))}
	for _, s := range all {
		t.Rows = append(t.Rows, []any{s.ID, s.Name, s.Type, s.Enabled, s.Description})
	}
	return o.WriteList(w, t)
}

func newServiceShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <service>",
		Short: "Show service details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newIdentityClient(ctx, a)
			if err != nil {
				return err
			}
			return runServiceShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runServiceShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, nameOrID string, w io.Writer) error {
	id, err := resolveServiceID(ctx, client, nameOrID)
	if err != nil {
		return err
	}
	s, err := services.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("showing service %q: %w", nameOrID, err)
	}
	return o.WriteSingle(w,
		[]string{"ID", "Name", "Type", "Enabled", "Description"},
		[]any{s.ID, s.Name, s.Type, s.Enabled, s.Description})
}

// --- create / delete / set --------------------------------------------------

// serviceWriteFlags is the shared flag set of "service create" and
// "service set". --enable and --disable are a mutually exclusive pair rather
// than one boolean, matching upstream, so leaving both off means "do not
// change" instead of "disable".
type serviceWriteFlags struct {
	name        string
	description string
	typ         string
	enable      bool
	disable     bool
}

func (f *serviceWriteFlags) register(cmd *cobra.Command, withType bool) {
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "service name")
	fl.StringVar(&f.description, "description", "", "service description")
	if withType {
		fl.StringVar(&f.typ, "type", "", "service type")
	}
	fl.BoolVar(&f.enable, "enable", false, "enable the service")
	fl.BoolVar(&f.disable, "disable", false, "disable the service")
	cmd.MarkFlagsMutuallyExclusive("enable", "disable")
}

// enabled resolves the --enable/--disable pair into the tri-state the API
// expects: nil leaves the current value alone.
func (f *serviceWriteFlags) enabled() *bool {
	switch {
	case f.enable:
		t := true
		return &t
	case f.disable:
		t := false
		return &t
	}
	return nil
}

func newServiceCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &serviceWriteFlags{}
	cmd := &cobra.Command{
		Use:   "create <type>",
		Short: "Create a new catalog service",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newIdentityClient(ctx, a)
			if err != nil {
				return err
			}
			return runServiceCreate(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	// Upstream takes the type positionally on create, so --type is not offered
	// here; it is a --type flag only on "service set".
	f.register(cmd, false)
	return cmd
}

func runServiceCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	typ string, f *serviceWriteFlags, w io.Writer,
) error {
	s, err := services.Create(ctx, client, services.CreateOpts{
		Type:        typ,
		Name:        f.name,
		Description: f.description,
		Enabled:     f.enabled(),
	}).Extract()
	if err != nil {
		return fmt.Errorf("creating service of type %q: %w", typ, err)
	}
	return o.WriteSingle(w,
		[]string{"ID", "Name", "Type", "Enabled", "Description"},
		[]any{s.ID, s.Name, s.Type, s.Enabled, s.Description})
}

func newServiceDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <service> [<service> ...]",
		Short: "Delete catalog service(s)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newIdentityClient(ctx, a)
			if err != nil {
				return err
			}
			return runServiceDelete(ctx, client, args)
		},
	}
}

func runServiceDelete(ctx context.Context, client *gophercloud.ServiceClient, refs []string) error {
	return batchdelete.Each(refs, func(ref string) error {
		id, err := resolveServiceID(ctx, client, ref)
		if err != nil {
			return err
		}
		if err := services.Delete(ctx, client, id).ExtractErr(); err != nil {
			return fmt.Errorf("deleting service %q: %w", ref, err)
		}
		return nil
	})
}

func newServiceSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &serviceWriteFlags{}
	cmd := &cobra.Command{
		Use:   "set <service>",
		Short: "Set catalog service properties",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newIdentityClient(ctx, a)
			if err != nil {
				return err
			}
			fl := cmd.Flags()
			return runServiceSet(ctx, client, args[0], f, fl.Changed("name"), fl.Changed("description"))
		},
	}
	f.register(cmd, true)
	return cmd
}

func runServiceSet(ctx context.Context, client *gophercloud.ServiceClient, ref string,
	f *serviceWriteFlags, nameSet, descSet bool,
) error {
	id, err := resolveServiceID(ctx, client, ref)
	if err != nil {
		return err
	}
	// Name and Description are *string in UpdateOpts, so an explicitly empty
	// value clears the field while an omitted flag leaves it untouched.
	opts := services.UpdateOpts{Type: f.typ, Enabled: f.enabled()}
	if nameSet {
		opts.Name = &f.name
	}
	if descSet {
		opts.Description = &f.description
	}
	if _, err := services.Update(ctx, client, id, opts).Extract(); err != nil {
		return fmt.Errorf("updating service %q: %w", ref, err)
	}
	return nil
}

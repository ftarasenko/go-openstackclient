package identity

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/domains"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Flag names follow upstream OSC (`openstack domain ...`). The KeyStack command
// reference at https://docs.keystack.ru/ returned HTTP 403 at implementation
// time, so these flags are UNVERIFIED against KeyStack and fall back to upstream
// OSC semantics.

func newDomainCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "domain", Short: "Manage domains"}
	cmd.AddCommand(
		newDomainListCommand(a, o),
		newDomainShowCommand(a, o),
		newDomainCreateCommand(a, o),
		newDomainDeleteCommand(a, o),
		newDomainSetCommand(a, o),
	)
	return cmd
}

func newDomainListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List domains",
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
			return runDomainList(ctx, client, o, cmd.OutOrStdout())
		},
	}
}

func runDomainList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, w io.Writer) error {
	pages, err := domains.List(client, domains.ListOpts{}).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing domains: %w", err)
	}
	all, err := domains.ExtractDomains(pages)
	if err != nil {
		return fmt.Errorf("parsing domain list: %w", err)
	}
	t := output.Table{Columns: []string{"ID", "Name", "Enabled", "Description"}, Rows: make([][]any, 0, len(all))}
	for _, d := range all {
		t.Rows = append(t.Rows, []any{d.ID, d.Name, d.Enabled, d.Description})
	}
	return o.WriteList(w, t)
}

func newDomainShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <domain>",
		Short: "Show domain details",
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
			return runDomainShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runDomainShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, nameOrID string, w io.Writer) error {
	id, err := resolveDomainID(ctx, client, nameOrID)
	if err != nil {
		return err
	}
	d, err := domains.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("showing domain %q: %w", nameOrID, err)
	}
	return o.WriteSingle(w,
		[]string{"ID", "Name", "Enabled", "Description"},
		[]any{d.ID, d.Name, d.Enabled, d.Description})
}

type domainWriteFlags struct {
	description string
	enable      bool
	enableSet   bool
	disableSet  bool
	name        string
}

func newDomainCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &domainWriteFlags{}
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new domain",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			f.enableSet = cmd.Flags().Changed("enable")
			f.disableSet = cmd.Flags().Changed("disable")
			if err := checkEnableDisable(f.enableSet, f.disableSet); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newIdentityClient(ctx, a)
			if err != nil {
				return err
			}
			return runDomainCreate(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.description, "description", "", "domain description")
	fl.BoolVar(&f.enable, "enable", true, "enable the domain (default)")
	fl.BoolVar(new(bool), "disable", false, "disable the domain")
	return cmd
}

func runDomainCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, name string, f *domainWriteFlags, w io.Writer) error {
	opts := domains.CreateOpts{
		Name:        name,
		Description: f.description,
		Enabled:     enabledFromFlags(f.enableSet, f.disableSet, f.enable),
	}
	d, err := domains.Create(ctx, client, opts).Extract()
	if err != nil {
		return fmt.Errorf("creating domain %q: %w", name, err)
	}
	return o.WriteSingle(w,
		[]string{"ID", "Name", "Enabled", "Description"},
		[]any{d.ID, d.Name, d.Enabled, d.Description})
}

func newDomainDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <domain>",
		Short: "Delete a domain (must be disabled first)",
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
			return runDomainDelete(ctx, client, args[0])
		},
	}
}

func runDomainDelete(ctx context.Context, client *gophercloud.ServiceClient, nameOrID string) error {
	id, err := resolveDomainID(ctx, client, nameOrID)
	if err != nil {
		return err
	}
	if err := domains.Delete(ctx, client, id).ExtractErr(); err != nil {
		return fmt.Errorf("deleting domain %q: %w", nameOrID, err)
	}
	return nil
}

func newDomainSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &domainWriteFlags{}
	cmd := &cobra.Command{
		Use:   "set <domain>",
		Short: "Update a domain",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			f.enableSet = cmd.Flags().Changed("enable")
			f.disableSet = cmd.Flags().Changed("disable")
			if err := checkEnableDisable(f.enableSet, f.disableSet); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newIdentityClient(ctx, a)
			if err != nil {
				return err
			}
			return runDomainSet(ctx, client, args[0], f, cmd.Flags().Changed("description"))
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "new domain name")
	fl.StringVar(&f.description, "description", "", "new domain description")
	fl.BoolVar(&f.enable, "enable", false, "enable the domain")
	fl.BoolVar(new(bool), "disable", false, "disable the domain")
	return cmd
}

func runDomainSet(ctx context.Context, client *gophercloud.ServiceClient, nameOrID string, f *domainWriteFlags, descSet bool) error {
	id, err := resolveDomainID(ctx, client, nameOrID)
	if err != nil {
		return err
	}
	opts := domains.UpdateOpts{
		Name:    f.name,
		Enabled: enabledFromFlags(f.enableSet, f.disableSet, f.enable),
	}
	if descSet {
		opts.Description = &f.description
	}
	if _, err := domains.Update(ctx, client, id, opts).Extract(); err != nil {
		return fmt.Errorf("updating domain %q: %w", nameOrID, err)
	}
	return nil
}

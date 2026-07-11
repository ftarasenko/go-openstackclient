package identity

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/users"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Flag names follow upstream OSC (`openstack user ...`). The KeyStack command
// reference at https://docs.keystack.ru/ returned HTTP 403 at implementation
// time, so these flags are UNVERIFIED against KeyStack and fall back to upstream
// OSC semantics.

func newUserCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "user", Short: "Manage users"}
	cmd.AddCommand(
		newUserListCommand(a, o),
		newUserShowCommand(a, o),
		newUserCreateCommand(a, o),
		newUserDeleteCommand(a, o),
		newUserSetCommand(a, o),
	)
	return cmd
}

func newUserListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var domain string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List users",
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
			return runUserList(ctx, client, o, domain, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&domain, "domain", "", "filter by domain (name or ID)")
	return cmd
}

func runUserList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, domainNameOrID string, w io.Writer) error {
	domainID, err := resolveDomainID(ctx, client, domainNameOrID)
	if err != nil {
		return err
	}
	pages, err := users.List(client, users.ListOpts{DomainID: domainID}).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing users: %w", err)
	}
	all, err := users.ExtractUsers(pages)
	if err != nil {
		return fmt.Errorf("parsing user list: %w", err)
	}
	t := output.Table{Columns: []string{"ID", "Name", "Domain ID", "Enabled"}, Rows: make([][]any, 0, len(all))}
	for _, u := range all {
		t.Rows = append(t.Rows, []any{u.ID, u.Name, u.DomainID, u.Enabled})
	}
	return o.WriteList(w, t)
}

func newUserShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var domain string
	cmd := &cobra.Command{
		Use:   "show <user>",
		Short: "Show user details",
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
			return runUserShow(ctx, client, o, args[0], domain, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&domain, "domain", "", "domain owning the user (name or ID)")
	return cmd
}

func runUserShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, nameOrID, domainNameOrID string, w io.Writer) error {
	domainID, err := resolveDomainID(ctx, client, domainNameOrID)
	if err != nil {
		return err
	}
	id, err := resolveUserID(ctx, client, nameOrID, domainID)
	if err != nil {
		return err
	}
	u, err := users.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("showing user %q: %w", nameOrID, err)
	}
	return o.WriteSingle(w,
		[]string{"ID", "Name", "Domain ID", "Enabled", "Description", "Default Project ID"},
		[]any{u.ID, u.Name, u.DomainID, u.Enabled, u.Description, u.DefaultProjectID})
}

type userWriteFlags struct {
	domain      string
	password    string
	project     string
	description string
	name        string
	enable      bool
	enableSet   bool
	disableSet  bool
}

func newUserCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &userWriteFlags{}
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new user",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			f.enableSet = cmd.Flags().Changed("enable")
			f.disableSet = cmd.Flags().Changed("disable")
			ctx := cmd.Context()
			client, err := newIdentityClient(ctx, a)
			if err != nil {
				return err
			}
			return runUserCreate(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.domain, "domain", "", "domain to create the user in (name or ID)")
	fl.StringVar(&f.password, "password", "", "user password")
	fl.StringVar(&f.project, "project", "", "default project (name or ID)")
	fl.StringVar(&f.description, "description", "", "user description")
	fl.BoolVar(&f.enable, "enable", true, "enable the user (default)")
	fl.BoolVar(new(bool), "disable", false, "disable the user")
	return cmd
}

func runUserCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, name string, f *userWriteFlags, w io.Writer) error {
	domainID, err := resolveDomainID(ctx, client, f.domain)
	if err != nil {
		return err
	}
	projectID, err := resolveProjectID(ctx, client, f.project, domainID)
	if err != nil {
		return err
	}
	opts := users.CreateOpts{
		Name:             name,
		DomainID:         domainID,
		Password:         f.password,
		DefaultProjectID: projectID,
		Description:      f.description,
		Enabled:          enabledFromFlags(f.enableSet, f.disableSet, f.enable),
	}
	u, err := users.Create(ctx, client, opts).Extract()
	if err != nil {
		return fmt.Errorf("creating user %q: %w", name, err)
	}
	return o.WriteSingle(w,
		[]string{"ID", "Name", "Domain ID", "Enabled", "Description"},
		[]any{u.ID, u.Name, u.DomainID, u.Enabled, u.Description})
}

func newUserDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var domain string
	cmd := &cobra.Command{
		Use:   "delete <user>",
		Short: "Delete a user",
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
			return runUserDelete(ctx, client, args[0], domain)
		},
	}
	cmd.Flags().StringVar(&domain, "domain", "", "domain owning the user (name or ID)")
	return cmd
}

func runUserDelete(ctx context.Context, client *gophercloud.ServiceClient, nameOrID, domainNameOrID string) error {
	domainID, err := resolveDomainID(ctx, client, domainNameOrID)
	if err != nil {
		return err
	}
	id, err := resolveUserID(ctx, client, nameOrID, domainID)
	if err != nil {
		return err
	}
	if err := users.Delete(ctx, client, id).ExtractErr(); err != nil {
		return fmt.Errorf("deleting user %q: %w", nameOrID, err)
	}
	return nil
}

func newUserSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &userWriteFlags{}
	cmd := &cobra.Command{
		Use:   "set <user>",
		Short: "Update a user",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			f.enableSet = cmd.Flags().Changed("enable")
			f.disableSet = cmd.Flags().Changed("disable")
			ctx := cmd.Context()
			client, err := newIdentityClient(ctx, a)
			if err != nil {
				return err
			}
			return runUserSet(ctx, client, args[0], f, cmd.Flags().Changed("description"))
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.domain, "domain", "", "domain owning the user (name or ID)")
	fl.StringVar(&f.name, "name", "", "new user name")
	fl.StringVar(&f.password, "password", "", "new user password")
	fl.StringVar(&f.description, "description", "", "new user description")
	fl.BoolVar(&f.enable, "enable", false, "enable the user")
	fl.BoolVar(new(bool), "disable", false, "disable the user")
	return cmd
}

func runUserSet(ctx context.Context, client *gophercloud.ServiceClient, nameOrID string, f *userWriteFlags, descSet bool) error {
	domainID, err := resolveDomainID(ctx, client, f.domain)
	if err != nil {
		return err
	}
	id, err := resolveUserID(ctx, client, nameOrID, domainID)
	if err != nil {
		return err
	}
	opts := users.UpdateOpts{
		Name:     f.name,
		Password: f.password,
		Enabled:  enabledFromFlags(f.enableSet, f.disableSet, f.enable),
	}
	if descSet {
		opts.Description = &f.description
	}
	if _, err := users.Update(ctx, client, id, opts).Extract(); err != nil {
		return fmt.Errorf("updating user %q: %w", nameOrID, err)
	}
	return nil
}

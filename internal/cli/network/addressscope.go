package network

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/layer3/addressscopes"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/security/addressgroups"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// "address scope" and "address group" — two neutron extensions that both start
// with "address" but are unrelated: a scope groups subnet pools into one
// routable domain, a group is a named set of CIDRs for security-group rules.
//
// Flag names follow upstream OSC. UNVERIFIED against KeyStack docs
// (https://docs.keystack.ru/ returned HTTP 403 at implementation time); falls
// back to upstream OSC semantics.

func newAddressCommands(a *auth.Options, o *output.Options) []*cobra.Command {
	scope := &cobra.Command{Use: "scope", Short: "Manage address scopes"}
	scope.AddCommand(
		newAddressScopeListCommand(a, o),
		newAddressScopeShowCommand(a, o),
		newAddressScopeCreateCommand(a, o),
		newAddressScopeSetCommand(a, o),
		newAddressScopeDeleteCommand(a, o),
	)
	group := &cobra.Command{Use: "group", Short: "Manage address groups"}
	group.AddCommand(
		newAddressGroupListCommand(a, o),
		newAddressGroupShowCommand(a, o),
		newAddressGroupCreateCommand(a, o),
		newAddressGroupSetCommand(a, o),
		newAddressGroupUnsetCommand(a, o),
		newAddressGroupDeleteCommand(a, o),
	)
	cmd := &cobra.Command{Use: "address", Short: "Address scope and address group commands"}
	cmd.AddCommand(scope, group)
	return []*cobra.Command{cmd}
}

// --- address scope ----------------------------------------------------------

func newAddressScopeListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var name string
	var ipVersion int
	var shared, noShared bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List address scopes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			return runAddressScopeList(ctx, client, o, name, ipVersion, shared, noShared, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&name, "name", "", "filter by name")
	fl.IntVar(&ipVersion, "ip-version", 0, "filter by IP version: 4 or 6")
	fl.BoolVar(&shared, "share", false, "list only shared address scopes")
	fl.BoolVar(&noShared, "no-share", false, "list only unshared address scopes")
	cmd.MarkFlagsMutuallyExclusive("share", "no-share")
	return cmd
}

func runAddressScopeList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	name string, ipVersion int, shared, noShared bool, w io.Writer,
) error {
	opts := addressscopes.ListOpts{Name: name, IPVersion: ipVersion}
	// ListOpts.Shared is a *bool, so both sides of the filter reach neutron —
	// unlike the plain-bool filters elsewhere, where a false is dropped.
	switch {
	case shared:
		t := true
		opts.Shared = &t
	case noShared:
		f := false
		opts.Shared = &f
	}
	pages, err := addressscopes.List(client, opts).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing address scopes: %w", err)
	}
	all, err := addressscopes.ExtractAddressScopes(pages)
	if err != nil {
		return fmt.Errorf("parsing the address scope list: %w", err)
	}
	t := output.Table{Columns: []string{"ID", "Name", "IP Version", "Shared", "Project ID"}, Rows: make([][]any, 0, len(all))}
	for _, sc := range all {
		t.Rows = append(t.Rows, []any{sc.ID, sc.Name, sc.IPVersion, sc.Shared, sc.ProjectID})
	}
	return o.WriteList(w, t)
}

func newAddressScopeShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <address-scope>",
		Short: "Show an address scope",
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
			return runAddressScopeShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runAddressScopeShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, id string, w io.Writer) error {
	sc, err := addressscopes.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("showing address scope %s: %w", id, err)
	}
	return writeAddressScope(o, w, sc)
}

func writeAddressScope(o *output.Options, w io.Writer, sc *addressscopes.AddressScope) error {
	return o.WriteSingle(w,
		[]string{"id", "name", "ip_version", "shared", "project_id"},
		[]any{sc.ID, sc.Name, sc.IPVersion, sc.Shared, sc.ProjectID})
}

func newAddressScopeCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var ipVersion int
	var share bool
	var project string
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create an address scope",
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
			return runAddressScopeCreate(ctx, client, o, args[0], ipVersion, share, project, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.IntVar(&ipVersion, "ip-version", 4, "IP version: 4 or 6")
	fl.BoolVar(&share, "share", false, "share the address scope with every project")
	fl.StringVar(&project, "project", "", "owning project ID")
	return cmd
}

func runAddressScopeCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	name string, ipVersion int, share bool, project string, w io.Writer,
) error {
	sc, err := addressscopes.Create(ctx, client, addressscopes.CreateOpts{
		Name:      name,
		IPVersion: ipVersion,
		Shared:    share,
		ProjectID: project,
	}).Extract()
	if err != nil {
		return fmt.Errorf("creating address scope %q: %w", name, err)
	}
	return writeAddressScope(o, w, sc)
}

func newAddressScopeSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var name string
	var share, noShare bool
	cmd := &cobra.Command{
		Use:   "set <address-scope>",
		Short: "Set address scope properties",
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
			return runAddressScopeSet(ctx, client, o, args[0], name, share, noShare,
				cmd.Flags().Changed("name"), cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&name, "name", "", "new name")
	fl.BoolVar(&share, "share", false, "share the address scope with every project")
	fl.BoolVar(&noShare, "no-share", false, "stop sharing the address scope")
	cmd.MarkFlagsMutuallyExclusive("share", "no-share")
	return cmd
}

func runAddressScopeSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	id, name string, share, noShare, nameSet bool, w io.Writer,
) error {
	opts := addressscopes.UpdateOpts{}
	if nameSet {
		opts.Name = &name
	}
	// Shared is a *bool so --no-share sends an explicit false rather than being
	// dropped as a zero value.
	switch {
	case share:
		t := true
		opts.Shared = &t
	case noShare:
		f := false
		opts.Shared = &f
	}
	sc, err := addressscopes.Update(ctx, client, id, opts).Extract()
	if err != nil {
		return fmt.Errorf("updating address scope %s: %w", id, err)
	}
	return writeAddressScope(o, w, sc)
}

func newAddressScopeDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <address-scope> [<address-scope> ...]",
		Short: "Delete address scope(s)",
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
			for _, id := range args {
				if err := addressscopes.Delete(ctx, client, id).ExtractErr(); err != nil {
					return fmt.Errorf("deleting address scope %s: %w", id, err)
				}
			}
			return nil
		},
	}
}

// --- address group ----------------------------------------------------------

func newAddressGroupListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var name, project string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List address groups",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			return runAddressGroupList(ctx, client, o, name, project, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&name, "name", "", "filter by name")
	fl.StringVar(&project, "project", "", "filter by project ID")
	return cmd
}

func runAddressGroupList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	name, project string, w io.Writer,
) error {
	pages, err := addressgroups.List(client, addressgroups.ListOpts{Name: name, ProjectID: project}).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing address groups: %w", err)
	}
	all, err := addressgroups.ExtractGroups(pages)
	if err != nil {
		return fmt.Errorf("parsing the address group list: %w", err)
	}
	t := output.Table{Columns: []string{"ID", "Name", "Description", "Addresses"}, Rows: make([][]any, 0, len(all))}
	for _, g := range all {
		t.Rows = append(t.Rows, []any{g.ID, g.Name, g.Description, g.Addresses})
	}
	return o.WriteList(w, t)
}

func newAddressGroupShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <address-group>",
		Short: "Show an address group",
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
			return runAddressGroupShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runAddressGroupShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, id string, w io.Writer) error {
	g, err := addressgroups.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("showing address group %s: %w", id, err)
	}
	return writeAddressGroup(o, w, g)
}

func writeAddressGroup(o *output.Options, w io.Writer, g *addressgroups.AddressGroup) error {
	return o.WriteSingle(w,
		[]string{"id", "name", "description", "addresses", "project_id"},
		[]any{g.ID, g.Name, g.Description, g.Addresses, g.ProjectID})
}

func newAddressGroupCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var description, project string
	var addresses []string
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create an address group",
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
			return runAddressGroupCreate(ctx, client, o, args[0], description, project, addresses, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&description, "description", "", "description of the address group")
	fl.StringVar(&project, "project", "", "owning project ID")
	fl.StringArrayVar(&addresses, "address", nil,
		"CIDR or IP range to include, e.g. 192.0.2.0/24 (repeatable)")
	return cmd
}

func runAddressGroupCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	name, description, project string, addresses []string, w io.Writer,
) error {
	// Neutron requires the addresses key even when empty, and gophercloud tags
	// it `required` — so a nil slice has to become an empty one.
	if addresses == nil {
		addresses = []string{}
	}
	g, err := addressgroups.Create(ctx, client, addressgroups.CreateOpts{
		Name:        name,
		Description: description,
		ProjectID:   project,
		Addresses:   addresses,
	}).Extract()
	if err != nil {
		return fmt.Errorf("creating address group %q: %w", name, err)
	}
	return writeAddressGroup(o, w, g)
}

func newAddressGroupSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var name, description string
	var addresses []string
	cmd := &cobra.Command{
		Use:   "set <address-group>",
		Short: "Set address group properties or add addresses",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			fl := cmd.Flags()
			c, err := newNetworkClient(cmd.Context(), a)
			if err != nil {
				return err
			}
			return runAddressGroupSet(cmd.Context(), c, o, args[0], name, description, addresses,
				fl.Changed("name"), fl.Changed("description"), cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&name, "name", "", "new name")
	fl.StringVar(&description, "description", "", "new description")
	fl.StringArrayVar(&addresses, "address", nil, "address to add to the group (repeatable)")
	return cmd
}

func newAddressGroupUnsetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var addresses []string
	cmd := &cobra.Command{
		Use:   "unset <address-group>",
		Short: "Remove addresses from an address group",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			if len(addresses) == 0 {
				return fmt.Errorf("address group unset requires at least one --address")
			}
			c, err := newNetworkClient(cmd.Context(), a)
			if err != nil {
				return err
			}
			return runAddressGroupRemoveAddresses(cmd.Context(), c, o, args[0], addresses, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringArrayVar(&addresses, "address", nil, "address to remove from the group (repeatable)")
	return cmd
}

func newAddressGroupDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <address-group> [<address-group> ...]",
		Short: "Delete address group(s)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			c, err := newNetworkClient(cmd.Context(), a)
			if err != nil {
				return err
			}
			for _, id := range args {
				if err := addressgroups.Delete(cmd.Context(), c, id).ExtractErr(); err != nil {
					return fmt.Errorf("deleting address group %s: %w", id, err)
				}
			}
			return nil
		},
	}
}

// runAddressGroupSet renames or re-describes the group and, with --address,
// adds addresses through neutron's dedicated add_addresses action — the plain
// update has no addresses field at all.
func runAddressGroupSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	id, name, description string, addresses []string, nameSet, descSet bool, w io.Writer,
) error {
	if nameSet || descSet {
		opts := addressgroups.UpdateOpts{}
		if nameSet {
			opts.Name = &name
		}
		if descSet {
			opts.Description = &description
		}
		if _, err := addressgroups.Update(ctx, client, id, opts).Extract(); err != nil {
			return fmt.Errorf("updating address group %s: %w", id, err)
		}
	}
	if len(addresses) > 0 {
		if _, err := addressgroups.AddAddresses(ctx, client, id,
			addressgroups.UpdateAddressesOpts{Addresses: addresses}).Extract(); err != nil {
			return fmt.Errorf("adding addresses to address group %s: %w", id, err)
		}
	}
	return runAddressGroupShow(ctx, client, o, id, w)
}

func runAddressGroupRemoveAddresses(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	id string, addresses []string, w io.Writer,
) error {
	if _, err := addressgroups.RemoveAddresses(ctx, client, id,
		addressgroups.UpdateAddressesOpts{Addresses: addresses}).Extract(); err != nil {
		return fmt.Errorf("removing addresses from address group %s: %w", id, err)
	}
	return runAddressGroupShow(ctx, client, o, id, w)
}

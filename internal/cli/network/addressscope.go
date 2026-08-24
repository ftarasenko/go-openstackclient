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
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
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

type addressScopeListFlags struct {
	name      string
	ipVersion int
	shared    bool
	noShared  bool
}

func newAddressScopeListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &addressScopeListFlags{}
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
			return runAddressScopeList(ctx, client, o, f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "filter by name")
	fl.IntVar(&f.ipVersion, "ip-version", 0, "filter by IP version: 4 or 6")
	fl.BoolVar(&f.shared, flagShare, false, "list only shared address scopes")
	fl.BoolVar(&f.noShared, flagNoShare, false, "list only unshared address scopes")
	cmd.MarkFlagsMutuallyExclusive(flagShare, flagNoShare)
	return cmd
}

func runAddressScopeList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	f *addressScopeListFlags, w io.Writer,
) error {
	opts := addressscopes.ListOpts{Name: f.name, IPVersion: f.ipVersion}
	// ListOpts.Shared is a *bool, so both sides of the filter reach neutron —
	// unlike the plain-bool filters elsewhere, where a false is dropped.
	switch {
	case f.shared:
		t := true
		opts.Shared = &t
	case f.noShared:
		no := false
		opts.Shared = &no
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
	id, err := resolveAddressScopeID(ctx, client, id)
	if err != nil {
		return err
	}
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

type addressScopeCreateFlags struct {
	ipVersion int
	share     bool
	project   string
}

func newAddressScopeCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &addressScopeCreateFlags{}
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
			return runAddressScopeCreate(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.IntVar(&f.ipVersion, "ip-version", 4, "IP version: 4 or 6")
	fl.BoolVar(&f.share, flagShare, false, "share the address scope with every project")
	fl.StringVar(&f.project, "project", "", "owning project ID")
	return cmd
}

func runAddressScopeCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	name string, f *addressScopeCreateFlags, w io.Writer,
) error {
	sc, err := addressscopes.Create(ctx, client, addressscopes.CreateOpts{
		Name:      name,
		IPVersion: f.ipVersion,
		Shared:    f.share,
		ProjectID: f.project,
	}).Extract()
	if err != nil {
		return fmt.Errorf("creating address scope %q: %w", name, err)
	}
	return writeAddressScope(o, w, sc)
}

type addressScopeSetFlags struct {
	name    string
	share   bool
	noShare bool

	// nameSet records whether --name was given, so an empty new name is still
	// distinguishable from "leave the name alone".
	nameSet bool
}

func newAddressScopeSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &addressScopeSetFlags{}
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
			f.nameSet = cmd.Flags().Changed("name")
			return runAddressScopeSet(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "new name")
	fl.BoolVar(&f.share, flagShare, false, "share the address scope with every project")
	fl.BoolVar(&f.noShare, flagNoShare, false, "stop sharing the address scope")
	cmd.MarkFlagsMutuallyExclusive(flagShare, flagNoShare)
	return cmd
}

func runAddressScopeSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	id string, f *addressScopeSetFlags, w io.Writer,
) error {
	id, err := resolveAddressScopeID(ctx, client, id)
	if err != nil {
		return err
	}
	opts := addressscopes.UpdateOpts{}
	if f.nameSet {
		opts.Name = &f.name
	}
	// Shared is a *bool so --no-share sends an explicit false rather than being
	// dropped as a zero value.
	switch {
	case f.share:
		t := true
		opts.Shared = &t
	case f.noShare:
		no := false
		opts.Shared = &no
	}
	sc, err2 := addressscopes.Update(ctx, client, id, opts).Extract()
	if err2 != nil {
		return fmt.Errorf("updating address scope %s: %w", id, err2)
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
			return runAddressScopeDelete(ctx, client, args)
		},
	}
}

func runAddressScopeDelete(ctx context.Context, client *gophercloud.ServiceClient, refs []string) error {
	return batchdelete.Each(refs, func(ref string) error {
		id, err := resolveAddressScopeID(ctx, client, ref)
		if err != nil {
			return err
		}
		if err := addressscopes.Delete(ctx, client, id).ExtractErr(); err != nil {
			return fmt.Errorf("deleting address scope %s: %w", ref, err)
		}
		return nil
	})
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
	id, err := resolveAddressGroupID(ctx, client, id)
	if err != nil {
		return err
	}
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

type addressGroupCreateFlags struct {
	description string
	project     string
	addresses   []string
}

func newAddressGroupCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &addressGroupCreateFlags{}
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
			return runAddressGroupCreate(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.description, "description", "", "description of the address group")
	fl.StringVar(&f.project, "project", "", "owning project ID")
	fl.StringArrayVar(&f.addresses, "address", nil,
		"CIDR or IP range to include, e.g. 192.0.2.0/24 (repeatable)")
	return cmd
}

func runAddressGroupCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	name string, f *addressGroupCreateFlags, w io.Writer,
) error {
	// Neutron requires the addresses key even when empty, and gophercloud tags
	// it `required` — so a nil slice has to become an empty one.
	addresses := f.addresses
	if addresses == nil {
		addresses = []string{}
	}
	g, err := addressgroups.Create(ctx, client, addressgroups.CreateOpts{
		Name:        name,
		Description: f.description,
		ProjectID:   f.project,
		Addresses:   addresses,
	}).Extract()
	if err != nil {
		return fmt.Errorf("creating address group %q: %w", name, err)
	}
	return writeAddressGroup(o, w, g)
}

type addressGroupSetFlags struct {
	name        string
	description string
	addresses   []string

	// nameSet/descSet record which of the two were given: an empty value is a
	// meaningful update, so neither can be inferred from the value alone.
	nameSet bool
	descSet bool
}

func newAddressGroupSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &addressGroupSetFlags{}
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
			f.nameSet, f.descSet = fl.Changed("name"), fl.Changed("description")
			return runAddressGroupSet(cmd.Context(), c, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "new name")
	fl.StringVar(&f.description, "description", "", "new description")
	fl.StringArrayVar(&f.addresses, "address", nil, "address to add to the group (repeatable)")
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
			return runAddressGroupDelete(cmd.Context(), c, args)
		},
	}
}

func runAddressGroupDelete(ctx context.Context, client *gophercloud.ServiceClient, refs []string) error {
	return batchdelete.Each(refs, func(ref string) error {
		id, err := resolveAddressGroupID(ctx, client, ref)
		if err != nil {
			return err
		}
		if err := addressgroups.Delete(ctx, client, id).ExtractErr(); err != nil {
			return fmt.Errorf("deleting address group %s: %w", ref, err)
		}
		return nil
	})
}

// runAddressGroupSet renames or re-describes the group and, with --address,
// adds addresses through neutron's dedicated add_addresses action — the plain
// update has no addresses field at all.
func runAddressGroupSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	id string, f *addressGroupSetFlags, w io.Writer,
) error {
	id, err := resolveAddressGroupID(ctx, client, id)
	if err != nil {
		return err
	}
	if f.nameSet || f.descSet {
		opts := addressgroups.UpdateOpts{}
		if f.nameSet {
			opts.Name = &f.name
		}
		if f.descSet {
			opts.Description = &f.description
		}
		if _, err := addressgroups.Update(ctx, client, id, opts).Extract(); err != nil {
			return fmt.Errorf("updating address group %s: %w", id, err)
		}
	}
	if len(f.addresses) > 0 {
		if _, err := addressgroups.AddAddresses(ctx, client, id,
			addressgroups.UpdateAddressesOpts{Addresses: f.addresses}).Extract(); err != nil {
			return fmt.Errorf("adding addresses to address group %s: %w", id, err)
		}
	}
	return runAddressGroupShow(ctx, client, o, id, w)
}

func runAddressGroupRemoveAddresses(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	id string, addresses []string, w io.Writer,
) error {
	id, err := resolveAddressGroupID(ctx, client, id)
	if err != nil {
		return err
	}
	if _, err := addressgroups.RemoveAddresses(ctx, client, id,
		addressgroups.UpdateAddressesOpts{Addresses: addresses}).Extract(); err != nil {
		return fmt.Errorf("removing addresses from address group %s: %w", id, err)
	}
	return runAddressGroupShow(ctx, client, o, id, w)
}

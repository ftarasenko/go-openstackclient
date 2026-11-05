package dns

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
	"github.com/ftarasenko/go-openstackclient/internal/output"
	dnsapi "github.com/ftarasenko/go-openstackclient/pkg/openstack/dns"
)

// Zone blacklists and TLDs: the two deployment-wide policy resources that decide
// which zone names tenants may create. Neither has a gophercloud package, so both
// go through the raw helpers in raw.go.
//
// Command and flag names follow upstream python-designateclient 7.0.0
// (`openstack zone blacklist create|list|show|set|delete`,
// `openstack tld create|list|show|set|delete`). The KeyStack command reference at
// https://docs.keystack.ru/ was not reachable at implementation time (HTTP 403),
// so these are UNVERIFIED against KeyStack and fall back to upstream semantics.

// --- zone blacklist --------------------------------------------------------

func blacklistFields(b *dnsapi.Blacklist) ([]string, []any) {
	return []string{"id", "pattern", "description", "created_at", "updated_at"},
		[]any{b.ID, b.Pattern, b.Description, dnsTimeString(b.CreatedAt), dnsTimeString(b.UpdatedAt)}
}

func newZoneBlacklistCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "blacklist",
		Short: "Manage the zone-name patterns tenants may not create",
	}
	cmd.AddCommand(
		newZoneBlacklistListCommand(a, o),
		newZoneBlacklistShowCommand(a, o),
		newZoneBlacklistCreateCommand(a, o),
		newZoneBlacklistSetCommand(a, o),
		newZoneBlacklistDeleteCommand(a, o),
	)
	return cmd
}

func newZoneBlacklistListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var pattern string
	var limit int
	common := &commonOptions{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List blacklisted zone-name patterns",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newDNSClient(ctx, a)
			if err != nil {
				return err
			}
			return runZoneBlacklistList(ctx, client, o, pattern, limit, common, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&pattern, "pattern", "", "filter by blacklist pattern")
	fl.IntVar(&limit, "limit", 0, "maximum number of blacklists to return")
	common.bind(cmd)
	return cmd
}

func runZoneBlacklistList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	pattern string, limit int, common *commonOptions, w io.Writer,
) error {
	all, err := dnsapi.ListBlacklists(ctx, withCommonHeaders(client, common), pattern, limit)
	if err != nil {
		return err
	}
	t := output.Table{
		Columns: []string{"ID", "Pattern", "Description"},
		Rows:    make([][]any, 0, len(all)),
	}
	for _, b := range all {
		t.Rows = append(t.Rows, []any{b.ID, b.Pattern, b.Description})
	}
	return o.WriteList(w, t)
}

// resolveBlacklistID turns a blacklist reference into an ID. Upstream takes the ID
// only; koc additionally accepts the pattern, since that is what an operator has
// in hand after reading a "zone name is blacklisted" error. Matching more than one
// is an error rather than a silent pick.
func resolveBlacklistID(ctx context.Context, client *gophercloud.ServiceClient,
	ref string, common *commonOptions,
) (string, error) {
	return lookup("blacklist pattern %q is ambiguous: %d matches, use the ID", ref, func() (string, error) {
		return dnsapi.FindBlacklistID(ctx, withCommonHeaders(client, common), ref)
	})
}

func newZoneBlacklistShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	common := &commonOptions{}
	cmd := &cobra.Command{
		Use:   "show <blacklist>",
		Short: "Show blacklist details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newDNSClient(ctx, a)
			if err != nil {
				return err
			}
			return runZoneBlacklistShow(ctx, client, o, args[0], common, cmd.OutOrStdout())
		},
	}
	common.bind(cmd)
	return cmd
}

func runZoneBlacklistShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	ref string, common *commonOptions, w io.Writer,
) error {
	id, err := resolveBlacklistID(ctx, client, ref, common)
	if err != nil {
		return err
	}
	b, err := dnsapi.GetBlacklist(ctx, withCommonHeaders(client, common), id)
	if err != nil {
		return relabel(err, "showing blacklist %q", ref)
	}
	fields, values := blacklistFields(b)
	return o.WriteSingle(w, fields, values)
}

func newZoneBlacklistCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var pattern, description string
	common := &commonOptions{}
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Blacklist a zone-name pattern",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			if pattern == "" {
				return fmt.Errorf("--pattern is required")
			}
			ctx := cmd.Context()
			client, err := newDNSClient(ctx, a)
			if err != nil {
				return err
			}
			return runZoneBlacklistCreate(ctx, client, o, pattern, description, common, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	// Upstream takes the pattern as a required option, not a positional.
	fl.StringVar(&pattern, "pattern", "", "regular expression matching the zone names to refuse (required)")
	fl.StringVar(&description, flagDescription, "", flagDescription)
	common.bind(cmd)
	return cmd
}

func runZoneBlacklistCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	pattern, description string, common *commonOptions, w io.Writer,
) error {
	if err := o.CheckColumns(blacklistColumns...); err != nil {
		return err
	}
	b, err := dnsapi.CreateBlacklist(ctx, withCommonHeaders(client, common), pattern, description)
	if err != nil {
		return err
	}
	fields, values := blacklistFields(b)
	return o.WriteSingle(w, fields, values)
}

type blacklistSetFlags struct {
	pattern       string
	description   string
	noDescription bool
	common        *commonOptions
}

func newZoneBlacklistSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &blacklistSetFlags{common: &commonOptions{}}
	cmd := &cobra.Command{
		Use:   "set <blacklist>",
		Short: "Update a blacklist",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			fl := cmd.Flags()
			if !fl.Changed("pattern") && !fl.Changed(flagDescription) && !f.noDescription {
				return fmt.Errorf("nothing to set: pass --pattern, --description or --no-description")
			}
			ctx := cmd.Context()
			client, err := newDNSClient(ctx, a)
			if err != nil {
				return err
			}
			return runZoneBlacklistSet(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.pattern, "pattern", "", "new pattern")
	fl.StringVar(&f.description, flagDescription, "", "new description")
	fl.BoolVar(&f.noDescription, flagNoDescription, false, "clear the description")
	cmd.MarkFlagsMutuallyExclusive(flagDescription, flagNoDescription)
	f.common.bind(cmd)
	return cmd
}

// runZoneBlacklistSet PATCHes only the named attributes. --no-description sends an
// explicit JSON null, which is the only way to clear the field; an omitted key
// leaves it alone.
func runZoneBlacklistSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	ref string, f *blacklistSetFlags, w io.Writer,
) error {
	if err := o.CheckColumns(blacklistColumns...); err != nil {
		return err
	}
	id, err := resolveBlacklistID(ctx, client, ref, f.common)
	if err != nil {
		return err
	}
	body := make(map[string]any, 2)
	if f.pattern != "" {
		body["pattern"] = f.pattern
	}
	switch {
	case f.noDescription:
		body["description"] = nil
	case f.description != "":
		body["description"] = f.description
	}
	b, err := dnsapi.UpdateBlacklist(ctx, withCommonHeaders(client, f.common), id, body)
	if err != nil {
		return relabel(err, "updating blacklist %q", ref)
	}
	fields, values := blacklistFields(b)
	return o.WriteSingle(w, fields, values)
}

func newZoneBlacklistDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	common := &commonOptions{}
	cmd := &cobra.Command{
		Use:   "delete <blacklist> [<blacklist>...]",
		Short: "Delete one or more blacklists",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newDNSClient(ctx, a)
			if err != nil {
				return err
			}
			return runZoneBlacklistDelete(ctx, client, args, common, cmd.OutOrStdout())
		},
	}
	common.bind(cmd)
	return cmd
}

func runZoneBlacklistDelete(ctx context.Context, client *gophercloud.ServiceClient,
	refs []string, common *commonOptions, w io.Writer,
) error {
	return batchdelete.Each(refs, func(ref string) error {
		id, err := resolveBlacklistID(ctx, client, ref, common)
		if err != nil {
			return err
		}
		if err := dnsapi.DeleteBlacklist(ctx, withCommonHeaders(client, common), id); err != nil {
			return relabel(err, "deleting blacklist %q", ref)
		}
		if _, err := fmt.Fprintf(w, "Deleted blacklist %s\n", ref); err != nil {
			return err
		}
		return nil
	})
}

// --- tld -------------------------------------------------------------------

func tldFields(t *dnsapi.TLD) ([]string, []any) {
	return []string{"id", "name", "description", "created_at", "updated_at"},
		[]any{t.ID, t.Name, t.Description, dnsTimeString(t.CreatedAt), dnsTimeString(t.UpdatedAt)}
}

func newTLDCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tld",
		Short: "Manage the top-level domains zones may be created under",
	}
	cmd.AddCommand(
		newTLDListCommand(a, o),
		newTLDShowCommand(a, o),
		newTLDCreateCommand(a, o),
		newTLDSetCommand(a, o),
		newTLDDeleteCommand(a, o),
	)
	return cmd
}

// resolveTLDID turns a TLD reference into an ID. Upstream resolves the name too
// (designateclient's tlds controller calls resolve_by_name), so a name is the
// normal way to address one.
func resolveTLDID(ctx context.Context, client *gophercloud.ServiceClient,
	ref string, common *commonOptions,
) (string, error) {
	return lookup("TLD name %q is ambiguous: %d matches, use the ID", ref, func() (string, error) {
		return dnsapi.FindTLDID(ctx, withCommonHeaders(client, common), ref)
	})
}

func newTLDListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var name string
	var limit int
	common := &commonOptions{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List top-level domains",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newDNSClient(ctx, a)
			if err != nil {
				return err
			}
			return runTLDList(ctx, client, o, name, limit, common, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&name, "name", "", "filter by TLD name")
	fl.IntVar(&limit, "limit", 0, "maximum number of TLDs to return")
	common.bind(cmd)
	return cmd
}

func runTLDList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	name string, limit int, common *commonOptions, w io.Writer,
) error {
	all, err := dnsapi.ListTLDs(ctx, withCommonHeaders(client, common), name, limit)
	if err != nil {
		return err
	}
	t := output.Table{
		Columns: []string{"ID", "Name", "Description"},
		Rows:    make([][]any, 0, len(all)),
	}
	for _, item := range all {
		t.Rows = append(t.Rows, []any{item.ID, item.Name, item.Description})
	}
	return o.WriteList(w, t)
}

func newTLDShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	common := &commonOptions{}
	cmd := &cobra.Command{
		Use:   "show <tld>",
		Short: "Show TLD details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newDNSClient(ctx, a)
			if err != nil {
				return err
			}
			return runTLDShow(ctx, client, o, args[0], common, cmd.OutOrStdout())
		},
	}
	common.bind(cmd)
	return cmd
}

func runTLDShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	ref string, common *commonOptions, w io.Writer,
) error {
	id, err := resolveTLDID(ctx, client, ref, common)
	if err != nil {
		return err
	}
	item, err := dnsapi.GetTLD(ctx, withCommonHeaders(client, common), id)
	if err != nil {
		return relabel(err, "showing TLD %q", ref)
	}
	fields, values := tldFields(item)
	return o.WriteSingle(w, fields, values)
}

func newTLDCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var name, description string
	common := &commonOptions{}
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a TLD",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			if name == "" {
				return fmt.Errorf("--name is required")
			}
			ctx := cmd.Context()
			client, err := newDNSClient(ctx, a)
			if err != nil {
				return err
			}
			return runTLDCreate(ctx, client, o, name, description, common, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	// Upstream takes the name as a required option, not a positional.
	fl.StringVar(&name, "name", "", "TLD name, e.g. com (required)")
	fl.StringVar(&description, flagDescription, "", flagDescription)
	common.bind(cmd)
	return cmd
}

func runTLDCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	name, description string, common *commonOptions, w io.Writer,
) error {
	if err := o.CheckColumns(tldColumns...); err != nil {
		return err
	}
	item, err := dnsapi.CreateTLD(ctx, withCommonHeaders(client, common), name, description)
	if err != nil {
		return err
	}
	fields, values := tldFields(item)
	return o.WriteSingle(w, fields, values)
}

type tldSetFlags struct {
	name          string
	description   string
	noDescription bool
	common        *commonOptions
}

func newTLDSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &tldSetFlags{common: &commonOptions{}}
	cmd := &cobra.Command{
		Use:   "set <tld>",
		Short: "Update a TLD",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			fl := cmd.Flags()
			if !fl.Changed("name") && !fl.Changed(flagDescription) && !f.noDescription {
				return fmt.Errorf("nothing to set: pass --name, --description or --no-description")
			}
			ctx := cmd.Context()
			client, err := newDNSClient(ctx, a)
			if err != nil {
				return err
			}
			return runTLDSet(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "new TLD name")
	fl.StringVar(&f.description, flagDescription, "", "new description")
	fl.BoolVar(&f.noDescription, flagNoDescription, false, "clear the description")
	cmd.MarkFlagsMutuallyExclusive(flagDescription, flagNoDescription)
	f.common.bind(cmd)
	return cmd
}

func runTLDSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	ref string, f *tldSetFlags, w io.Writer,
) error {
	if err := o.CheckColumns(tldColumns...); err != nil {
		return err
	}
	id, err := resolveTLDID(ctx, client, ref, f.common)
	if err != nil {
		return err
	}
	body := make(map[string]any, 2)
	if f.name != "" {
		body["name"] = f.name
	}
	switch {
	case f.noDescription:
		body["description"] = nil
	case f.description != "":
		body["description"] = f.description
	}
	item, err := dnsapi.UpdateTLD(ctx, withCommonHeaders(client, f.common), id, body)
	if err != nil {
		return relabel(err, "updating TLD %q", ref)
	}
	fields, values := tldFields(item)
	return o.WriteSingle(w, fields, values)
}

func newTLDDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	common := &commonOptions{}
	cmd := &cobra.Command{
		Use:   "delete <tld> [<tld>...]",
		Short: "Delete one or more TLDs",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newDNSClient(ctx, a)
			if err != nil {
				return err
			}
			return runTLDDelete(ctx, client, args, common, cmd.OutOrStdout())
		},
	}
	common.bind(cmd)
	return cmd
}

func runTLDDelete(ctx context.Context, client *gophercloud.ServiceClient,
	refs []string, common *commonOptions, w io.Writer,
) error {
	return batchdelete.Each(refs, func(ref string) error {
		id, err := resolveTLDID(ctx, client, ref, common)
		if err != nil {
			return err
		}
		if err := dnsapi.DeleteTLD(ctx, withCommonHeaders(client, common), id); err != nil {
			return relabel(err, "deleting TLD %q", ref)
		}
		if _, err := fmt.Fprintf(w, "Deleted TLD %s\n", ref); err != nil {
			return err
		}
		return nil
	})
}

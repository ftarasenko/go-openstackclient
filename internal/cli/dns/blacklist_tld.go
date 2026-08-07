package dns

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/resolve"
	"github.com/ftarasenko/go-openstackclient/internal/output"
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

type blacklist struct {
	ID          string `json:"id"`
	Pattern     string `json:"pattern"`
	Description string `json:"description"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

func blacklistFields(b *blacklist) ([]string, []any) {
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

// listBlacklists reads every page of /v2/blacklists, optionally filtered.
func listBlacklists(ctx context.Context, client *gophercloud.ServiceClient,
	pattern string, limit int, headers map[string]string,
) ([]blacklist, error) {
	q, err := dnsQuery(struct {
		Pattern string `q:"pattern"`
	}{pattern})
	if err != nil {
		return nil, err
	}
	return dnsListAll(ctx, client, client.ServiceURL("blacklists")+q, headers, limit,
		func(raw json.RawMessage) ([]blacklist, string, error) {
			var page struct {
				Blacklists []blacklist `json:"blacklists"`
				Links      dnsLinks    `json:"links"`
			}
			if err := json.Unmarshal(raw, &page); err != nil {
				return nil, "", fmt.Errorf("parsing blacklist list: %w", err)
			}
			return page.Blacklists, page.Links.Next, nil
		})
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
	all, err := listBlacklists(ctx, client, pattern, limit, common.headers())
	if err != nil {
		return fmt.Errorf("listing blacklists: %w", err)
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
	ref string, headers map[string]string,
) (string, error) {
	if resolve.IsUUID(ref) {
		return ref, nil
	}
	all, err := listBlacklists(ctx, client, ref, 0, headers)
	if err != nil {
		return "", fmt.Errorf("looking up blacklist %q: %w", ref, err)
	}
	switch len(all) {
	case 0:
		return ref, nil
	case 1:
		return all[0].ID, nil
	default:
		return "", fmt.Errorf("blacklist pattern %q is ambiguous: %d matches, use the ID", ref, len(all))
	}
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
	headers := common.headers()
	id, err := resolveBlacklistID(ctx, client, ref, headers)
	if err != nil {
		return err
	}
	var b blacklist
	if err := dnsGetJSON(ctx, client, client.ServiceURL("blacklists", id), headers, &b); err != nil {
		return fmt.Errorf("showing blacklist %q: %w", ref, err)
	}
	fields, values := blacklistFields(&b)
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
	fl.StringVar(&description, "description", "", "description")
	common.bind(cmd)
	return cmd
}

func runZoneBlacklistCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	pattern, description string, common *commonOptions, w io.Writer,
) error {
	body := map[string]any{"pattern": pattern}
	if description != "" {
		body["description"] = description
	}
	var b blacklist
	if err := dnsPostJSON(ctx, client, client.ServiceURL("blacklists"), body, &b, common.headers()); err != nil {
		return fmt.Errorf("creating blacklist %q: %w", pattern, err)
	}
	fields, values := blacklistFields(&b)
	return o.WriteSingle(w, fields, values)
}

func newZoneBlacklistSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var pattern, description string
	var noDescription bool
	common := &commonOptions{}
	cmd := &cobra.Command{
		Use:   "set <blacklist>",
		Short: "Update a blacklist",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			fl := cmd.Flags()
			if !fl.Changed("pattern") && !fl.Changed("description") && !noDescription {
				return fmt.Errorf("nothing to set: pass --pattern, --description or --no-description")
			}
			ctx := cmd.Context()
			client, err := newDNSClient(ctx, a)
			if err != nil {
				return err
			}
			return runZoneBlacklistSet(ctx, client, o, args[0], pattern, description, noDescription,
				common, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&pattern, "pattern", "", "new pattern")
	fl.StringVar(&description, "description", "", "new description")
	fl.BoolVar(&noDescription, "no-description", false, "clear the description")
	cmd.MarkFlagsMutuallyExclusive("description", "no-description")
	common.bind(cmd)
	return cmd
}

// runZoneBlacklistSet PATCHes only the named attributes. --no-description sends an
// explicit JSON null, which is the only way to clear the field; an omitted key
// leaves it alone.
func runZoneBlacklistSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	ref, pattern, description string, noDescription bool, common *commonOptions, w io.Writer,
) error {
	headers := common.headers()
	id, err := resolveBlacklistID(ctx, client, ref, headers)
	if err != nil {
		return err
	}
	body := make(map[string]any, 2)
	if pattern != "" {
		body["pattern"] = pattern
	}
	switch {
	case noDescription:
		body["description"] = nil
	case description != "":
		body["description"] = description
	}
	var b blacklist
	if err := dnsPatchJSON(ctx, client, client.ServiceURL("blacklists", id), body, &b, headers); err != nil {
		return fmt.Errorf("updating blacklist %q: %w", ref, err)
	}
	fields, values := blacklistFields(&b)
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
	headers := common.headers()
	for _, ref := range refs {
		id, err := resolveBlacklistID(ctx, client, ref, headers)
		if err != nil {
			return err
		}
		if err := dnsDelete(ctx, client, client.ServiceURL("blacklists", id), headers); err != nil {
			return fmt.Errorf("deleting blacklist %q: %w", ref, err)
		}
		if _, err := fmt.Fprintf(w, "Deleted blacklist %s\n", ref); err != nil {
			return err
		}
	}
	return nil
}

// --- tld -------------------------------------------------------------------

type tld struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

func tldFields(t *tld) ([]string, []any) {
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

func listTLDs(ctx context.Context, client *gophercloud.ServiceClient,
	name string, limit int, headers map[string]string,
) ([]tld, error) {
	q, err := dnsQuery(struct {
		Name string `q:"name"`
	}{name})
	if err != nil {
		return nil, err
	}
	return dnsListAll(ctx, client, client.ServiceURL("tlds")+q, headers, limit,
		func(raw json.RawMessage) ([]tld, string, error) {
			var page struct {
				TLDs  []tld    `json:"tlds"`
				Links dnsLinks `json:"links"`
			}
			if err := json.Unmarshal(raw, &page); err != nil {
				return nil, "", fmt.Errorf("parsing TLD list: %w", err)
			}
			return page.TLDs, page.Links.Next, nil
		})
}

// resolveTLDID turns a TLD reference into an ID. Upstream resolves the name too
// (designateclient's tlds controller calls resolve_by_name), so a name is the
// normal way to address one.
func resolveTLDID(ctx context.Context, client *gophercloud.ServiceClient,
	ref string, headers map[string]string,
) (string, error) {
	if resolve.IsUUID(ref) {
		return ref, nil
	}
	all, err := listTLDs(ctx, client, ref, 0, headers)
	if err != nil {
		return "", fmt.Errorf("looking up TLD %q: %w", ref, err)
	}
	switch len(all) {
	case 0:
		return ref, nil
	case 1:
		return all[0].ID, nil
	default:
		return "", fmt.Errorf("TLD name %q is ambiguous: %d matches, use the ID", ref, len(all))
	}
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
	all, err := listTLDs(ctx, client, name, limit, common.headers())
	if err != nil {
		return fmt.Errorf("listing TLDs: %w", err)
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
	headers := common.headers()
	id, err := resolveTLDID(ctx, client, ref, headers)
	if err != nil {
		return err
	}
	var item tld
	if err := dnsGetJSON(ctx, client, client.ServiceURL("tlds", id), headers, &item); err != nil {
		return fmt.Errorf("showing TLD %q: %w", ref, err)
	}
	fields, values := tldFields(&item)
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
	fl.StringVar(&description, "description", "", "description")
	common.bind(cmd)
	return cmd
}

func runTLDCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	name, description string, common *commonOptions, w io.Writer,
) error {
	body := map[string]any{"name": name}
	if description != "" {
		body["description"] = description
	}
	var item tld
	if err := dnsPostJSON(ctx, client, client.ServiceURL("tlds"), body, &item, common.headers()); err != nil {
		return fmt.Errorf("creating TLD %q: %w", name, err)
	}
	fields, values := tldFields(&item)
	return o.WriteSingle(w, fields, values)
}

func newTLDSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var name, description string
	var noDescription bool
	common := &commonOptions{}
	cmd := &cobra.Command{
		Use:   "set <tld>",
		Short: "Update a TLD",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			fl := cmd.Flags()
			if !fl.Changed("name") && !fl.Changed("description") && !noDescription {
				return fmt.Errorf("nothing to set: pass --name, --description or --no-description")
			}
			ctx := cmd.Context()
			client, err := newDNSClient(ctx, a)
			if err != nil {
				return err
			}
			return runTLDSet(ctx, client, o, args[0], name, description, noDescription,
				common, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&name, "name", "", "new TLD name")
	fl.StringVar(&description, "description", "", "new description")
	fl.BoolVar(&noDescription, "no-description", false, "clear the description")
	cmd.MarkFlagsMutuallyExclusive("description", "no-description")
	common.bind(cmd)
	return cmd
}

func runTLDSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	ref, name, description string, noDescription bool, common *commonOptions, w io.Writer,
) error {
	headers := common.headers()
	id, err := resolveTLDID(ctx, client, ref, headers)
	if err != nil {
		return err
	}
	body := make(map[string]any, 2)
	if name != "" {
		body["name"] = name
	}
	switch {
	case noDescription:
		body["description"] = nil
	case description != "":
		body["description"] = description
	}
	var item tld
	if err := dnsPatchJSON(ctx, client, client.ServiceURL("tlds", id), body, &item, headers); err != nil {
		return fmt.Errorf("updating TLD %q: %w", ref, err)
	}
	fields, values := tldFields(&item)
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
	headers := common.headers()
	for _, ref := range refs {
		id, err := resolveTLDID(ctx, client, ref, headers)
		if err != nil {
			return err
		}
		if err := dnsDelete(ctx, client, client.ServiceURL("tlds", id), headers); err != nil {
			return fmt.Errorf("deleting TLD %q: %w", ref, err)
		}
		if _, err := fmt.Fprintf(w, "Deleted TLD %s\n", ref); err != nil {
			return err
		}
	}
	return nil
}

package server

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servergroups"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/paging"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// "server group" — nova's scheduler affinity groups, a top-level noun upstream
// rather than a subcommand of "server".
//
// Flag names follow upstream OSC (`openstack server group ...`). UNVERIFIED
// against KeyStack docs (https://docs.keystack.ru/ returned HTTP 403 at
// implementation time); falls back to upstream OSC semantics.

// serverGroupPolicyMicroversion is where nova replaced the `policies` array
// with a single `policy` string plus `rules`
// (nova/api/openstack/compute/schemas/server_groups.py, create_v264). The
// schema sets additionalProperties=false on both sides, so sending the wrong
// one is a 400 rather than a field nova ignores — which form goes on the wire
// has to follow the negotiated microversion.
const serverGroupPolicyMicroversion = "2.64"

// newServerGroupCommand builds "server group ...", attached to the existing
// "server" noun by newServerCommand.
func newServerGroupCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "group", Short: "Manage server (scheduler affinity) groups"}
	cmd.AddCommand(
		newServerGroupListCommand(a, o),
		newServerGroupShowCommand(a, o),
		newServerGroupCreateCommand(a, o),
		newServerGroupDeleteCommand(a, o),
	)
	return cmd
}

// --- list -------------------------------------------------------------------

func newServerGroupListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var allProjects, long bool
	var limit int
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List server groups",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newComputeClient(ctx, a)
			if err != nil {
				return err
			}
			return runServerGroupList(ctx, client, o, allProjects, long, limit, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&allProjects, "all-projects", false, "list groups from all projects (admin only)")
	fl.BoolVar(&long, "long", false, "list additional fields in output")
	fl.IntVar(&limit, "limit", 0, "maximum number of server groups to return")
	return cmd
}

func runServerGroupList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	allProjects, long bool, limit int, w io.Writer,
) error {
	opts := servergroups.ListOpts{AllProjects: allProjects, Limit: limit}
	all, err := paging.Collect(ctx, servergroups.List(client, opts), limit, servergroups.ExtractServerGroups)
	if err != nil {
		return fmt.Errorf("listing server groups: %w", err)
	}
	cols := []string{"ID", "Name", "Policy"}
	if long {
		cols = append(cols, "Members", "Project ID", "User ID")
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(all))}
	for _, g := range all {
		row := []any{g.ID, g.Name, serverGroupPolicy(g)}
		if long {
			row = append(row, g.Members, g.ProjectID, g.UserID)
		}
		t.Rows = append(t.Rows, row)
	}
	return o.WriteList(w, t)
}

// serverGroupPolicy renders whichever policy representation the cloud returned.
// From 2.64 nova reports a single `policy`; before that, a one-element
// `policies` array. Reading both means the column is never blank just because
// the cloud is on the other side of that line.
func serverGroupPolicy(g servergroups.ServerGroup) string {
	if g.Policy != nil && *g.Policy != "" {
		return *g.Policy
	}
	return strings.Join(g.Policies, ", ")
}

// --- show -------------------------------------------------------------------

func newServerGroupShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <server-group>",
		Short: "Show server group details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newComputeClient(ctx, a)
			if err != nil {
				return err
			}
			return runServerGroupShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runServerGroupShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, w io.Writer) error {
	id, err := resolveServerGroupID(ctx, client, ref)
	if err != nil {
		return err
	}
	g, err := servergroups.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("showing server group %q: %w", ref, err)
	}
	return writeServerGroup(o, w, g)
}

func writeServerGroup(o *output.Options, w io.Writer, g *servergroups.ServerGroup) error {
	var maxPerHost any = ""
	if g.Rules != nil {
		maxPerHost = g.Rules.MaxServerPerHost
	}
	return o.WriteSingle(w,
		[]string{"id", "name", "policy", "rules_max_server_per_host", "members", "project_id", "user_id"},
		[]any{g.ID, g.Name, serverGroupPolicy(*g), maxPerHost, g.Members, g.ProjectID, g.UserID})
}

// resolveServerGroupID passes a UUID through and otherwise resolves a name via
// the listing, erroring on an ambiguous name rather than picking one.
func resolveServerGroupID(ctx context.Context, client *gophercloud.ServiceClient, ref string) (string, error) {
	if isUUID(ref) {
		return ref, nil
	}
	pages, err := servergroups.List(client, servergroups.ListOpts{AllProjects: true}).AllPages(ctx)
	if err != nil {
		return "", fmt.Errorf("looking up server group %q: %w", ref, err)
	}
	all, err := servergroups.ExtractServerGroups(pages)
	if err != nil {
		return "", fmt.Errorf("parsing the server group list: %w", err)
	}
	var matches []string
	for _, g := range all {
		if g.Name == ref {
			matches = append(matches, g.ID)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		// Consistent with the other resolvers: pass the literal through and let
		// the API produce the not-found error.
		return ref, nil
	default:
		return "", fmt.Errorf("server group %q is ambiguous: %d groups share that name; use an ID", ref, len(matches))
	}
}

// --- create -----------------------------------------------------------------

func newServerGroupCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var policy string
	var rules []string
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new server group",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newComputeClient(ctx, a)
			if err != nil {
				return err
			}
			return runServerGroupCreate(ctx, client, o, args[0], policy, rules, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&policy, "policy", "affinity",
		"scheduler policy: affinity, anti-affinity, soft-affinity or soft-anti-affinity")
	fl.StringArrayVar(&rules, "rule", nil,
		"policy rule as key=value, e.g. max_server_per_host=2 (anti-affinity only; nova 2.64 or later)")
	return cmd
}

func runServerGroupCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	name, policy string, rules []string, w io.Writer,
) error {
	opts := servergroups.CreateOpts{Name: name}
	parsedRules, err := parseServerGroupRules(rules)
	if err != nil {
		return err
	}
	if computeSupportsMicroversion(client, serverGroupPolicyMicroversion) {
		opts.Policy = policy
		opts.Rules = parsedRules
	} else {
		if parsedRules != nil {
			return fmt.Errorf("--rule requires nova microversion %s or later; this client is pinned to %s",
				serverGroupPolicyMicroversion, client.Microversion)
		}
		// Below 2.64 nova's schema knows only the one-element `policies` array
		// and rejects `policy` outright.
		opts.Policies = []string{policy}
	}
	g, err := servergroups.Create(ctx, client, opts).Extract()
	if err != nil {
		return fmt.Errorf("creating server group %q: %w", name, err)
	}
	return writeServerGroup(o, w, g)
}

func parseServerGroupRules(rules []string) (*servergroups.Rules, error) {
	if len(rules) == 0 {
		return nil, nil
	}
	out := &servergroups.Rules{}
	for _, r := range rules {
		k, v, ok := strings.Cut(r, "=")
		if !ok {
			return nil, fmt.Errorf("expected key=value, got %q", r)
		}
		switch strings.TrimSpace(k) {
		case "max_server_per_host":
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				return nil, fmt.Errorf("--rule max_server_per_host: %w", err)
			}
			out.MaxServerPerHost = n
		default:
			return nil, fmt.Errorf("unknown --rule key %q; nova defines only max_server_per_host", k)
		}
	}
	return out, nil
}

// --- delete -----------------------------------------------------------------

func newServerGroupDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <server-group> [<server-group> ...]",
		Short: "Delete server group(s)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newComputeClient(ctx, a)
			if err != nil {
				return err
			}
			return runServerGroupDelete(ctx, client, args)
		},
	}
}

func runServerGroupDelete(ctx context.Context, client *gophercloud.ServiceClient, refs []string) error {
	for _, ref := range refs {
		id, err := resolveServerGroupID(ctx, client, ref)
		if err != nil {
			return err
		}
		if err := servergroups.Delete(ctx, client, id).ExtractErr(); err != nil {
			return fmt.Errorf("deleting server group %q: %w", ref, err)
		}
	}
	return nil
}

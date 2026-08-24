package loadbalancer

import (
	"context"
	"fmt"
	"io"
	neturl "net/url"
	"sort"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/loadbalancer/v2/amphorae"
	"github.com/gophercloud/gophercloud/v2/openstack/loadbalancer/v2/flavorprofiles"
	"github.com/gophercloud/gophercloud/v2/openstack/loadbalancer/v2/flavors"
	"github.com/gophercloud/gophercloud/v2/openstack/loadbalancer/v2/providers"
	"github.com/gophercloud/gophercloud/v2/openstack/loadbalancer/v2/quotas"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
	"github.com/ftarasenko/go-openstackclient/internal/cli/nameflag"
	"github.com/ftarasenko/go-openstackclient/internal/cli/resolve"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// This file holds the octavia admin tail: quotas, amphorae, providers and
// flavors. Four operations here have no typed gophercloud call at v2.13.0 and use
// the raw-ServiceClient fallback per AGENTS.md, each isolated in its own helper
// with a comment naming the endpoint:
//
//   - quota defaults show      GET /v2.0/quotas/defaults
//   - amphora configure        PUT /v2.0/octavia/amphorae/<id>/config
//   - amphora delete        DELETE /v2.0/octavia/amphorae/<id>
//   - amphora stats show       GET /v2.0/octavia/amphorae/<id>/stats
//   - provider capability list GET /v2.0/lbaas/providers/<name>/capabilities

// --- quota -----------------------------------------------------------------

func newLBQuotaCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "quota",
		Short: "Manage per-project load balancer quotas",
	}
	cmd.AddCommand(
		newLBQuotaListCommand(a, o),
		newLBQuotaShowCommand(a, o),
		newLBQuotaSetCommand(a, o),
		newLBQuotaUnsetCommand(a, o),
		newLBQuotaResetCommand(a, o),
		newLBQuotaDefaultsCommand(a, o),
	)
	return cmd
}

// lbQuotaNames are octavia's seven quota keys, in the order upstream's
// `loadbalancer quota unset` registers its flags.
var lbQuotaNames = []string{"loadbalancer", "listener", "pool", "member", "healthmonitor", "l7policy", "l7rule"}

func lbQuotaFields(q *quotas.Quota) ([]string, []any) {
	return []string{"loadbalancer", "listener", "pool", "member", "healthmonitor", "l7policy", "l7rule"},
		[]any{q.Loadbalancer, q.Listener, q.Pool, q.Member, q.Healthmonitor, q.L7Policy, q.L7Rule}
}

// resolveQuotaProject resolves the project reference a quota command targets,
// defaulting to the invocation's own project when none is given.
func resolveQuotaProject(ctx context.Context, session *auth.Client, a *auth.Options, args []string, domainRef string) (string, error) {
	var ref string
	switch {
	case len(args) == 1:
		ref = args[0]
	case a.ProjectID != "":
		ref = a.ProjectID
	default:
		ref = a.ProjectName
	}
	if ref == "" {
		return "", fmt.Errorf("no project given: pass a project name/ID or set OS_PROJECT_ID/OS_PROJECT_NAME")
	}
	if resolve.IsUUID(ref) {
		return ref, nil
	}
	identity, err := session.Identity()
	if err != nil {
		return "", err
	}
	return resolve.ProjectIDInDomain(ctx, identity, ref, domainRef)
}

// lbProjectQuota is one row of GET /v2.0/quotas: a project's quotas plus the
// project they belong to.
//
// The fields are pointers and carry both spellings octavia has used, because
// this cannot reuse quotas.Quota: that type defines UnmarshalJSON to reconcile
// the legacy `load_balancer`/`health_monitor` names, and embedding it would
// promote that method onto this struct and swallow project_id.
type lbProjectQuota struct {
	ProjectID string `json:"project_id"`

	Loadbalancer *int `json:"loadbalancer"`
	LoadBalancer *int `json:"load_balancer"` // legacy spelling

	Listener      *int `json:"listener"`
	Pool          *int `json:"pool"`
	Member        *int `json:"member"`
	Healthmonitor *int `json:"healthmonitor"`
	HealthMonitor *int `json:"health_monitor"` // legacy spelling
	L7Policy      *int `json:"l7policy"`
	L7Rule        *int `json:"l7rule"`
}

// quotaValue renders one quota cell: octavia's -1 means unlimited, and a key the
// deployment omits renders empty rather than as a misleading 0.
func quotaValue(primary, legacy *int) any {
	v := primary
	if v == nil {
		v = legacy
	}
	if v == nil {
		return nil
	}
	return *v
}

// newLBQuotaListCommand builds "loadbalancer quota list", upstream
// octaviaclient's ListQuota.
func newLBQuotaListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var projectRef, projectDomain string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List per-project load balancer quotas",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, session, err := newLoadBalancerSession(ctx, a)
			if err != nil {
				return err
			}
			project := ""
			if projectRef != "" {
				// resolveQuotaProject's default-to-own-project fallback is wrong
				// here: no --project means "every project", not "mine".
				if project, err = resolveQuotaProject(ctx, session, a, []string{projectRef}, projectDomain); err != nil {
					return err
				}
			}
			return runLBQuotaList(ctx, client, o, project, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&projectRef, "project", "", "list only this project's quotas (name or ID)")
	cmd.Flags().StringVar(&projectDomain, flagProjectDomain, "", helpProjectDomain)
	return cmd
}

// runLBQuotaList reads GET /v2.0/quotas, for which gophercloud has no typed
// call — its quotas package covers only the per-project Get/Update/Delete. Raw
// fallback per AGENTS.md, on the same prefix the typed calls use (see the note
// on runLBQuotaDefaultsShow). Octavia paginates with `quotas_links`, so the next
// links are followed to completion.
func runLBQuotaList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	project string, w io.Writer,
) error {
	url := client.ServiceURL("quotas")
	if project != "" {
		url += "?project_id=" + neturl.QueryEscape(project)
	}
	var all []lbProjectQuota
	seen := map[string]bool{}
	for url != "" {
		var page struct {
			Quotas []lbProjectQuota `json:"quotas"`
			Links  []struct {
				Rel  string `json:"rel"`
				HRef string `json:"href"`
			} `json:"quotas_links"`
		}
		resp, err := client.Get(ctx, url, &page, nil)
		if resp != nil {
			_ = resp.Body.Close()
		}
		if _, _, err = gophercloud.ParseResponse(resp, err); err != nil {
			return fmt.Errorf("listing load balancer quotas: %w", err)
		}
		all = append(all, page.Quotas...)
		seen[url] = true
		url = ""
		for _, l := range page.Links {
			// Guard against a server that echoes the current page as "next".
			if l.Rel == "next" && l.HRef != "" && !seen[l.HRef] {
				url = l.HRef
			}
		}
	}

	t := output.Table{
		Columns: []string{"Project ID", "Load Balancer", "Listener", "Pool", "Member", "Health Monitor", "L7Policy", "L7Rule"},
		Rows:    make([][]any, 0, len(all)),
	}
	for i := range all {
		q := &all[i]
		t.Rows = append(t.Rows, []any{
			q.ProjectID,
			quotaValue(q.Loadbalancer, q.LoadBalancer),
			quotaValue(q.Listener, nil),
			quotaValue(q.Pool, nil),
			quotaValue(q.Member, nil),
			quotaValue(q.Healthmonitor, q.HealthMonitor),
			quotaValue(q.L7Policy, nil),
			quotaValue(q.L7Rule, nil),
		})
	}
	return o.WriteList(w, t)
}

func newLBQuotaShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var projectDomain string
	cmd := &cobra.Command{
		Use:   "show [<project>]",
		Short: "Show a project's load balancer quotas",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, session, err := newLoadBalancerSession(ctx, a)
			if err != nil {
				return err
			}
			project, err := resolveQuotaProject(ctx, session, a, args, projectDomain)
			if err != nil {
				return err
			}
			return runLBQuotaShow(ctx, client, o, project, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&projectDomain, flagProjectDomain, "", helpProjectDomain)
	return cmd
}

func runLBQuotaShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, project string, w io.Writer) error {
	q, err := quotas.Get(ctx, client, project).Extract()
	if err != nil {
		return fmt.Errorf("showing load balancer quotas for project %q: %w", project, err)
	}
	fields, values := lbQuotaFields(q)
	return o.WriteSingle(w, fields, values)
}

type lbQuotaSetFlags struct {
	loadBalancer  int
	listener      int
	pool          int
	member        int
	healthMonitor int
	l7Policy      int
	l7Rule        int
	projectDomain string
}

// quotaFlagNames maps each quota flag to the UpdateOpts field it fills.
func (f *lbQuotaSetFlags) bindings(opts *quotas.UpdateOpts) map[string]func() {
	return map[string]func(){
		"loadbalancer":  func() { opts.Loadbalancer = &f.loadBalancer },
		"listener":      func() { opts.Listener = &f.listener },
		"pool":          func() { opts.Pool = &f.pool },
		"member":        func() { opts.Member = &f.member },
		"healthmonitor": func() { opts.Healthmonitor = &f.healthMonitor },
		"l7policy":      func() { opts.L7Policy = &f.l7Policy },
		"l7rule":        func() { opts.L7Rule = &f.l7Rule },
	}
}

func newLBQuotaSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &lbQuotaSetFlags{}
	cmd := &cobra.Command{
		Use:   "set [<project>]",
		Short: "Set a project's load balancer quotas",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			changed := changedFlags(cmd.Flags())
			ctx := cmd.Context()
			client, session, err := newLoadBalancerSession(ctx, a)
			if err != nil {
				return err
			}
			project, err := resolveQuotaProject(ctx, session, a, args, f.projectDomain)
			if err != nil {
				return err
			}
			return runLBQuotaSet(ctx, client, o, project, f, changed, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	// -1 means unlimited in octavia, so a negative value is legitimate and the
	// "was the flag given" check is what decides whether to send each key.
	fl.IntVar(&f.loadBalancer, "loadbalancer", 0, "maximum load balancers; -1 for unlimited")
	fl.IntVar(&f.listener, "listener", 0, "maximum listeners; -1 for unlimited")
	fl.IntVar(&f.pool, "pool", 0, "maximum pools; -1 for unlimited")
	fl.IntVar(&f.member, "member", 0, "maximum members; -1 for unlimited")
	fl.IntVar(&f.healthMonitor, "healthmonitor", 0, "maximum health monitors; -1 for unlimited")
	fl.IntVar(&f.l7Policy, "l7policy", 0, "maximum layer-7 policies; -1 for unlimited")
	fl.IntVar(&f.l7Rule, "l7rule", 0, "maximum layer-7 rules; -1 for unlimited")
	fl.StringVar(&f.projectDomain, flagProjectDomain, "", helpProjectDomain)
	return cmd
}

func runLBQuotaSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	project string, f *lbQuotaSetFlags, changed changedSet, w io.Writer,
) error {
	var opts quotas.UpdateOpts
	touched := false
	// Applied in a fixed order so the request is deterministic.
	bindings := f.bindings(&opts)
	names := make([]string, 0, len(bindings))
	for name := range bindings {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if changed[name] {
			bindings[name]()
			touched = true
		}
	}
	if !touched {
		return fmt.Errorf("nothing to set: pass at least one quota flag")
	}
	q, err := quotas.Update(ctx, client, project, opts).Extract()
	if err != nil {
		return fmt.Errorf("setting load balancer quotas for project %q: %w", project, err)
	}
	fields, values := lbQuotaFields(q)
	return o.WriteSingle(w, fields, values)
}

// newLBQuotaUnsetCommand builds "loadbalancer quota unset", which clears ONLY
// the quotas named by its boolean flags — mirroring upstream
// python-octaviaclient's UnsetQuota (osc/v2/quota.py). Clearing every quota at
// once is a separate verb upstream, `quota reset`; koc used to spell that
// "unset", which silently reverted quotas the operator never named.
func newLBQuotaUnsetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var projectDomain string
	clearFlags := make(map[string]*bool, len(lbQuotaNames))
	cmd := &cobra.Command{
		Use:   "unset [<project>]",
		Short: "Clear the named load balancer quotas, reverting them to the defaults",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			var names []string
			for _, n := range lbQuotaNames {
				if *clearFlags[n] {
					names = append(names, n)
				}
			}
			if len(names) == 0 {
				return fmt.Errorf("nothing to unset: pass at least one quota flag " +
					"(use \"loadbalancer quota reset\" to clear them all)")
			}
			ctx := cmd.Context()
			client, session, err := newLoadBalancerSession(ctx, a)
			if err != nil {
				return err
			}
			project, err := resolveQuotaProject(ctx, session, a, args, projectDomain)
			if err != nil {
				return err
			}
			return runLBQuotaUnset(ctx, client, o, project, names, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	for _, n := range lbQuotaNames {
		clearFlags[n] = fl.Bool(n, false, "clear the "+n+" quota")
	}
	fl.StringVar(&projectDomain, flagProjectDomain, "", helpProjectDomain)
	return cmd
}

// runLBQuotaUnset PUTs an explicit JSON null for each named quota, which is how
// octavia is told "revert this one key to the deployment default". The typed
// quotas.UpdateOpts cannot express it — its fields are `*int` with
// `omitempty`, so a nil pointer is omitted rather than serialised as null — so
// this is the raw fallback per AGENTS.md, against the same
// PUT /v2.0/quotas/<project> the typed Update uses.
func runLBQuotaUnset(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	project string, names []string, w io.Writer,
) error {
	quota := make(map[string]any, len(names))
	for _, n := range names {
		quota[n] = nil
	}
	reqBody := map[string]any{"quota": quota}
	var respBody struct {
		Quota quotas.Quota `json:"quota"`
	}
	resp, err := client.Put(ctx, client.ServiceURL("quotas", project), reqBody, &respBody,
		&gophercloud.RequestOpts{OkCodes: []int{200, 202}})
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if _, _, err = gophercloud.ParseResponse(resp, err); err != nil {
		return fmt.Errorf("unsetting load balancer quotas %v for project %q: %w", names, project, err)
	}
	fields, values := lbQuotaFields(&respBody.Quota)
	return o.WriteSingle(w, fields, values)
}

// newLBQuotaResetCommand builds "loadbalancer quota reset": clear every quota
// for the project, no flags. This is upstream octaviaclient's ResetQuota, and
// the behaviour koc previously exposed under the `unset` name.
func newLBQuotaResetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var projectDomain string
	cmd := &cobra.Command{
		Use:   "reset [<project>]",
		Short: "Reset all of a project's load balancer quotas to the defaults",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, session, err := newLoadBalancerSession(ctx, a)
			if err != nil {
				return err
			}
			project, err := resolveQuotaProject(ctx, session, a, args, projectDomain)
			if err != nil {
				return err
			}
			return runLBQuotaReset(ctx, client, project, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&projectDomain, flagProjectDomain, "", helpProjectDomain)
	return cmd
}

func runLBQuotaReset(ctx context.Context, client *gophercloud.ServiceClient, project string, w io.Writer) error {
	if err := quotas.Delete(ctx, client, project).ExtractErr(); err != nil {
		return fmt.Errorf("resetting load balancer quotas for project %q: %w", project, err)
	}
	if _, err := fmt.Fprintf(w, "Reset load balancer quotas for project %s to the defaults\n", project); err != nil {
		return err
	}
	return nil
}

func newLBQuotaDefaultsCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "defaults", Short: "Deployment-wide default load balancer quotas"}
	cmd.AddCommand(&cobra.Command{
		Use:   "show",
		Short: "Show the deployment's default load balancer quotas",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newLoadBalancerClient(ctx, a)
			if err != nil {
				return err
			}
			return runLBQuotaDefaultsShow(ctx, client, o, cmd.OutOrStdout())
		},
	})
	return cmd
}

// runLBQuotaDefaultsShow reads GET /v2.0/quotas/defaults, for which gophercloud
// has no typed call — the quotas package covers only the per-project
// Get/Update/Delete. Raw fallback per AGENTS.md; replace it if a typed call lands.
//
// The path deliberately omits the "lbaas" segment even though octavia's API
// reference documents /v2.0/lbaas/quotas: octavia serves its resources at both
// prefixes, and gophercloud's typed quota calls use the shorter one. Matching
// them keeps all four quota verbs on the same prefix, so they cannot disagree
// about which one a given deployment answers.
func runLBQuotaDefaultsShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, w io.Writer) error {
	var body struct {
		Quota quotas.Quota `json:"quota"`
	}
	resp, err := client.Get(ctx, client.ServiceURL("quotas", "defaults"), &body, nil)
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if _, _, err = gophercloud.ParseResponse(resp, err); err != nil {
		return fmt.Errorf("showing default load balancer quotas: %w", err)
	}
	fields, values := lbQuotaFields(&body.Quota)
	return o.WriteSingle(w, fields, values)
}

// --- amphora ---------------------------------------------------------------

func newAmphoraCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "amphora",
		Short: "Inspect and manage amphorae (the VMs behind amphora-driver load balancers)",
	}
	cmd.AddCommand(
		newAmphoraListCommand(a, o),
		newAmphoraShowCommand(a, o),
		newAmphoraFailoverCommand(a, o),
		newAmphoraConfigureCommand(a, o),
		newAmphoraDeleteCommand(a, o),
		newAmphoraStatsCommand(a, o),
	)
	return cmd
}

func amphoraFields(am *amphorae.Amphora) ([]string, []any) {
	fields := []string{
		"id", "loadbalancer_id", "compute_id", "role", "status", "lb_network_ip",
		"ha_ip", "ha_port_id", "vrrp_ip", "vrrp_port_id", "vrrp_interface",
		"vrrp_id", "vrrp_priority", "cached_zone", "image_id", "cert_busy",
		"cert_expiration", "created_at", "updated_at",
	}
	values := []any{
		am.ID, am.LoadbalancerID, am.ComputeID, am.Role, am.Status, am.LBNetworkIP,
		am.HAIP, am.HAPortID, am.VRRPIP, am.VRRPPortID, am.VRRPInterface,
		am.VRRPID, am.VRRPPriority, am.CachedZone, am.ImageID, am.CertBusy,
		am.CertExpiration, am.CreatedAt, am.UpdatedAt,
	}
	return fields, values
}

type amphoraListFlags struct {
	loadBalancer string
	role         string
	status       string
	long         bool
}

func newAmphoraListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &amphoraListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List amphorae",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newLoadBalancerClient(ctx, a)
			if err != nil {
				return err
			}
			return runAmphoraList(ctx, client, o, f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.loadBalancer, "loadbalancer", "", "list only amphorae of this load balancer (name or ID)")
	fl.StringVar(&f.role, "role", "", "filter by role: MASTER, BACKUP or STANDALONE")
	fl.StringVar(&f.status, "status", "", "filter by status, e.g. ALLOCATED, BOOTING, READY or ERROR")
	fl.BoolVar(&f.long, "long", false, "list additional fields in output")
	return cmd
}

func runAmphoraList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	f *amphoraListFlags, w io.Writer,
) error {
	opts := amphorae.ListOpts{Role: f.role, Status: f.status}
	if f.loadBalancer != "" {
		lbID, err := resolveLoadBalancerID(ctx, client, f.loadBalancer)
		if err != nil {
			return err
		}
		opts.LoadbalancerID = lbID
	}
	pages, err := amphorae.List(client, opts).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing amphorae: %w", err)
	}
	all, err := amphorae.ExtractAmphorae(pages)
	if err != nil {
		return fmt.Errorf("parsing amphora list: %w", err)
	}

	cols := []string{"ID", "Load Balancer ID", "Role", "Status", "LB Network IP", "HA IP"}
	if f.long {
		cols = append(cols, "Compute ID", "Image ID", "Cached Zone", "VRRP IP", "Cert Expiration")
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(all))}
	for i := range all {
		am := &all[i]
		row := []any{am.ID, am.LoadbalancerID, am.Role, am.Status, am.LBNetworkIP, am.HAIP}
		if f.long {
			row = append(row, am.ComputeID, am.ImageID, am.CachedZone, am.VRRPIP, am.CertExpiration)
		}
		t.Rows = append(t.Rows, row)
	}
	return o.WriteList(w, t)
}

func newAmphoraShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <amphora-id>",
		Short: "Show amphora details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newLoadBalancerClient(ctx, a)
			if err != nil {
				return err
			}
			return runAmphoraShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

// Amphorae have no name, so every verb takes the ID: there is nothing to resolve.
func runAmphoraShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, id string, w io.Writer) error {
	am, err := amphorae.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("showing amphora %s: %w", id, err)
	}
	fields, values := amphoraFields(am)
	return o.WriteSingle(w, fields, values)
}

func newAmphoraFailoverCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "failover <amphora-id>",
		Short: "Fail over a single amphora",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newLoadBalancerClient(ctx, a)
			if err != nil {
				return err
			}
			return runAmphoraFailover(ctx, client, args[0], cmd.OutOrStdout())
		},
	}
}

func runAmphoraFailover(ctx context.Context, client *gophercloud.ServiceClient, id string, w io.Writer) error {
	if err := amphorae.Failover(ctx, client, id).ExtractErr(); err != nil {
		return fmt.Errorf("failing over amphora %s: %w", id, err)
	}
	if _, err := fmt.Fprintf(w, "Requested failover of amphora %s\n", id); err != nil {
		return err
	}
	return nil
}

func newAmphoraConfigureCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "configure <amphora-id>",
		Short: "Push the current configuration to an amphora's agent",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newLoadBalancerClient(ctx, a)
			if err != nil {
				return err
			}
			return runAmphoraConfigure(ctx, client, args[0], cmd.OutOrStdout())
		},
	}
}

// runAmphoraConfigure PUTs /v2.0/octavia/amphorae/<id>/config. gophercloud's
// amphorae package covers only List/Get/Failover, so this is a raw fallback per
// AGENTS.md. Note the /octavia/ path segment: the amphora admin endpoints sit
// there rather than under /lbaas/.
func runAmphoraConfigure(ctx context.Context, client *gophercloud.ServiceClient, id string, w io.Writer) error {
	url := client.ServiceURL("octavia", "amphorae", id, "config")
	resp, err := client.Put(ctx, url, nil, nil, &gophercloud.RequestOpts{OkCodes: []int{202}})
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if _, _, err = gophercloud.ParseResponse(resp, err); err != nil {
		return fmt.Errorf("configuring amphora %s: %w", id, err)
	}
	if _, err := fmt.Fprintf(w, "Requested configuration update of amphora %s\n", id); err != nil {
		return err
	}
	return nil
}

func newAmphoraDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <amphora-id> [<amphora-id>...]",
		Short: "Delete one or more amphorae",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newLoadBalancerClient(ctx, a)
			if err != nil {
				return err
			}
			return runAmphoraDelete(ctx, client, args, cmd.OutOrStdout())
		},
	}
}

// runAmphoraDelete DELETEs /v2.0/octavia/amphorae/<id> — again no typed call.
func runAmphoraDelete(ctx context.Context, client *gophercloud.ServiceClient, ids []string, w io.Writer) error {
	return batchdelete.Each(ids, func(id string) error {
		if err := deleteAmphoraRaw(ctx, client, id); err != nil {
			return fmt.Errorf("deleting amphora %s: %w", id, err)
		}
		if _, werr := fmt.Fprintf(w, "Requested deletion of amphora %s\n", id); werr != nil {
			return werr
		}
		return nil
	})
}

func deleteAmphoraRaw(ctx context.Context, client *gophercloud.ServiceClient, id string) error {
	resp, err := client.Delete(ctx, client.ServiceURL("octavia", "amphorae", id), &gophercloud.RequestOpts{
		OkCodes: []int{204},
	})
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	_, _, err = gophercloud.ParseResponse(resp, err)
	return err
}

func newAmphoraStatsCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "stats", Short: "Amphora traffic statistics"}
	cmd.AddCommand(&cobra.Command{
		Use:   "show <amphora-id>",
		Short: "Show an amphora's per-listener traffic statistics",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newLoadBalancerClient(ctx, a)
			if err != nil {
				return err
			}
			return runAmphoraStatsShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	})
	return cmd
}

// amphoraListenerStats is koc's DTO for one row of GET
// /v2.0/octavia/amphorae/<id>/stats, which gophercloud has no typed call for.
// Unlike the load balancer's own stats this endpoint returns one entry per
// listener, so it renders as a list rather than a single record.
type amphoraListenerStats struct {
	ListenerID        string `json:"listener_id"`
	LoadbalancerID    string `json:"loadbalancer_id"`
	ID                string `json:"id"`
	ActiveConnections int    `json:"active_connections"`
	BytesIn           int    `json:"bytes_in"`
	BytesOut          int    `json:"bytes_out"`
	RequestErrors     int    `json:"request_errors"`
	TotalConnections  int    `json:"total_connections"`
}

func runAmphoraStatsShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, id string, w io.Writer) error {
	var body struct {
		Stats []amphoraListenerStats `json:"amphora_stats"`
	}
	url := client.ServiceURL("octavia", "amphorae", id, "stats")
	resp, err := client.Get(ctx, url, &body, nil)
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if _, _, err = gophercloud.ParseResponse(resp, err); err != nil {
		return fmt.Errorf("getting statistics for amphora %s: %w", id, err)
	}
	t := output.Table{
		Columns: []string{"Listener ID", "Load Balancer ID", "Active Connections", "Bytes In", "Bytes Out", "Request Errors", "Total Connections"},
		Rows:    make([][]any, 0, len(body.Stats)),
	}
	for _, s := range body.Stats {
		t.Rows = append(t.Rows, []any{
			s.ListenerID, s.LoadbalancerID, s.ActiveConnections,
			s.BytesIn, s.BytesOut, s.RequestErrors, s.TotalConnections,
		})
	}
	return o.WriteList(w, t)
}

// --- provider --------------------------------------------------------------

func newProviderCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "provider",
		Short: "Inspect the load balancer provider drivers this deployment offers",
	}
	cmd.AddCommand(newProviderListCommand(a, o), newProviderCapabilityCommand(a, o))
	return cmd
}

func newProviderListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the enabled provider drivers",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newLoadBalancerClient(ctx, a)
			if err != nil {
				return err
			}
			return runProviderList(ctx, client, o, cmd.OutOrStdout())
		},
	}
}

func runProviderList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, w io.Writer) error {
	pages, err := providers.List(client, providers.ListOpts{}).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing load balancer providers: %w", err)
	}
	all, err := providers.ExtractProviders(pages)
	if err != nil {
		return fmt.Errorf("parsing provider list: %w", err)
	}
	t := output.Table{Columns: []string{"Name", "Description"}, Rows: make([][]any, 0, len(all))}
	for _, p := range all {
		t.Rows = append(t.Rows, []any{p.Name, p.Description})
	}
	return o.WriteList(w, t)
}

func newProviderCapabilityCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "capability", Short: "Provider driver capabilities"}
	cmd.AddCommand(&cobra.Command{
		Use:   "list <provider>",
		Short: "List the flavor capabilities a provider driver supports",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newLoadBalancerClient(ctx, a)
			if err != nil {
				return err
			}
			return runProviderCapabilityList(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	})
	return cmd
}

// runProviderCapabilityList reads GET /v2.0/lbaas/providers/<name>/capabilities,
// which gophercloud's providers package (List only) does not cover. Raw fallback.
func runProviderCapabilityList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	provider string, w io.Writer,
) error {
	var body struct {
		Capabilities []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"flavor_capabilities"`
	}
	url := client.ServiceURL("lbaas", "providers", provider, "capabilities")
	resp, err := client.Get(ctx, url, &body, nil)
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if _, _, err = gophercloud.ParseResponse(resp, err); err != nil {
		return fmt.Errorf("listing capabilities of provider %q: %w", provider, err)
	}
	t := output.Table{Columns: []string{"Name", "Description"}, Rows: make([][]any, 0, len(body.Capabilities))}
	for _, c := range body.Capabilities {
		t.Rows = append(t.Rows, []any{c.Name, c.Description})
	}
	return o.WriteList(w, t)
}

// --- flavor / flavorprofile ------------------------------------------------

func newFlavorCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "flavor",
		Short: "Manage load balancer flavors",
	}
	cmd.AddCommand(
		newFlavorListCommand(a, o),
		newFlavorShowCommand(a, o),
		newFlavorCreateCommand(a, o),
		newFlavorSetCommand(a, o),
		newFlavorDeleteCommand(a, o),
	)
	return cmd
}

func resolveFlavorID(ctx context.Context, client *gophercloud.ServiceClient, ref string) (string, error) {
	return resolveByName("flavor", ref, func() ([]string, error) {
		pages, err := flavors.List(client, flavors.ListOpts{Name: ref}).AllPages(ctx)
		if err != nil {
			return nil, err
		}
		all, err := flavors.ExtractFlavors(pages)
		if err != nil {
			return nil, err
		}
		ids := make([]string, 0, len(all))
		for _, fl := range all {
			ids = append(ids, fl.ID)
		}
		return ids, nil
	})
}

func newFlavorListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List load balancer flavors",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newLoadBalancerClient(ctx, a)
			if err != nil {
				return err
			}
			return runFlavorList(ctx, client, o, name, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "filter flavors by name")
	return cmd
}

func runFlavorList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, name string, w io.Writer) error {
	pages, err := flavors.List(client, flavors.ListOpts{Name: name}).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing load balancer flavors: %w", err)
	}
	all, err := flavors.ExtractFlavors(pages)
	if err != nil {
		return fmt.Errorf("parsing flavor list: %w", err)
	}
	t := output.Table{
		Columns: []string{"ID", "Name", "Enabled", "Flavor Profile ID", "Description"},
		Rows:    make([][]any, 0, len(all)),
	}
	for _, fl := range all {
		t.Rows = append(t.Rows, []any{fl.ID, fl.Name, fl.Enabled, fl.FlavorProfileId, fl.Description})
	}
	return o.WriteList(w, t)
}

func newFlavorShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <flavor>",
		Short: "Show load balancer flavor details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newLoadBalancerClient(ctx, a)
			if err != nil {
				return err
			}
			return runFlavorShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runFlavorShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, w io.Writer) error {
	id, err := resolveFlavorID(ctx, client, ref)
	if err != nil {
		return err
	}
	fl, err := flavors.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("showing load balancer flavor %q: %w", ref, err)
	}
	return o.WriteSingle(w,
		[]string{"id", "name", "description", "enabled", "flavor_profile_id"},
		[]any{fl.ID, fl.Name, fl.Description, fl.Enabled, fl.FlavorProfileId})
}

type octaviaFlavorCreateFlags struct {
	name          string
	description   string
	flavorProfile string
	disable       bool
}

func newFlavorCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &octaviaFlavorCreateFlags{}
	cmd := &cobra.Command{
		Use:   "create [<name>]",
		Short: "Create a load balancer flavor",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			name, err := nameflag.Resolve(args, f.name, true)
			if err != nil {
				return err
			}
			if f.flavorProfile == "" {
				return fmt.Errorf("--flavor-profile is required")
			}
			ctx := cmd.Context()
			client, err := newLoadBalancerClient(ctx, a)
			if err != nil {
				return err
			}
			return runFlavorCreate(ctx, client, o, name, f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	// Upstream octavia requires --name here and takes no positional; koc grew the
	// positional first. Both work — see internal/cli/nameflag.
	fl.StringVar(&f.name, "name", "", "name of the flavor (upstream spelling; the positional form also works)")
	fl.StringVar(&f.description, "description", "", "flavor description")
	fl.StringVar(&f.flavorProfile, "flavor-profile", "", "flavor profile to base the flavor on (name or ID, required)")
	fl.BoolVar(&f.disable, "disable", false, "create the flavor disabled, so it cannot be selected yet")
	return cmd
}

func runFlavorCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	name string, f *octaviaFlavorCreateFlags, w io.Writer,
) error {
	profileID, err := resolveFlavorProfileID(ctx, client, f.flavorProfile)
	if err != nil {
		return err
	}
	fl, err := flavors.Create(ctx, client, flavors.CreateOpts{
		Name:            name,
		Description:     f.description,
		FlavorProfileId: profileID,
		Enabled:         !f.disable,
	}).Extract()
	if err != nil {
		return fmt.Errorf("creating load balancer flavor %q: %w", name, err)
	}
	return o.WriteSingle(w,
		[]string{"id", "name", "description", "enabled", "flavor_profile_id"},
		[]any{fl.ID, fl.Name, fl.Description, fl.Enabled, fl.FlavorProfileId})
}

type octaviaFlavorSetFlags struct {
	name        string
	description string
	enable      bool
	disable     bool

	// changed is the set of attribute flags actually given, captured in RunE.
	changed changedSet
}

func newFlavorSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &octaviaFlavorSetFlags{}
	cmd := &cobra.Command{
		Use:   "set <flavor>",
		Short: "Update a load balancer flavor",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			fl := cmd.Flags()
			if fl.Changed("enable") && fl.Changed("disable") {
				return fmt.Errorf("--enable and --disable are mutually exclusive")
			}
			ctx := cmd.Context()
			client, err := newLoadBalancerClient(ctx, a)
			if err != nil {
				return err
			}
			f.changed = changedFlags(fl)
			return runFlavorSet(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "new flavor name")
	fl.StringVar(&f.description, "description", "", "new flavor description")
	fl.BoolVar(&f.enable, "enable", false, "allow the flavor to be selected")
	fl.BoolVar(&f.disable, "disable", false, "stop the flavor from being selected")
	return cmd
}

// runFlavorSet builds a sparse UpdateOpts. flavors.UpdateOpts carries Enabled as
// a plain bool with omitempty, so "disabled" cannot be expressed by that field —
// --disable therefore goes through a raw PUT that sends enabled:false explicitly.
func runFlavorSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	ref string, f *octaviaFlavorSetFlags, w io.Writer,
) error {
	changed := f.changed
	if !changed["name"] && !changed["description"] && !changed["enable"] && !changed["disable"] {
		return fmt.Errorf("nothing to set: pass at least one attribute flag")
	}
	id, err := resolveFlavorID(ctx, client, ref)
	if err != nil {
		return err
	}

	if changed["disable"] {
		if err := setFlavorEnabledRaw(ctx, client, id, false); err != nil {
			return fmt.Errorf("disabling load balancer flavor %q: %w", ref, err)
		}
	}
	opts := flavors.UpdateOpts{}
	touched := false
	if changed["name"] {
		opts.Name = f.name
		touched = true
	}
	if changed["description"] {
		opts.Description = f.description
		touched = true
	}
	if changed["enable"] && f.enable {
		opts.Enabled = true
		touched = true
	}
	var fl *flavors.Flavor
	if touched {
		fl, err = flavors.Update(ctx, client, id, opts).Extract()
		if err != nil {
			return fmt.Errorf("updating load balancer flavor %q: %w", ref, err)
		}
	} else {
		// Only --disable was given, which went through the raw PUT above; re-read so
		// the reported record reflects it.
		fl, err = flavors.Get(ctx, client, id).Extract()
		if err != nil {
			return fmt.Errorf("getting load balancer flavor %q: %w", ref, err)
		}
	}
	return o.WriteSingle(w,
		[]string{"id", "name", "description", "enabled", "flavor_profile_id"},
		[]any{fl.ID, fl.Name, fl.Description, fl.Enabled, fl.FlavorProfileId})
}

// setFlavorEnabledRaw PUTs enabled explicitly. flavors.UpdateOpts tags Enabled
// omitempty, so a false would be dropped from the body and --disable would
// silently do nothing; this sends it. Raw fallback per AGENTS.md — delete it once
// gophercloud makes the field a pointer.
func setFlavorEnabledRaw(ctx context.Context, client *gophercloud.ServiceClient, id string, enabled bool) error {
	body := map[string]any{"flavor": map[string]any{"enabled": enabled}}
	resp, err := client.Put(ctx, client.ServiceURL("lbaas", "flavors", id), body, nil, &gophercloud.RequestOpts{
		OkCodes: []int{200},
	})
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	_, _, err = gophercloud.ParseResponse(resp, err)
	return err
}

func newFlavorDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <flavor> [<flavor>...]",
		Short: "Delete one or more load balancer flavors",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newLoadBalancerClient(ctx, a)
			if err != nil {
				return err
			}
			return runFlavorDelete(ctx, client, args, cmd.OutOrStdout())
		},
	}
}

func runFlavorDelete(ctx context.Context, client *gophercloud.ServiceClient, refs []string, w io.Writer) error {
	return batchdelete.Each(refs, func(ref string) error {
		id, err := resolveFlavorID(ctx, client, ref)
		if err != nil {
			return err
		}
		if derr := flavors.Delete(ctx, client, id).ExtractErr(); derr != nil {
			return fmt.Errorf("deleting load balancer flavor %q: %w", ref, derr)
		}
		if _, werr := fmt.Fprintf(w, "Deleted load balancer flavor %s\n", ref); werr != nil {
			return werr
		}
		return nil
	})
}

// --- flavorprofile ---------------------------------------------------------

func newFlavorProfileCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "flavorprofile",
		Short: "Manage load balancer flavor profiles (provider driver + flavor data)",
	}
	cmd.AddCommand(
		newFlavorProfileListCommand(a, o),
		newFlavorProfileShowCommand(a, o),
		newFlavorProfileCreateCommand(a, o),
		newFlavorProfileSetCommand(a, o),
		newFlavorProfileDeleteCommand(a, o),
	)
	return cmd
}

func resolveFlavorProfileID(ctx context.Context, client *gophercloud.ServiceClient, ref string) (string, error) {
	return resolveByName("flavor profile", ref, func() ([]string, error) {
		pages, err := flavorprofiles.List(client, flavorprofiles.ListOpts{}).AllPages(ctx)
		if err != nil {
			return nil, err
		}
		all, err := flavorprofiles.ExtractFlavorProfiles(pages)
		if err != nil {
			return nil, err
		}
		// flavorprofiles.ListOpts has no name filter, so the match is client-side.
		ids := make([]string, 0, 1)
		for _, p := range all {
			if p.Name == ref {
				ids = append(ids, p.ID)
			}
		}
		return ids, nil
	})
}

func newFlavorProfileListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List flavor profiles",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newLoadBalancerClient(ctx, a)
			if err != nil {
				return err
			}
			return runFlavorProfileList(ctx, client, o, name, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "filter flavor profiles by name")
	return cmd
}

func runFlavorProfileList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, name string, w io.Writer) error {
	pages, err := flavorprofiles.List(client, flavorprofiles.ListOpts{Name: name}).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing flavor profiles: %w", err)
	}
	all, err := flavorprofiles.ExtractFlavorProfiles(pages)
	if err != nil {
		return fmt.Errorf("parsing flavor profile list: %w", err)
	}
	t := output.Table{Columns: []string{"ID", "Name", "Provider Name"}, Rows: make([][]any, 0, len(all))}
	for _, p := range all {
		t.Rows = append(t.Rows, []any{p.ID, p.Name, p.ProviderName})
	}
	return o.WriteList(w, t)
}

func newFlavorProfileShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <flavorprofile>",
		Short: "Show flavor profile details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newLoadBalancerClient(ctx, a)
			if err != nil {
				return err
			}
			return runFlavorProfileShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runFlavorProfileShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, w io.Writer) error {
	id, err := resolveFlavorProfileID(ctx, client, ref)
	if err != nil {
		return err
	}
	p, err := flavorprofiles.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("showing flavor profile %q: %w", ref, err)
	}
	return o.WriteSingle(w,
		[]string{"id", "name", "provider_name", "flavor_data"},
		[]any{p.ID, p.Name, p.ProviderName, p.FlavorData})
}

func newFlavorProfileCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var flagName, providerName, flavorData string
	cmd := &cobra.Command{
		Use:   "create [<name>]",
		Short: "Create a flavor profile",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			name, err := nameflag.Resolve(args, flagName, true)
			if err != nil {
				return err
			}
			if providerName == "" || flavorData == "" {
				return fmt.Errorf("--provider and --flavor-data are required")
			}
			ctx := cmd.Context()
			client, err := newLoadBalancerClient(ctx, a)
			if err != nil {
				return err
			}
			return runFlavorProfileCreate(ctx, client, o, name, providerName, flavorData, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	// As for `loadbalancer flavor create`, upstream spells this --name only.
	fl.StringVar(&flagName, "name", "", "name of the flavor profile (upstream spelling; the positional form also works)")
	fl.StringVar(&providerName, "provider", "", "provider driver the profile targets, e.g. amphora (required)")
	fl.StringVar(&flavorData, flagFlavorData, "", "driver-specific JSON, e.g. '{\"loadbalancer_topology\": \"ACTIVE_STANDBY\"}' (required)")
	return cmd
}

func runFlavorProfileCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	name, providerName, flavorData string, w io.Writer,
) error {
	p, err := flavorprofiles.Create(ctx, client, flavorprofiles.CreateOpts{
		Name:         name,
		ProviderName: providerName,
		FlavorData:   flavorData,
	}).Extract()
	if err != nil {
		return fmt.Errorf("creating flavor profile %q: %w", name, err)
	}
	return o.WriteSingle(w,
		[]string{"id", "name", "provider_name", "flavor_data"},
		[]any{p.ID, p.Name, p.ProviderName, p.FlavorData})
}

type flavorProfileSetFlags struct {
	name         string
	providerName string
	flavorData   string
}

func newFlavorProfileSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &flavorProfileSetFlags{}
	cmd := &cobra.Command{
		Use:   "set <flavorprofile>",
		Short: "Update a flavor profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			fl := cmd.Flags()
			if !fl.Changed("name") && !fl.Changed("provider") && !fl.Changed(flagFlavorData) {
				return fmt.Errorf("nothing to set: pass --name, --provider and/or --flavor-data")
			}
			ctx := cmd.Context()
			client, err := newLoadBalancerClient(ctx, a)
			if err != nil {
				return err
			}
			return runFlavorProfileSet(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "new profile name")
	fl.StringVar(&f.providerName, "provider", "", "new provider driver")
	fl.StringVar(&f.flavorData, flagFlavorData, "", "new driver-specific JSON")
	return cmd
}

func runFlavorProfileSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	ref string, f *flavorProfileSetFlags, w io.Writer,
) error {
	id, err := resolveFlavorProfileID(ctx, client, ref)
	if err != nil {
		return err
	}
	// Every UpdateOpts field is omitempty, so the empty ones are simply not sent.
	p, err := flavorprofiles.Update(ctx, client, id, flavorprofiles.UpdateOpts{
		Name:         f.name,
		ProviderName: f.providerName,
		FlavorData:   f.flavorData,
	}).Extract()
	if err != nil {
		return fmt.Errorf("updating flavor profile %q: %w", ref, err)
	}
	return o.WriteSingle(w,
		[]string{"id", "name", "provider_name", "flavor_data"},
		[]any{p.ID, p.Name, p.ProviderName, p.FlavorData})
}

func newFlavorProfileDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <flavorprofile> [<flavorprofile>...]",
		Short: "Delete one or more flavor profiles",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newLoadBalancerClient(ctx, a)
			if err != nil {
				return err
			}
			return runFlavorProfileDelete(ctx, client, args, cmd.OutOrStdout())
		},
	}
}

func runFlavorProfileDelete(ctx context.Context, client *gophercloud.ServiceClient, refs []string, w io.Writer) error {
	return batchdelete.Each(refs, func(ref string) error {
		id, err := resolveFlavorProfileID(ctx, client, ref)
		if err != nil {
			return err
		}
		if derr := flavorprofiles.Delete(ctx, client, id).ExtractErr(); derr != nil {
			return fmt.Errorf("deleting flavor profile %q: %w", ref, derr)
		}
		if _, werr := fmt.Fprintf(w, "Deleted flavor profile %s\n", ref); werr != nil {
			return werr
		}
		return nil
	})
}

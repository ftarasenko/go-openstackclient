package dns

import (
	"context"
	"fmt"
	"io"
	"sort"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/dns/v2/quotas"
	"github.com/gophercloud/gophercloud/v2/openstack/dns/v2/tsigkeys"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
	"github.com/ftarasenko/go-openstackclient/internal/cli/nameflag"
	"github.com/ftarasenko/go-openstackclient/internal/cli/resolve"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// NewDNSNounCommand builds the top-level "dns" command group — the nouns upstream
// spells with the service prefix (`openstack dns quota …`, `dns service list`),
// as opposed to `zone` and `recordset`, which have no prefix.
//
// Command names follow upstream python-designateclient. The KeyStack command
// reference at https://docs.keystack.ru/ was not reachable at implementation time
// (HTTP 403), so these are UNVERIFIED against KeyStack.
func newDNSNounCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dns",
		Short: "DNS (designate) service-level commands",
	}
	cmd.AddCommand(
		newDNSQuotaCommand(a, o),
		newDNSPoolCommand(a, o),
		newDNSServiceCommand(a, o),
		newDNSLimitCommand(a, o),
	)
	return cmd
}

// --- dns quota -------------------------------------------------------------

func newDNSQuotaCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "quota",
		Short: "Manage per-project DNS quotas",
	}
	cmd.AddCommand(
		newDNSQuotaListCommand(a, o),
		newDNSQuotaSetCommand(a, o),
		newDNSQuotaResetCommand(a, o),
	)
	return cmd
}

func dnsQuotaFields(q *quotas.Quota) ([]string, []any) {
	return []string{"api_export_size", "recordset_records", "zone_records", "zone_recordsets", "zones"},
		[]any{q.APIExporterSize, q.RecordsetRecords, q.ZoneRecords, q.ZoneRecordsets, q.Zones}
}

// resolveDNSQuotaProject resolves the project a quota command targets, defaulting
// to the invocation's own project.
func resolveDNSQuotaProject(ctx context.Context, session *auth.Client, a *auth.Options, args []string, domainRef string) (string, error) {
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

// dnsQuotaTarget names the project a quota verb acts on. Upstream designate
// spells it --project-id; koc grew the positional first, so both work.
type dnsQuotaTarget struct {
	projectID     string
	projectDomain string
}

func (t *dnsQuotaTarget) bind(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringVar(&t.projectID, "project-id", "", "project to act on (upstream's spelling of the positional argument)")
	fl.StringVar(&t.projectDomain, "project-domain", "", "domain owning the project (name or ID)")
}

// resolve returns the target project ID, and enables the all-projects header
// when that project is not the one the session is scoped to.
//
// designate answers 403 to a cross-project GET or PATCH of /v2/quotas unless the
// request carries X-Auth-All-Projects or X-Auth-Sudo-Project-ID, which made the
// project argument a trap: it only ever worked for your own project. Upstream
// python-designateclient's quotas.py calls common.set_all_projects(client, True)
// automatically whenever the requested project differs from the session's, and
// that is what happens here. An explicit --all-projects/--sudo-project-id still
// wins, since headers() already carries whatever the operator asked for.
func (t *dnsQuotaTarget) resolve(ctx context.Context, session *auth.Client, a *auth.Options,
	args []string, common *commonOptions,
) (string, error) {
	if t.projectID != "" {
		if len(args) == 1 && args[0] != t.projectID {
			return "", fmt.Errorf("conflicting projects: %q as an argument and %q as --project-id", args[0], t.projectID)
		}
		args = []string{t.projectID}
	}
	target, err := resolveDNSQuotaProject(ctx, session, a, args, t.projectDomain)
	if err != nil {
		return "", err
	}
	own, err := sessionProjectID(ctx, session, a)
	if err != nil {
		return "", err
	}
	if own != "" && target != own && !common.allProjects && common.sudoProjectID == "" {
		common.allProjects = true
	}
	return target, nil
}

// sessionProjectID resolves the project the invocation is scoped to, without a
// round trip when OS_PROJECT_ID is already an ID. An unscoped session (neither
// set) yields "", which disables the cross-project auto-detection rather than
// guessing.
func sessionProjectID(ctx context.Context, session *auth.Client, a *auth.Options) (string, error) {
	if a.ProjectID != "" {
		return a.ProjectID, nil
	}
	if a.ProjectName == "" {
		return "", nil
	}
	if resolve.IsUUID(a.ProjectName) {
		return a.ProjectName, nil
	}
	identity, err := session.Identity()
	if err != nil {
		return "", err
	}
	return resolve.ProjectIDInDomain(ctx, identity, a.ProjectName, a.ProjectDomainName)
}

// newDNSQuotaListCommand is spelled "list" to match upstream even though the
// designate API has no collection endpoint for quotas: it reads one project's
// quotas, which is what `openstack dns quota list` does too.
func newDNSQuotaListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	target := &dnsQuotaTarget{}
	common := &commonOptions{}
	cmd := &cobra.Command{
		Use:     "list [<project>]",
		Short:   "Show a project's DNS quotas",
		Aliases: []string{"show"},
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, session, err := newDNSSession(ctx, a)
			if err != nil {
				return err
			}
			project, err := target.resolve(ctx, session, a, args, common)
			if err != nil {
				return err
			}
			return runDNSQuotaList(ctx, client, o, project, common, cmd.OutOrStdout())
		},
	}
	target.bind(cmd)
	common.bind(cmd)
	return cmd
}

func runDNSQuotaList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	project string, common *commonOptions, w io.Writer,
) error {
	q, err := quotas.Get(ctx, withCommonHeaders(client, common), project).Extract()
	if err != nil {
		return fmt.Errorf("showing DNS quotas for project %q: %w", project, err)
	}
	fields, values := dnsQuotaFields(q)
	return o.WriteSingle(w, fields, values)
}

type dnsQuotaSetFlags struct {
	apiExportSize    int
	recordsetRecords int
	zoneRecords      int
	zoneRecordsets   int
	zones            int
}

func (f *dnsQuotaSetFlags) bindings(opts *quotas.UpdateOpts) map[string]func() {
	return map[string]func(){
		"api-export-size":   func() { opts.APIExporterSize = &f.apiExportSize },
		"recordset-records": func() { opts.RecordsetRecords = &f.recordsetRecords },
		"zone-records":      func() { opts.ZoneRecords = &f.zoneRecords },
		"zone-recordsets":   func() { opts.ZoneRecordsets = &f.zoneRecordsets },
		"zones":             func() { opts.Zones = &f.zones },
	}
}

func newDNSQuotaSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &dnsQuotaSetFlags{}
	target := &dnsQuotaTarget{}
	common := &commonOptions{}
	cmd := &cobra.Command{
		Use:   "set [<project>]",
		Short: "Set a project's DNS quotas",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			changed := changedFlagNames(cmd.Flags())
			ctx := cmd.Context()
			client, session, err := newDNSSession(ctx, a)
			if err != nil {
				return err
			}
			project, err := target.resolve(ctx, session, a, args, common)
			if err != nil {
				return err
			}
			return runDNSQuotaSet(ctx, client, o, project, f, changed, common, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.IntVar(&f.zones, "zones", 0, "maximum zones")
	fl.IntVar(&f.zoneRecordsets, "zone-recordsets", 0, "maximum recordsets per zone")
	fl.IntVar(&f.zoneRecords, "zone-records", 0, "maximum records per zone")
	fl.IntVar(&f.recordsetRecords, "recordset-records", 0, "maximum records per recordset")
	fl.IntVar(&f.apiExportSize, "api-export-size", 0, "maximum number of recordsets an API zone export may contain")
	target.bind(cmd)
	common.bind(cmd)
	return cmd
}

// runDNSQuotaSet sends only the quotas whose flags were given, so a set of one
// does not reset the rest to zero.
func runDNSQuotaSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	project string, f *dnsQuotaSetFlags, changed map[string]bool, common *commonOptions, w io.Writer,
) error {
	var opts quotas.UpdateOpts
	bindings := f.bindings(&opts)
	names := make([]string, 0, len(bindings))
	for name := range bindings {
		names = append(names, name)
	}
	// Fixed order so the request body is deterministic.
	sort.Strings(names)
	touched := false
	for _, name := range names {
		if changed[name] {
			bindings[name]()
			touched = true
		}
	}
	if !touched {
		return fmt.Errorf("nothing to set: pass at least one quota flag")
	}
	q, err := quotas.Update(ctx, withCommonHeaders(client, common), project, opts).Extract()
	if err != nil {
		return fmt.Errorf("setting DNS quotas for project %q: %w", project, err)
	}
	fields, values := dnsQuotaFields(q)
	return o.WriteSingle(w, fields, values)
}

func newDNSQuotaResetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	target := &dnsQuotaTarget{}
	common := &commonOptions{}
	cmd := &cobra.Command{
		Use:   "reset [<project>]",
		Short: "Reset a project's DNS quotas to the deployment defaults",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, session, err := newDNSSession(ctx, a)
			if err != nil {
				return err
			}
			project, err := target.resolve(ctx, session, a, args, common)
			if err != nil {
				return err
			}
			return runDNSQuotaReset(ctx, client, project, common, cmd.OutOrStdout())
		},
	}
	target.bind(cmd)
	common.bind(cmd)
	return cmd
}

// runDNSQuotaReset issues DELETE /v2/quotas/<project>, which gophercloud's
// dns/v2/quotas package does not cover — it has Get and Update only. Raw fallback
// per AGENTS.md; replace it if a typed Delete lands.
func runDNSQuotaReset(ctx context.Context, client *gophercloud.ServiceClient,
	project string, common *commonOptions, w io.Writer,
) error {
	resp, err := client.Delete(ctx, client.ServiceURL("quotas", project), &gophercloud.RequestOpts{
		OkCodes:     []int{204},
		MoreHeaders: common.headers(),
	})
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if _, _, err = gophercloud.ParseResponse(resp, err); err != nil {
		return fmt.Errorf("resetting DNS quotas for project %q: %w", project, err)
	}
	if _, err := fmt.Fprintf(w, "Reset DNS quotas for project %s to the defaults\n", project); err != nil {
		return err
	}
	return nil
}

// changedFlagNames records which flags an invocation gave, so the sparse-update
// seams take plain data rather than a *pflag.FlagSet.
func changedFlagNames(fl *pflag.FlagSet) map[string]bool {
	set := make(map[string]bool)
	fl.VisitAll(func(f *pflag.Flag) {
		if f.Changed {
			set[f.Name] = true
		}
	})
	return set
}

// --- tsigkey ---------------------------------------------------------------

// newTSIGKeyCommand builds "tsigkey ...", the TSIG keys designate uses to
// authenticate zone transfers with external nameservers.
func newTSIGKeyCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tsigkey",
		Short: "Manage TSIG keys (authenticated zone transfers with external nameservers)",
	}
	cmd.AddCommand(
		newTSIGKeyListCommand(a, o),
		newTSIGKeyShowCommand(a, o),
		newTSIGKeyCreateCommand(a, o),
		newTSIGKeySetCommand(a, o),
		newTSIGKeyDeleteCommand(a, o),
	)
	return cmd
}

func tsigKeyFields(k *tsigkeys.TSIGKey) ([]string, []any) {
	return []string{"id", "name", "algorithm", "scope", "resource_id", "created_at", "updated_at"},
		[]any{k.ID, k.Name, k.Algorithm, k.Scope, k.ResourceID, dnsTime(k.CreatedAt), dnsTime(k.UpdatedAt)}
}

// resolveTSIGKeyID turns a TSIG key name or ID into an ID. Names are unique per
// deployment, and designate's list takes an exact ?name= filter.
func resolveTSIGKeyID(ctx context.Context, client *gophercloud.ServiceClient, ref string) (string, error) {
	if ref == "" || resolve.IsUUID(ref) {
		return ref, nil
	}
	pages, err := tsigkeys.List(client, tsigkeys.ListOpts{Name: ref}).AllPages(ctx)
	if err != nil {
		return "", fmt.Errorf("looking up TSIG key %q: %w", ref, err)
	}
	all, err := tsigkeys.ExtractTSIGKeys(pages)
	if err != nil {
		return "", fmt.Errorf("looking up TSIG key %q: %w", ref, err)
	}
	switch len(all) {
	case 0:
		return ref, nil
	case 1:
		return all[0].ID, nil
	default:
		return "", fmt.Errorf("TSIG key name %q is ambiguous: %d matches, use the ID", ref, len(all))
	}
}

type tsigKeyListFlags struct {
	name      string
	algorithm string
	scope     string
	limit     int
}

func newTSIGKeyListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &tsigKeyListFlags{}
	common := &commonOptions{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List TSIG keys",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := common.client(ctx, a)
			if err != nil {
				return err
			}
			return runTSIGKeyList(ctx, client, o, f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "filter by key name")
	fl.StringVar(&f.algorithm, "algorithm", "", "filter by algorithm, e.g. hmac-sha256")
	fl.StringVar(&f.scope, "scope", "", "filter by scope: POOL or ZONE")
	fl.IntVar(&f.limit, "limit", 0, "maximum number of keys to return")
	common.bind(cmd)
	return cmd
}

func runTSIGKeyList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	f *tsigKeyListFlags, w io.Writer,
) error {
	opts := tsigkeys.ListOpts{Name: f.name, Algorithm: f.algorithm, Scope: f.scope, Limit: f.limit}
	pages, err := tsigkeys.List(client, opts).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing TSIG keys: %w", err)
	}
	all, err := tsigkeys.ExtractTSIGKeys(pages)
	if err != nil {
		return fmt.Errorf("parsing TSIG key list: %w", err)
	}
	// The secret is deliberately not a column: it is the shared authentication
	// material, and listing keys should not spray it across a terminal or a log.
	t := output.Table{
		Columns: []string{"ID", "Name", "Algorithm", "Scope", "Resource ID"},
		Rows:    make([][]any, 0, len(all)),
	}
	for _, k := range all {
		t.Rows = append(t.Rows, []any{k.ID, k.Name, k.Algorithm, k.Scope, k.ResourceID})
	}
	return o.WriteList(w, t)
}

func newTSIGKeyShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var showSecret bool
	common := &commonOptions{}
	cmd := &cobra.Command{
		Use:   "show <tsigkey>",
		Short: "Show TSIG key details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := common.client(ctx, a)
			if err != nil {
				return err
			}
			return runTSIGKeyShow(ctx, client, o, args[0], showSecret, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&showSecret, "show-secret", false,
		"include the shared secret in the output (omitted by default so it does not land in logs)")
	common.bind(cmd)
	return cmd
}

// runTSIGKeyShow omits the secret unless asked. It is the shared authentication
// material for zone transfers, so printing it by default would put it into shell
// history, terminal scrollback and CI logs for anyone who merely wanted the
// algorithm or scope.
func runTSIGKeyShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	ref string, showSecret bool, w io.Writer,
) error {
	id, err := resolveTSIGKeyID(ctx, client, ref)
	if err != nil {
		return err
	}
	k, err := tsigkeys.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("showing TSIG key %q: %w", ref, err)
	}
	fields, values := tsigKeyFields(k)
	if showSecret {
		fields = append(fields, "secret")
		values = append(values, k.Secret)
	}
	return o.WriteSingle(w, fields, values)
}

type tsigKeyWriteFlags struct {
	name       string
	algorithm  string
	secret     string
	scope      string
	resourceID string
}

func newTSIGKeyCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &tsigKeyWriteFlags{}
	common := &commonOptions{}
	cmd := &cobra.Command{
		// Upstream designate names the key with --name and takes no positional;
		// koc has always taken it positionally. Both work — see internal/cli/nameflag.
		Use:   "create [<name>]",
		Short: "Create a TSIG key",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			name, err := nameflag.Resolve(args, f.name, true)
			if err != nil {
				return err
			}
			// designate requires all four; naming the missing one beats a 400.
			for _, req := range []struct{ flag, value string }{
				{"--algorithm", f.algorithm},
				{"--secret", f.secret},
				{"--scope", f.scope},
				{"--resource-id", f.resourceID},
			} {
				if req.value == "" {
					return fmt.Errorf("%s is required", req.flag)
				}
			}
			ctx := cmd.Context()
			client, err := common.client(ctx, a)
			if err != nil {
				return err
			}
			return runTSIGKeyCreate(ctx, client, o, name, f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "name of the key (upstream spelling; the positional form also works)")
	fl.StringVar(&f.algorithm, "algorithm", "", "TSIG algorithm, e.g. hmac-sha256 (required)")
	fl.StringVar(&f.secret, "secret", "", "shared secret (required)")
	fl.StringVar(&f.scope, "scope", "", "scope: POOL or ZONE (required)")
	fl.StringVar(&f.resourceID, flagResourceID, "", "ID of the pool or zone the key applies to (required)")
	common.bind(cmd)
	return cmd
}

func runTSIGKeyCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	name string, f *tsigKeyWriteFlags, w io.Writer,
) error {
	k, err := tsigkeys.Create(ctx, client, tsigkeys.CreateOpts{
		Name:       name,
		Algorithm:  f.algorithm,
		Secret:     f.secret,
		Scope:      f.scope,
		ResourceID: f.resourceID,
	}).Extract()
	if err != nil {
		return fmt.Errorf("creating TSIG key %q: %w", name, err)
	}
	fields, values := tsigKeyFields(k)
	return o.WriteSingle(w, fields, values)
}

func newTSIGKeySetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &tsigKeyWriteFlags{}
	common := &commonOptions{}
	cmd := &cobra.Command{
		Use:   "set <tsigkey>",
		Short: "Update a TSIG key",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			fl := cmd.Flags()
			if !fl.Changed("name") && !fl.Changed("algorithm") && !fl.Changed("secret") &&
				!fl.Changed("scope") && !fl.Changed(flagResourceID) {
				return fmt.Errorf("nothing to set: pass at least one attribute flag")
			}
			ctx := cmd.Context()
			client, err := common.client(ctx, a)
			if err != nil {
				return err
			}
			return runTSIGKeySet(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "new key name")
	fl.StringVar(&f.algorithm, "algorithm", "", "new algorithm")
	fl.StringVar(&f.secret, "secret", "", "new shared secret")
	fl.StringVar(&f.scope, "scope", "", "new scope: POOL or ZONE")
	fl.StringVar(&f.resourceID, flagResourceID, "", "new pool or zone ID")
	common.bind(cmd)
	return cmd
}

// runTSIGKeySet relies on UpdateOpts tagging every field omitempty, so the ones
// left empty are simply absent from the request body.
func runTSIGKeySet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	ref string, f *tsigKeyWriteFlags, w io.Writer,
) error {
	id, err := resolveTSIGKeyID(ctx, client, ref)
	if err != nil {
		return err
	}
	k, err := tsigkeys.Update(ctx, client, id, tsigkeys.UpdateOpts{
		Name:       f.name,
		Algorithm:  f.algorithm,
		Secret:     f.secret,
		Scope:      f.scope,
		ResourceID: f.resourceID,
	}).Extract()
	if err != nil {
		return fmt.Errorf("updating TSIG key %q: %w", ref, err)
	}
	fields, values := tsigKeyFields(k)
	return o.WriteSingle(w, fields, values)
}

func newTSIGKeyDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	common := &commonOptions{}
	cmd := &cobra.Command{
		Use:   "delete <tsigkey> [<tsigkey>...]",
		Short: "Delete one or more TSIG keys",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := common.client(ctx, a)
			if err != nil {
				return err
			}
			return runTSIGKeyDelete(ctx, client, args, cmd.OutOrStdout())
		},
	}
	common.bind(cmd)
	return cmd
}

func runTSIGKeyDelete(ctx context.Context, client *gophercloud.ServiceClient, refs []string, w io.Writer) error {
	return batchdelete.Each(refs, func(ref string) error {
		id, err := resolveTSIGKeyID(ctx, client, ref)
		if err != nil {
			return err
		}
		if derr := tsigkeys.Delete(ctx, client, id).ExtractErr(); derr != nil {
			return fmt.Errorf("deleting TSIG key %q: %w", ref, derr)
		}
		if _, werr := fmt.Fprintf(w, "Deleted TSIG key %s\n", ref); werr != nil {
			return werr
		}
		return nil
	})
}

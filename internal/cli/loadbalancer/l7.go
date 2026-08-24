package loadbalancer

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/loadbalancer/v2/l7policies"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
	"github.com/ftarasenko/go-openstackclient/internal/cli/nameflag"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// newL7PolicyCommand builds "loadbalancer l7policy ...".
func newL7PolicyCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "l7policy",
		Short: "Manage layer-7 policies on a listener",
	}
	cmd.AddCommand(
		newL7PolicyListCommand(a, o),
		newL7PolicyShowCommand(a, o),
		newL7PolicyCreateCommand(a, o),
		newL7PolicySetCommand(a, o),
		newL7PolicyDeleteCommand(a, o),
	)
	return cmd
}

// newL7RuleCommand builds "loadbalancer l7rule ...". Rules are l7policy
// subresources in octavia — gophercloud even keeps them in the l7policies
// package — so the policy is the first positional argument on every verb.
func newL7RuleCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "l7rule",
		Short: "Manage the rules of a layer-7 policy",
	}
	cmd.AddCommand(
		newL7RuleListCommand(a, o),
		newL7RuleShowCommand(a, o),
		newL7RuleCreateCommand(a, o),
		newL7RuleSetCommand(a, o),
		newL7RuleDeleteCommand(a, o),
	)
	return cmd
}

func l7PolicyFields(p *l7policies.L7Policy) ([]string, []any) {
	fields := []string{
		"id", "name", "description", "listener_id", "action", "position",
		"redirect_pool_id", "redirect_url", "redirect_prefix", "redirect_http_code",
		"rules", "admin_state_up", "project_id", "provisioning_status",
		"operating_status", "tags",
	}
	values := []any{
		p.ID, p.Name, p.Description, p.ListenerID, p.Action, p.Position,
		p.RedirectPoolID, p.RedirectURL, p.RedirectPrefix, p.RedirectHttpCode,
		p.Rules, p.AdminStateUp, p.ProjectID, p.ProvisioningStatus,
		p.OperatingStatus, p.Tags,
	}
	return fields, values
}

func l7RuleFields(r *l7policies.Rule) ([]string, []any) {
	fields := []string{
		"id", "type", "compare_type", "key", "value", "invert", "admin_state_up",
		"project_id", "provisioning_status", "operating_status", "tags",
	}
	values := []any{
		r.ID, r.RuleType, r.CompareType, r.Key, r.Value, r.Invert, r.AdminStateUp,
		r.ProjectID, r.ProvisioningStatus, r.OperatingStatus, r.Tags,
	}
	return fields, values
}

func resolveL7PolicyID(ctx context.Context, client *gophercloud.ServiceClient, ref string) (string, error) {
	return resolveByName("l7policy", ref, func() ([]string, error) {
		pages, err := l7policies.List(client, l7policies.ListOpts{Name: ref}).AllPages(ctx)
		if err != nil {
			return nil, err
		}
		all, err := l7policies.ExtractL7Policies(pages)
		if err != nil {
			return nil, err
		}
		ids := make([]string, 0, len(all))
		for _, p := range all {
			ids = append(ids, p.ID)
		}
		return ids, nil
	})
}

// --- l7policy list/show ----------------------------------------------------

type l7PolicyListFlags struct {
	name     string
	listener string
	action   string
	project  string
	long     bool
}

func newL7PolicyListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &l7PolicyListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List layer-7 policies",
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
			refs, err := resolveLBRefs(ctx, session, lbRefs{project: f.project})
			if err != nil {
				return err
			}
			return runL7PolicyList(ctx, client, o, f, refs.projectID, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "filter by policy name")
	fl.StringVar(&f.listener, "listener", "", "list only policies of this listener (name or ID)")
	fl.StringVar(&f.action, "action", "", "filter by action: REDIRECT_TO_POOL, REDIRECT_TO_URL, REDIRECT_PREFIX or REJECT")
	fl.StringVar(&f.project, "project", "", "filter by owning project (name or ID)")
	fl.BoolVar(&f.long, "long", false, "list additional fields in output")
	return cmd
}

func runL7PolicyList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	f *l7PolicyListFlags, projectID string, w io.Writer,
) error {
	opts := l7policies.ListOpts{
		Name:      f.name,
		Action:    f.action,
		ProjectID: projectID,
	}
	if f.listener != "" {
		listenerID, err := resolveListenerID(ctx, client, f.listener)
		if err != nil {
			return err
		}
		opts.ListenerID = listenerID
	}
	pages, err := l7policies.List(client, opts).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing l7 policies: %w", err)
	}
	all, err := l7policies.ExtractL7Policies(pages)
	if err != nil {
		return fmt.Errorf("parsing l7 policy list: %w", err)
	}

	cols := []string{"ID", "Name", "Action", "Position", "Listener ID", "Operating Status"}
	if f.long {
		cols = append(cols, "Provisioning Status", "Admin State Up", "Redirect Pool", "Redirect URL", "Rules", "Project")
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(all))}
	for i := range all {
		p := &all[i]
		row := []any{p.ID, p.Name, p.Action, p.Position, p.ListenerID, p.OperatingStatus}
		if f.long {
			row = append(row, p.ProvisioningStatus, p.AdminStateUp, p.RedirectPoolID, p.RedirectURL, len(p.Rules), p.ProjectID)
		}
		t.Rows = append(t.Rows, row)
	}
	return o.WriteList(w, t)
}

func newL7PolicyShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <l7policy>",
		Short: "Show layer-7 policy details",
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
			return runL7PolicyShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runL7PolicyShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, w io.Writer) error {
	id, err := resolveL7PolicyID(ctx, client, ref)
	if err != nil {
		return err
	}
	p, err := l7policies.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("showing l7 policy %q: %w", ref, err)
	}
	fields, values := l7PolicyFields(p)
	return o.WriteSingle(w, fields, values)
}

// --- l7policy create/set ---------------------------------------------------

type l7PolicyWriteFlags struct {
	name             string
	description      string
	listener         string
	action           string
	position         int32
	redirectPool     string
	redirectURL      string
	redirectPrefix   string
	redirectHTTPCode int32
	tag              []string
	noTag            bool
	project          string
	enable           bool
	disable          bool

	adminStateUp *bool
}

func (f *l7PolicyWriteFlags) register(cmd *cobra.Command, isCreate bool) {
	fl := cmd.Flags()
	fl.StringVar(&f.description, "description", "", "policy description")
	fl.Int32Var(&f.position, "position", 0, "evaluation order within the listener (1 is first)")
	fl.StringVar(&f.redirectPool, "redirect-pool", "", "pool to redirect to, for action REDIRECT_TO_POOL (name or ID)")
	fl.StringVar(&f.redirectURL, "redirect-url", "", "URL to redirect to, for action REDIRECT_TO_URL")
	fl.StringVar(&f.redirectPrefix, "redirect-prefix", "", "URL prefix to redirect to, for action REDIRECT_PREFIX")
	fl.Int32Var(&f.redirectHTTPCode, "redirect-http-code", 0, "HTTP status to use for the redirect, e.g. 301 or 302")
	fl.StringArrayVar(&f.tag, "tag", nil, "tag to set (repeatable)")
	fl.BoolVar(&f.enable, "enable", false, "administratively up")
	fl.BoolVar(&f.disable, "disable", false, "administratively down")
	if isCreate {
		// Upstream octavia names a new policy with --name and has no positional
		// for it; koc grew the positional first. Both work — see internal/cli/nameflag.
		fl.StringVar(&f.name, "name", "", "name of the policy (upstream spelling; the positional form also works)")
		fl.StringVar(&f.listener, "listener", "", "listener to attach the policy to (name or ID, required)")
		fl.StringVar(&f.action, "action", "",
			"policy action: REDIRECT_TO_POOL, REDIRECT_TO_URL, REDIRECT_PREFIX or REJECT (required)")
		fl.StringVar(&f.project, "project", "", "owning project (name or ID)")
		return
	}
	fl.StringVar(&f.name, "name", "", "new policy name")
	fl.StringVar(&f.action, "action", "", "new policy action")
	fl.BoolVar(&f.noTag, "no-tag", false, "clear all tags")
}

func newL7PolicyCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &l7PolicyWriteFlags{}
	cmd := &cobra.Command{
		Use:   "create [<name>]",
		Short: "Create a layer-7 policy on a listener",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			name, err := nameflag.Resolve(args, f.name, false)
			if err != nil {
				return err
			}
			fl := cmd.Flags()
			if fl.Changed("enable") && fl.Changed("disable") {
				return fmt.Errorf("--enable and --disable are mutually exclusive")
			}
			f.adminStateUp = triState(fl, f.enable, f.disable)
			if f.listener == "" || f.action == "" {
				return fmt.Errorf("--listener and --action are required")
			}
			// Each action needs its own target; octavia rejects a mismatch, so name
			// the missing flag rather than relaying a generic 400.
			if err := checkL7Action(f); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, session, err := newLoadBalancerSession(ctx, a)
			if err != nil {
				return err
			}
			refs, err := resolveLBRefs(ctx, session, lbRefs{project: f.project})
			if err != nil {
				return err
			}
			return runL7PolicyCreate(ctx, client, o, name, f, refs.projectID, cmd.OutOrStdout())
		},
	}
	f.register(cmd, true)
	return cmd
}

// checkL7Action verifies the action has the target it needs. REJECT takes none,
// and each REDIRECT_* takes exactly one specific flag.
func checkL7Action(f *l7PolicyWriteFlags) error {
	switch f.action {
	case "REDIRECT_TO_POOL":
		if f.redirectPool == "" {
			return fmt.Errorf("--action REDIRECT_TO_POOL requires --redirect-pool")
		}
	case "REDIRECT_TO_URL":
		if f.redirectURL == "" {
			return fmt.Errorf("--action REDIRECT_TO_URL requires --redirect-url")
		}
	case "REDIRECT_PREFIX":
		if f.redirectPrefix == "" {
			return fmt.Errorf("--action REDIRECT_PREFIX requires --redirect-prefix")
		}
	case "REJECT":
		if f.redirectPool != "" || f.redirectURL != "" || f.redirectPrefix != "" {
			return fmt.Errorf("--action REJECT takes no redirect target")
		}
	default:
		return fmt.Errorf("unsupported --action %q: expected REDIRECT_TO_POOL, REDIRECT_TO_URL, REDIRECT_PREFIX or REJECT", f.action)
	}
	return nil
}

func runL7PolicyCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	name string, f *l7PolicyWriteFlags, projectID string, w io.Writer,
) error {
	listenerID, err := resolveListenerID(ctx, client, f.listener)
	if err != nil {
		return err
	}
	opts := l7policies.CreateOpts{
		Name:             name,
		Description:      f.description,
		ListenerID:       listenerID,
		Action:           l7policies.Action(f.action),
		Position:         f.position,
		RedirectURL:      f.redirectURL,
		RedirectPrefix:   f.redirectPrefix,
		RedirectHttpCode: f.redirectHTTPCode,
		ProjectID:        projectID,
		AdminStateUp:     f.adminStateUp,
	}
	if f.redirectPool != "" {
		poolID, perr := resolvePoolID(ctx, client, f.redirectPool)
		if perr != nil {
			return perr
		}
		opts.RedirectPoolID = poolID
	}
	p, err := l7policies.Create(ctx, client, opts).Extract()
	if err != nil {
		return fmt.Errorf("creating l7 policy %q: %w", name, err)
	}
	fields, values := l7PolicyFields(p)
	return o.WriteSingle(w, fields, values)
}

func newL7PolicySetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &l7PolicyWriteFlags{}
	cmd := &cobra.Command{
		Use:   "set <l7policy>",
		Short: "Update a layer-7 policy",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			fl := cmd.Flags()
			if fl.Changed("enable") && fl.Changed("disable") {
				return fmt.Errorf("--enable and --disable are mutually exclusive")
			}
			if fl.Changed("tag") && f.noTag {
				return fmt.Errorf("--tag and --no-tag are mutually exclusive")
			}
			f.adminStateUp = triState(fl, f.enable, f.disable)
			ctx := cmd.Context()
			client, err := newLoadBalancerClient(ctx, a)
			if err != nil {
				return err
			}
			return runL7PolicySet(ctx, client, o, args[0], f, changedFlags(fl), cmd.OutOrStdout())
		},
	}
	f.register(cmd, false)
	return cmd
}

func runL7PolicySet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	ref string, f *l7PolicyWriteFlags, changed changedSet, w io.Writer,
) error {
	opts := l7policies.UpdateOpts{AdminStateUp: f.adminStateUp}
	touched := f.adminStateUp != nil

	assignString(changed, "name", f.name, &opts.Name, &touched)
	assignString(changed, "description", f.description, &opts.Description, &touched)
	assignString(changed, "redirect-url", f.redirectURL, &opts.RedirectURL, &touched)
	assignString(changed, "redirect-prefix", f.redirectPrefix, &opts.RedirectPrefix, &touched)
	if changed["action"] {
		opts.Action = l7policies.Action(f.action)
		touched = true
	}
	if changed["position"] {
		opts.Position = f.position
		touched = true
	}
	if changed["redirect-http-code"] {
		opts.RedirectHttpCode = f.redirectHTTPCode
		touched = true
	}
	if changed["redirect-pool"] {
		poolID, err := resolvePoolID(ctx, client, f.redirectPool)
		if err != nil {
			return err
		}
		opts.RedirectPoolID = &poolID
		touched = true
	}
	switch {
	case f.noTag:
		empty := []string{}
		opts.Tags = &empty
		touched = true
	case changed["tag"]:
		tags := f.tag
		opts.Tags = &tags
		touched = true
	}
	if !touched {
		return fmt.Errorf("nothing to set: pass at least one attribute flag")
	}

	id, err := resolveL7PolicyID(ctx, client, ref)
	if err != nil {
		return err
	}
	p, err := l7policies.Update(ctx, client, id, opts).Extract()
	if err != nil {
		return fmt.Errorf("updating l7 policy %q: %w", ref, err)
	}
	fields, values := l7PolicyFields(p)
	return o.WriteSingle(w, fields, values)
}

func newL7PolicyDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <l7policy> [<l7policy>...]",
		Short: "Delete one or more layer-7 policies",
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
			return runL7PolicyDelete(ctx, client, args, cmd.OutOrStdout())
		},
	}
}

func runL7PolicyDelete(ctx context.Context, client *gophercloud.ServiceClient, refs []string, w io.Writer) error {
	return batchdelete.Each(refs, func(ref string) error {
		id, err := resolveL7PolicyID(ctx, client, ref)
		if err != nil {
			return err
		}
		if derr := l7policies.Delete(ctx, client, id).ExtractErr(); derr != nil {
			return fmt.Errorf("deleting l7 policy %q: %w", ref, derr)
		}
		if _, werr := fmt.Fprintf(w, "Requested deletion of l7 policy %s\n", ref); werr != nil {
			return werr
		}
		return nil
	})
}

// --- l7rule ----------------------------------------------------------------

type l7RuleListFlags struct {
	typ         string
	compareType string
	long        bool
}

func newL7RuleListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &l7RuleListFlags{}
	cmd := &cobra.Command{
		Use:   "list <l7policy>",
		Short: "List the rules of a layer-7 policy",
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
			return runL7RuleList(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.typ, "type", "", "filter by rule type, e.g. PATH, HOST_NAME, HEADER, COOKIE, FILE_TYPE, SSL_CONN_HAS_CERT")
	fl.StringVar(&f.compareType, flagCompareType, "", "filter by comparison: REGEX, STARTS_WITH, ENDS_WITH, CONTAINS or EQUAL_TO")
	fl.BoolVar(&f.long, "long", false, "list additional fields in output")
	return cmd
}

func runL7RuleList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	policyRef string, f *l7RuleListFlags, w io.Writer,
) error {
	policyID, err := resolveL7PolicyID(ctx, client, policyRef)
	if err != nil {
		return err
	}
	opts := l7policies.ListRulesOpts{RuleType: l7policies.RuleType(f.typ), CompareType: l7policies.CompareType(f.compareType)}
	pages, err := l7policies.ListRules(client, policyID, opts).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing rules of l7 policy %q: %w", policyRef, err)
	}
	all, err := l7policies.ExtractRules(pages)
	if err != nil {
		return fmt.Errorf("parsing l7 rule list: %w", err)
	}

	cols := []string{"ID", "Type", "Compare Type", "Key", "Value", "Operating Status"}
	if f.long {
		cols = append(cols, "Provisioning Status", "Admin State Up", "Invert", "Tags")
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(all))}
	for _, r := range all {
		row := []any{r.ID, r.RuleType, r.CompareType, r.Key, r.Value, r.OperatingStatus}
		if f.long {
			row = append(row, r.ProvisioningStatus, r.AdminStateUp, r.Invert, r.Tags)
		}
		t.Rows = append(t.Rows, row)
	}
	return o.WriteList(w, t)
}

func newL7RuleShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <l7policy> <l7rule>",
		Short: "Show details of one layer-7 rule",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newLoadBalancerClient(ctx, a)
			if err != nil {
				return err
			}
			return runL7RuleShow(ctx, client, o, args[0], args[1], cmd.OutOrStdout())
		},
	}
}

func runL7RuleShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	policyRef, ruleID string, w io.Writer,
) error {
	policyID, err := resolveL7PolicyID(ctx, client, policyRef)
	if err != nil {
		return err
	}
	// Rules have no name, so there is nothing to resolve: the reference is the ID.
	r, err := l7policies.GetRule(ctx, client, policyID, ruleID).Extract()
	if err != nil {
		return fmt.Errorf("showing rule %s of l7 policy %q: %w", ruleID, policyRef, err)
	}
	fields, values := l7RuleFields(r)
	return o.WriteSingle(w, fields, values)
}

type l7RuleWriteFlags struct {
	typ         string
	compareType string
	value       string
	key         string
	invert      bool
	noInvert    bool
	tag         []string
	noTag       bool
	project     string
	enable      bool
	disable     bool

	// changed is the set of flags actually given, captured in RunE.
	changed changedSet

	adminStateUp *bool
}

func (f *l7RuleWriteFlags) register(cmd *cobra.Command, isCreate bool) {
	fl := cmd.Flags()
	fl.StringVar(&f.typ, "type", "",
		"rule type: PATH, HOST_NAME, HEADER, COOKIE, FILE_TYPE, SSL_CONN_HAS_CERT, SSL_VERIFY_RESULT or SSL_DN_FIELD")
	fl.StringVar(&f.compareType, flagCompareType, "",
		"comparison: REGEX, STARTS_WITH, ENDS_WITH, CONTAINS or EQUAL_TO")
	fl.StringVar(&f.value, "value", "", "value to compare against")
	fl.StringVar(&f.key, "key", "", "key to compare, required for HEADER and COOKIE rules")
	fl.BoolVar(&f.invert, flagInvert, false, "match when the comparison does NOT hold")
	fl.StringArrayVar(&f.tag, "tag", nil, "tag to set (repeatable)")
	fl.BoolVar(&f.enable, "enable", false, "administratively up")
	fl.BoolVar(&f.disable, "disable", false, "administratively down")
	if isCreate {
		fl.StringVar(&f.project, "project", "", "owning project (name or ID)")
		return
	}
	fl.BoolVar(&f.noInvert, flagNoInvert, false, "match when the comparison holds (the default)")
	fl.BoolVar(&f.noTag, "no-tag", false, "clear all tags")
}

func newL7RuleCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &l7RuleWriteFlags{}
	cmd := &cobra.Command{
		Use:   "create <l7policy>",
		Short: "Add a rule to a layer-7 policy",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			fl := cmd.Flags()
			if fl.Changed("enable") && fl.Changed("disable") {
				return fmt.Errorf("--enable and --disable are mutually exclusive")
			}
			f.adminStateUp = triState(fl, f.enable, f.disable)
			if f.typ == "" || f.compareType == "" || f.value == "" {
				return fmt.Errorf("--type, --compare-type and --value are required")
			}
			// HEADER and COOKIE rules compare a named field, so the name is required.
			if (f.typ == "HEADER" || f.typ == "COOKIE") && f.key == "" {
				return fmt.Errorf("--type %s requires --key", f.typ)
			}
			ctx := cmd.Context()
			client, session, err := newLoadBalancerSession(ctx, a)
			if err != nil {
				return err
			}
			refs, err := resolveLBRefs(ctx, session, lbRefs{project: f.project})
			if err != nil {
				return err
			}
			return runL7RuleCreate(ctx, client, o, args[0], f, refs.projectID, cmd.OutOrStdout())
		},
	}
	f.register(cmd, true)
	return cmd
}

func runL7RuleCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	policyRef string, f *l7RuleWriteFlags, projectID string, w io.Writer,
) error {
	policyID, err := resolveL7PolicyID(ctx, client, policyRef)
	if err != nil {
		return err
	}
	opts := l7policies.CreateRuleOpts{
		RuleType:     l7policies.RuleType(f.typ),
		CompareType:  l7policies.CompareType(f.compareType),
		Value:        f.value,
		Key:          f.key,
		Invert:       f.invert,
		ProjectID:    projectID,
		Tags:         f.tag,
		AdminStateUp: f.adminStateUp,
	}
	r, err := l7policies.CreateRule(ctx, client, policyID, opts).Extract()
	if err != nil {
		return fmt.Errorf("adding rule to l7 policy %q: %w", policyRef, err)
	}
	fields, values := l7RuleFields(r)
	return o.WriteSingle(w, fields, values)
}

func newL7RuleSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &l7RuleWriteFlags{}
	cmd := &cobra.Command{
		Use:   "set <l7policy> <l7rule>",
		Short: "Update a layer-7 rule",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			fl := cmd.Flags()
			if fl.Changed("enable") && fl.Changed("disable") {
				return fmt.Errorf("--enable and --disable are mutually exclusive")
			}
			if fl.Changed(flagInvert) && fl.Changed(flagNoInvert) {
				return fmt.Errorf("--invert and --no-invert are mutually exclusive")
			}
			if fl.Changed("tag") && f.noTag {
				return fmt.Errorf("--tag and --no-tag are mutually exclusive")
			}
			f.adminStateUp = triState(fl, f.enable, f.disable)
			ctx := cmd.Context()
			client, err := newLoadBalancerClient(ctx, a)
			if err != nil {
				return err
			}
			f.changed = changedFlags(fl)
			return runL7RuleSet(ctx, client, o, args[0], args[1], f, cmd.OutOrStdout())
		},
	}
	f.register(cmd, false)
	return cmd
}

func runL7RuleSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	policyRef, ruleID string, f *l7RuleWriteFlags, w io.Writer,
) error {
	changed := f.changed
	opts := l7policies.UpdateRuleOpts{AdminStateUp: f.adminStateUp}
	touched := f.adminStateUp != nil

	assignString(changed, "key", f.key, &opts.Key, &touched)
	if changed["type"] {
		opts.RuleType = l7policies.RuleType(f.typ)
		touched = true
	}
	if changed[flagCompareType] {
		opts.CompareType = l7policies.CompareType(f.compareType)
		touched = true
	}
	if changed["value"] {
		opts.Value = f.value
		touched = true
	}
	switch {
	case changed[flagInvert]:
		assignBool(changed, flagInvert, true, &opts.Invert, &touched)
	case changed[flagNoInvert]:
		v := false
		opts.Invert = &v
		touched = true
	}
	switch {
	case f.noTag:
		empty := []string{}
		opts.Tags = &empty
		touched = true
	case changed["tag"]:
		tags := f.tag
		opts.Tags = &tags
		touched = true
	}
	if !touched {
		return fmt.Errorf("nothing to set: pass at least one attribute flag")
	}

	policyID, err := resolveL7PolicyID(ctx, client, policyRef)
	if err != nil {
		return err
	}
	r, err := l7policies.UpdateRule(ctx, client, policyID, ruleID, opts).Extract()
	if err != nil {
		return fmt.Errorf("updating rule %s of l7 policy %q: %w", ruleID, policyRef, err)
	}
	fields, values := l7RuleFields(r)
	return o.WriteSingle(w, fields, values)
}

func newL7RuleDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <l7policy> <l7rule> [<l7rule>...]",
		Short: "Delete one or more layer-7 rules",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newLoadBalancerClient(ctx, a)
			if err != nil {
				return err
			}
			return runL7RuleDelete(ctx, client, args[0], args[1:], cmd.OutOrStdout())
		},
	}
}

func runL7RuleDelete(ctx context.Context, client *gophercloud.ServiceClient,
	policyRef string, ruleIDs []string, w io.Writer,
) error {
	policyID, err := resolveL7PolicyID(ctx, client, policyRef)
	if err != nil {
		return err
	}
	return batchdelete.Each(ruleIDs, func(ruleID string) error {
		if derr := l7policies.DeleteRule(ctx, client, policyID, ruleID).ExtractErr(); derr != nil {
			return fmt.Errorf("deleting rule %s of l7 policy %q: %w", ruleID, policyRef, derr)
		}
		if _, werr := fmt.Fprintf(w, "Requested deletion of l7 rule %s\n", ruleID); werr != nil {
			return werr
		}
		return nil
	})
}

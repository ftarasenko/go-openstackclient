package loadbalancer

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/loadbalancer/v2/pools"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// newPoolCommand builds "loadbalancer pool ...".
//
// The name deliberately collides with nothing: `subnet pool` and `dns pool` are
// separate top-level nouns, so cobra resolves all three unambiguously. The help
// text says "load balancer pool" so the three are told apart in `--help` output.
func newPoolCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pool",
		Short: "Manage load balancer pools (member groups behind a listener)",
	}
	cmd.AddCommand(
		newPoolListCommand(a, o),
		newPoolShowCommand(a, o),
		newPoolCreateCommand(a, o),
		newPoolSetCommand(a, o),
		newPoolDeleteCommand(a, o),
	)
	return cmd
}

func poolFields(p *pools.Pool) ([]string, []any) {
	fields := []string{
		"id", "name", "description", "protocol", "lb_algorithm", "loadbalancers",
		"listeners", "members", "healthmonitor_id", "session_persistence",
		"admin_state_up", "project_id", "provisioning_status", "operating_status",
		"tls_enabled", "tags",
	}
	values := []any{
		p.ID, p.Name, p.Description, p.Protocol, p.LBMethod, p.Loadbalancers,
		p.Listeners, p.Members, p.MonitorID, p.Persistence,
		p.AdminStateUp, p.ProjectID, p.ProvisioningStatus, p.OperatingStatus,
		p.TLSEnabled, p.Tags,
	}
	return fields, values
}

func resolvePoolID(ctx context.Context, client *gophercloud.ServiceClient, ref string) (string, error) {
	return resolveByName("pool", ref, func() ([]string, error) {
		pages, err := pools.List(client, pools.ListOpts{Name: ref}).AllPages(ctx)
		if err != nil {
			return nil, err
		}
		all, err := pools.ExtractPools(pages)
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

// --- list ------------------------------------------------------------------

type poolListFlags struct {
	name         string
	loadBalancer string
	protocol     string
	lbAlgorithm  string
	project      string
	enable       bool
	disable      bool
	long         bool

	adminStateUp *bool
}

func newPoolListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &poolListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List pools",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			fl := cmd.Flags()
			if fl.Changed("enable") && fl.Changed("disable") {
				return fmt.Errorf("--enable and --disable are mutually exclusive")
			}
			f.adminStateUp = triState(fl, f.enable, f.disable)
			ctx := cmd.Context()
			client, session, err := newLoadBalancerSession(ctx, a)
			if err != nil {
				return err
			}
			refs, err := resolveLBRefs(ctx, session, lbRefs{project: f.project})
			if err != nil {
				return err
			}
			return runPoolList(ctx, client, o, f, refs.projectID, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "filter by pool name")
	fl.StringVar(&f.loadBalancer, "loadbalancer", "", "list only pools of this load balancer (name or ID)")
	fl.StringVar(&f.protocol, "protocol", "", "filter by protocol, e.g. HTTP, HTTPS, TCP, PROXY, UDP")
	fl.StringVar(&f.lbAlgorithm, "lb-algorithm", "", "filter by algorithm: ROUND_ROBIN, LEAST_CONNECTIONS, SOURCE_IP or SOURCE_IP_PORT")
	fl.StringVar(&f.project, "project", "", "filter by owning project (name or ID)")
	fl.BoolVar(&f.enable, "enable", false, "list only administratively up pools")
	fl.BoolVar(&f.disable, "disable", false, "list only administratively down pools")
	fl.BoolVar(&f.long, "long", false, "list additional fields in output")
	return cmd
}

func runPoolList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	f *poolListFlags, projectID string, w io.Writer,
) error {
	opts := pools.ListOpts{
		Name:         f.name,
		Protocol:     f.protocol,
		LBMethod:     f.lbAlgorithm,
		ProjectID:    projectID,
		AdminStateUp: f.adminStateUp,
	}
	if f.loadBalancer != "" {
		lbID, err := resolveLoadBalancerID(ctx, client, f.loadBalancer)
		if err != nil {
			return err
		}
		opts.LoadbalancerID = lbID
	}
	pages, err := pools.List(client, opts).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing pools: %w", err)
	}
	all, err := pools.ExtractPools(pages)
	if err != nil {
		return fmt.Errorf("parsing pool list: %w", err)
	}

	cols := []string{"ID", "Name", "Protocol", "LB Algorithm", "Members", "Operating Status"}
	if f.long {
		cols = append(cols, "Provisioning Status", "Admin State Up", "Health Monitor", "Project", "Tags")
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(all))}
	for i := range all {
		p := &all[i]
		row := []any{p.ID, p.Name, p.Protocol, p.LBMethod, len(p.Members), p.OperatingStatus}
		if f.long {
			row = append(row, p.ProvisioningStatus, p.AdminStateUp, p.MonitorID, p.ProjectID, p.Tags)
		}
		t.Rows = append(t.Rows, row)
	}
	return o.WriteList(w, t)
}

// --- show ------------------------------------------------------------------

func newPoolShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <pool>",
		Short: "Show pool details",
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
			return runPoolShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runPoolShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, w io.Writer) error {
	id, err := resolvePoolID(ctx, client, ref)
	if err != nil {
		return err
	}
	p, err := pools.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("showing pool %q: %w", ref, err)
	}
	fields, values := poolFields(p)
	return o.WriteSingle(w, fields, values)
}

// --- create / set ----------------------------------------------------------

type poolWriteFlags struct {
	name               string
	description        string
	loadBalancer       string
	listener           string
	protocol           string
	lbAlgorithm        string
	sessionPersistence []string
	tlsEnabled         bool
	noTLS              bool
	tag                []string
	noTag              bool
	project            string
	enable             bool
	disable            bool

	adminStateUp *bool
}

func (f *poolWriteFlags) register(cmd *cobra.Command, isCreate bool) {
	fl := cmd.Flags()
	fl.StringVar(&f.description, "description", "", "pool description")
	fl.StringVar(&f.lbAlgorithm, "lb-algorithm", "",
		"load-balancing algorithm: ROUND_ROBIN, LEAST_CONNECTIONS, SOURCE_IP or SOURCE_IP_PORT")
	fl.StringArrayVar(&f.sessionPersistence, "session-persistence", nil,
		"session persistence as type=<SOURCE_IP|HTTP_COOKIE|APP_COOKIE>[,cookie_name=<name>]")
	fl.StringArrayVar(&f.tag, "tag", nil, "tag to set (repeatable)")
	fl.BoolVar(&f.enable, "enable", false, "administratively up")
	fl.BoolVar(&f.disable, "disable", false, "administratively down")
	if isCreate {
		fl.StringVar(&f.loadBalancer, "loadbalancer", "", "load balancer to attach the pool to (name or ID)")
		fl.StringVar(&f.listener, "listener", "", "listener to attach the pool to (name or ID)")
		fl.StringVar(&f.protocol, "protocol", "", "pool protocol: HTTP, HTTPS, TCP, PROXY, PROXYV2, UDP or SCTP (required)")
		fl.StringVar(&f.project, "project", "", "owning project (name or ID)")
		fl.BoolVar(&f.tlsEnabled, "enable-tls", false, "re-encrypt traffic to the members with TLS")
		return
	}
	fl.StringVar(&f.name, "name", "", "new pool name")
	fl.BoolVar(&f.tlsEnabled, "enable-tls", false, "re-encrypt traffic to the members with TLS")
	fl.BoolVar(&f.noTLS, "disable-tls", false, "do not re-encrypt traffic to the members")
	fl.BoolVar(&f.noTag, "no-tag", false, "clear all tags")
}

func newPoolCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &poolWriteFlags{}
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new pool",
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
			if f.protocol == "" || f.lbAlgorithm == "" {
				return fmt.Errorf("--protocol and --lb-algorithm are required")
			}
			// Octavia attaches a pool to exactly one of a load balancer or a listener.
			if (f.loadBalancer == "") == (f.listener == "") {
				return fmt.Errorf("exactly one of --loadbalancer or --listener is required")
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
			return runPoolCreate(ctx, client, o, args[0], f, refs.projectID, cmd.OutOrStdout())
		},
	}
	f.register(cmd, true)
	return cmd
}

func runPoolCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	name string, f *poolWriteFlags, projectID string, w io.Writer,
) error {
	persistence, err := parseSessionPersistence(f.sessionPersistence)
	if err != nil {
		return err
	}
	opts := pools.CreateOpts{
		Name:         name,
		Description:  f.description,
		Protocol:     pools.Protocol(f.protocol),
		LBMethod:     pools.LBMethod(f.lbAlgorithm),
		ProjectID:    projectID,
		Persistence:  persistence,
		TLSEnabled:   f.tlsEnabled,
		Tags:         f.tag,
		AdminStateUp: f.adminStateUp,
	}
	if f.loadBalancer != "" {
		lbID, rerr := resolveLoadBalancerID(ctx, client, f.loadBalancer)
		if rerr != nil {
			return rerr
		}
		opts.LoadbalancerID = lbID
	}
	if f.listener != "" {
		listenerID, rerr := resolveListenerID(ctx, client, f.listener)
		if rerr != nil {
			return rerr
		}
		opts.ListenerID = listenerID
	}
	p, err := pools.Create(ctx, client, opts).Extract()
	if err != nil {
		return fmt.Errorf("creating pool %q: %w", name, err)
	}
	fields, values := poolFields(p)
	return o.WriteSingle(w, fields, values)
}

// parseSessionPersistence parses the --session-persistence spec into octavia's
// nested object. HTTP_COOKIE and SOURCE_IP take no cookie name; APP_COOKIE
// requires one, which the API enforces but is worth catching before the request.
func parseSessionPersistence(specs []string) (*pools.SessionPersistence, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	var sp pools.SessionPersistence
	for _, spec := range specs {
		kv, err := parseCommaKeyValues(spec, "--session-persistence")
		if err != nil {
			return nil, err
		}
		for k, v := range kv {
			switch k {
			case "type":
				sp.Type = v
			case "cookie_name", "cookie-name":
				sp.CookieName = v
			default:
				return nil, fmt.Errorf("parsing --session-persistence %q: unknown key %q", spec, k)
			}
		}
	}
	if sp.Type == "" {
		return nil, fmt.Errorf("--session-persistence requires type=<SOURCE_IP|HTTP_COOKIE|APP_COOKIE>")
	}
	if sp.Type == "APP_COOKIE" && sp.CookieName == "" {
		return nil, fmt.Errorf("--session-persistence type=APP_COOKIE requires cookie_name=<name>")
	}
	return &sp, nil
}

func newPoolSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &poolWriteFlags{}
	cmd := &cobra.Command{
		Use:   "set <pool>",
		Short: "Update a pool",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			fl := cmd.Flags()
			if fl.Changed("enable") && fl.Changed("disable") {
				return fmt.Errorf("--enable and --disable are mutually exclusive")
			}
			if fl.Changed("enable-tls") && fl.Changed("disable-tls") {
				return fmt.Errorf("--enable-tls and --disable-tls are mutually exclusive")
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
			return runPoolSet(ctx, client, o, args[0], f, changedFlags(fl), cmd.OutOrStdout())
		},
	}
	f.register(cmd, false)
	return cmd
}

func runPoolSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	ref string, f *poolWriteFlags, changed changedSet, w io.Writer,
) error {
	opts := pools.UpdateOpts{AdminStateUp: f.adminStateUp}
	touched := f.adminStateUp != nil

	assignString(changed, "name", f.name, &opts.Name, &touched)
	assignString(changed, "description", f.description, &opts.Description, &touched)
	if changed["lb-algorithm"] {
		// LBMethod is a plain string with omitempty, not a pointer, so it can only
		// be set to a real value — which is all octavia accepts anyway.
		opts.LBMethod = pools.LBMethod(f.lbAlgorithm)
		touched = true
	}
	switch {
	case changed["enable-tls"]:
		assignBool(changed, "enable-tls", true, &opts.TLSEnabled, &touched)
	case changed["disable-tls"]:
		v := false
		opts.TLSEnabled = &v
		touched = true
	}
	if changed["session-persistence"] {
		persistence, err := parseSessionPersistence(f.sessionPersistence)
		if err != nil {
			return err
		}
		opts.Persistence = persistence
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

	id, err := resolvePoolID(ctx, client, ref)
	if err != nil {
		return err
	}
	p, err := pools.Update(ctx, client, id, opts).Extract()
	if err != nil {
		return fmt.Errorf("updating pool %q: %w", ref, err)
	}
	fields, values := poolFields(p)
	return o.WriteSingle(w, fields, values)
}

// --- delete ----------------------------------------------------------------

func newPoolDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <pool> [<pool>...]",
		Short: "Delete one or more pools",
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
			return runPoolDelete(ctx, client, args, cmd.OutOrStdout())
		},
	}
}

func runPoolDelete(ctx context.Context, client *gophercloud.ServiceClient, refs []string, w io.Writer) error {
	return batchdelete.Each(refs, func(ref string) error {
		id, err := resolvePoolID(ctx, client, ref)
		if err != nil {
			return err
		}
		if derr := pools.Delete(ctx, client, id).ExtractErr(); derr != nil {
			return fmt.Errorf("deleting pool %q: %w", ref, derr)
		}
		if _, werr := fmt.Fprintf(w, "Requested deletion of pool %s\n", ref); werr != nil {
			return werr
		}
		return nil
	})
}

package loadbalancer

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/loadbalancer/v2/monitors"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// newHealthMonitorCommand builds "loadbalancer healthmonitor ...".
func newHealthMonitorCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "healthmonitor",
		Short: "Manage load balancer health monitors",
	}
	cmd.AddCommand(
		newHealthMonitorListCommand(a, o),
		newHealthMonitorShowCommand(a, o),
		newHealthMonitorCreateCommand(a, o),
		newHealthMonitorSetCommand(a, o),
		newHealthMonitorDeleteCommand(a, o),
	)
	return cmd
}

func healthMonitorFields(m *monitors.Monitor) ([]string, []any) {
	fields := []string{
		"id", "name", "type", "pools", "delay", "timeout", "max_retries",
		"max_retries_down", "url_path", "http_method", "http_version",
		"expected_codes", "domain_name", "admin_state_up", "project_id",
		"provisioning_status", "operating_status", "tags",
	}
	values := []any{
		m.ID, m.Name, m.Type, m.Pools, m.Delay, m.Timeout, m.MaxRetries,
		m.MaxRetriesDown, m.URLPath, m.HTTPMethod, m.HTTPVersion,
		m.ExpectedCodes, m.DomainName, m.AdminStateUp, m.ProjectID,
		m.ProvisioningStatus, m.OperatingStatus, m.Tags,
	}
	return fields, values
}

func resolveHealthMonitorID(ctx context.Context, client *gophercloud.ServiceClient, ref string) (string, error) {
	return resolveByName("health monitor", ref, func() ([]string, error) {
		pages, err := monitors.List(client, monitors.ListOpts{Name: ref}).AllPages(ctx)
		if err != nil {
			return nil, err
		}
		all, err := monitors.ExtractMonitors(pages)
		if err != nil {
			return nil, err
		}
		ids := make([]string, 0, len(all))
		for _, m := range all {
			ids = append(ids, m.ID)
		}
		return ids, nil
	})
}

// --- list ------------------------------------------------------------------

type healthMonitorListFlags struct {
	name    string
	pool    string
	typ     string
	project string
	enable  bool
	disable bool
	long    bool

	adminStateUp *bool
}

func newHealthMonitorListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &healthMonitorListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List health monitors",
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
			return runHealthMonitorList(ctx, client, o, f, refs.projectID, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "filter by health monitor name")
	fl.StringVar(&f.pool, "pool", "", "list only the monitor of this pool (name or ID)")
	fl.StringVar(&f.typ, "type", "", "filter by type: HTTP, HTTPS, PING, TCP, TLS-HELLO, UDP-CONNECT or SCTP")
	fl.StringVar(&f.project, "project", "", "filter by owning project (name or ID)")
	fl.BoolVar(&f.enable, "enable", false, "list only administratively up health monitors")
	fl.BoolVar(&f.disable, "disable", false, "list only administratively down health monitors")
	fl.BoolVar(&f.long, "long", false, "list additional fields in output")
	return cmd
}

func runHealthMonitorList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	f *healthMonitorListFlags, projectID string, w io.Writer,
) error {
	opts := monitors.ListOpts{
		Name:         f.name,
		Type:         f.typ,
		ProjectID:    projectID,
		AdminStateUp: f.adminStateUp,
	}
	if f.pool != "" {
		poolID, err := resolvePoolID(ctx, client, f.pool)
		if err != nil {
			return err
		}
		opts.PoolID = poolID
	}
	pages, err := monitors.List(client, opts).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing health monitors: %w", err)
	}
	all, err := monitors.ExtractMonitors(pages)
	if err != nil {
		return fmt.Errorf("parsing health monitor list: %w", err)
	}

	cols := []string{"ID", "Name", "Type", "Delay", "Timeout", "Max Retries", "Operating Status"}
	if f.long {
		cols = append(cols, "Provisioning Status", "Admin State Up", "URL Path", "Expected Codes", "Project", "Tags")
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(all))}
	for _, m := range all {
		row := []any{m.ID, m.Name, m.Type, m.Delay, m.Timeout, m.MaxRetries, m.OperatingStatus}
		if f.long {
			row = append(row, m.ProvisioningStatus, m.AdminStateUp, m.URLPath, m.ExpectedCodes, m.ProjectID, m.Tags)
		}
		t.Rows = append(t.Rows, row)
	}
	return o.WriteList(w, t)
}

// --- show ------------------------------------------------------------------

func newHealthMonitorShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <health-monitor>",
		Short: "Show health monitor details",
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
			return runHealthMonitorShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runHealthMonitorShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, w io.Writer) error {
	id, err := resolveHealthMonitorID(ctx, client, ref)
	if err != nil {
		return err
	}
	m, err := monitors.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("showing health monitor %q: %w", ref, err)
	}
	fields, values := healthMonitorFields(m)
	return o.WriteSingle(w, fields, values)
}

// --- create / set ----------------------------------------------------------

type healthMonitorWriteFlags struct {
	name string
	// The pool a monitor belongs to is positional on create and immutable
	// afterwards, so there is no --pool here (only on "healthmonitor list").
	typ            string
	delay          int
	timeout        int
	maxRetries     int
	maxRetriesDown int
	urlPath        string
	httpMethod     string
	httpVersion    string
	expectedCodes  string
	domainName     string
	tag            []string
	noTag          bool
	project        string
	enable         bool
	disable        bool

	adminStateUp *bool
}

func (f *healthMonitorWriteFlags) register(cmd *cobra.Command, isCreate bool) {
	fl := cmd.Flags()
	fl.IntVar(&f.delay, "delay", 0, "seconds between health checks")
	fl.IntVar(&f.timeout, "timeout", 0, "seconds to wait for a check to succeed")
	fl.IntVar(&f.maxRetries, "max-retries", 0, "successes before a member is marked healthy")
	fl.IntVar(&f.maxRetriesDown, "max-retries-down", 0, "failures before a member is marked unhealthy")
	fl.StringVar(&f.urlPath, "url-path", "", "HTTP path to request, e.g. /healthz")
	fl.StringVar(&f.httpMethod, "http-method", "", "HTTP method for the check, e.g. GET")
	fl.StringVar(&f.httpVersion, "http-version", "", "HTTP version for the check, e.g. 1.1")
	fl.StringVar(&f.expectedCodes, "expected-codes", "", "status codes counted as healthy, e.g. 200 or 200-299")
	fl.StringVar(&f.domainName, "domain-name", "", "Host header to send with the check")
	fl.StringArrayVar(&f.tag, "tag", nil, "tag to set (repeatable)")
	fl.BoolVar(&f.enable, "enable", false, "administratively up")
	fl.BoolVar(&f.disable, "disable", false, "administratively down")
	if isCreate {
		fl.StringVar(&f.typ, "type", "",
			"monitor type: HTTP, HTTPS, PING, TCP, TLS-HELLO, UDP-CONNECT or SCTP (required)")
		fl.StringVar(&f.project, "project", "", "owning project (name or ID)")
		return
	}
	fl.StringVar(&f.name, "name", "", "new health monitor name")
	fl.BoolVar(&f.noTag, "no-tag", false, "clear all tags")
}

func newHealthMonitorCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &healthMonitorWriteFlags{}
	cmd := &cobra.Command{
		Use:   "create <pool> <name>",
		Short: "Create a health monitor for a pool",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			fl := cmd.Flags()
			if fl.Changed("enable") && fl.Changed("disable") {
				return fmt.Errorf("--enable and --disable are mutually exclusive")
			}
			f.adminStateUp = triState(fl, f.enable, f.disable)
			// Octavia requires all four; the API rejects a partial set, so say which
			// one is missing here rather than relaying a generic 400.
			for _, req := range []struct {
				name  string
				value int
			}{
				{"--delay", f.delay},
				{"--timeout", f.timeout},
				{"--max-retries", f.maxRetries},
			} {
				if req.value == 0 {
					return fmt.Errorf("%s is required and must be non-zero", req.name)
				}
			}
			if f.typ == "" {
				return fmt.Errorf("--type is required")
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
			return runHealthMonitorCreate(ctx, client, o, args[0], args[1], f, refs.projectID, cmd.OutOrStdout())
		},
	}
	f.register(cmd, true)
	return cmd
}

func runHealthMonitorCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	poolRef, name string, f *healthMonitorWriteFlags, projectID string, w io.Writer,
) error {
	poolID, err := resolvePoolID(ctx, client, poolRef)
	if err != nil {
		return err
	}
	opts := monitors.CreateOpts{
		Name:           name,
		PoolID:         poolID,
		Type:           f.typ,
		Delay:          f.delay,
		Timeout:        f.timeout,
		MaxRetries:     f.maxRetries,
		MaxRetriesDown: f.maxRetriesDown,
		URLPath:        f.urlPath,
		HTTPMethod:     f.httpMethod,
		HTTPVersion:    f.httpVersion,
		ExpectedCodes:  f.expectedCodes,
		DomainName:     f.domainName,
		ProjectID:      projectID,
		Tags:           f.tag,
		AdminStateUp:   f.adminStateUp,
	}
	m, err := monitors.Create(ctx, client, opts).Extract()
	if err != nil {
		return fmt.Errorf("creating health monitor %q for pool %q: %w", name, poolRef, err)
	}
	fields, values := healthMonitorFields(m)
	return o.WriteSingle(w, fields, values)
}

func newHealthMonitorSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &healthMonitorWriteFlags{}
	cmd := &cobra.Command{
		Use:   "set <health-monitor>",
		Short: "Update a health monitor",
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
			return runHealthMonitorSet(ctx, client, o, args[0], f, changedFlags(fl), cmd.OutOrStdout())
		},
	}
	f.register(cmd, false)
	return cmd
}

// runHealthMonitorSet builds a sparse UpdateOpts. Unlike the other octavia nouns,
// monitors.UpdateOpts carries the numeric and some string fields as plain values
// with omitempty rather than pointers, so a deliberate zero cannot be expressed —
// which matches octavia, since delay/timeout/max_retries must all be non-zero.
func runHealthMonitorSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	ref string, f *healthMonitorWriteFlags, changed changedSet, w io.Writer,
) error {
	opts := monitors.UpdateOpts{AdminStateUp: f.adminStateUp}
	touched := f.adminStateUp != nil

	assignString(changed, "name", f.name, &opts.Name, &touched)
	assignString(changed, "domain-name", f.domainName, &opts.DomainName, &touched)
	assignString(changed, "http-version", f.httpVersion, &opts.HTTPVersion, &touched)
	for flag, apply := range map[string]func(){
		"delay":            func() { opts.Delay = f.delay },
		"timeout":          func() { opts.Timeout = f.timeout },
		"max-retries":      func() { opts.MaxRetries = f.maxRetries },
		"max-retries-down": func() { opts.MaxRetriesDown = f.maxRetriesDown },
		"url-path":         func() { opts.URLPath = f.urlPath },
		"http-method":      func() { opts.HTTPMethod = f.httpMethod },
		"expected-codes":   func() { opts.ExpectedCodes = f.expectedCodes },
	} {
		if changed[flag] {
			apply()
			touched = true
		}
	}
	switch {
	case f.noTag:
		opts.Tags = []string{}
		touched = true
	case changed["tag"]:
		opts.Tags = f.tag
		touched = true
	}
	if !touched {
		return fmt.Errorf("nothing to set: pass at least one attribute flag")
	}

	id, err := resolveHealthMonitorID(ctx, client, ref)
	if err != nil {
		return err
	}
	m, err := monitors.Update(ctx, client, id, opts).Extract()
	if err != nil {
		return fmt.Errorf("updating health monitor %q: %w", ref, err)
	}
	fields, values := healthMonitorFields(m)
	return o.WriteSingle(w, fields, values)
}

// --- delete ----------------------------------------------------------------

func newHealthMonitorDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <health-monitor> [<health-monitor>...]",
		Short: "Delete one or more health monitors",
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
			return runHealthMonitorDelete(ctx, client, args, cmd.OutOrStdout())
		},
	}
}

func runHealthMonitorDelete(ctx context.Context, client *gophercloud.ServiceClient, refs []string, w io.Writer) error {
	for _, ref := range refs {
		id, err := resolveHealthMonitorID(ctx, client, ref)
		if err != nil {
			return err
		}
		if derr := monitors.Delete(ctx, client, id).ExtractErr(); derr != nil {
			return fmt.Errorf("deleting health monitor %q: %w", ref, derr)
		}
		if _, werr := fmt.Fprintf(w, "Requested deletion of health monitor %s\n", ref); werr != nil {
			return werr
		}
	}
	return nil
}

package loadbalancer

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/loadbalancer/v2/listeners"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// newListenerCommand builds "loadbalancer listener ...".
func newListenerCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "listener",
		Short: "Manage load balancer listeners",
	}
	cmd.AddCommand(
		newListenerListCommand(a, o),
		newListenerShowCommand(a, o),
		newListenerCreateCommand(a, o),
		newListenerSetCommand(a, o),
		newListenerDeleteCommand(a, o),
	)
	return cmd
}

func listenerFields(l *listeners.Listener) ([]string, []any) {
	fields := []string{
		"id", "name", "description", "loadbalancers", "protocol", "protocol_port",
		"default_pool_id", "connection_limit", "admin_state_up", "project_id",
		"provisioning_status", "operating_status", "default_tls_container_ref",
		"sni_container_refs", "allowed_cidrs", "timeout_client_data",
		"timeout_member_connect", "timeout_member_data", "timeout_tcp_inspect",
		"insert_headers", "tags",
	}
	values := []any{
		l.ID, l.Name, l.Description, l.Loadbalancers, l.Protocol, l.ProtocolPort,
		l.DefaultPoolID, l.ConnLimit, l.AdminStateUp, l.ProjectID,
		l.ProvisioningStatus, l.OperatingStatus, l.DefaultTlsContainerRef,
		l.SniContainerRefs, l.AllowedCIDRs, l.TimeoutClientData,
		l.TimeoutMemberConnect, l.TimeoutMemberData, l.TimeoutTCPInspect,
		l.InsertHeaders, l.Tags,
	}
	return fields, values
}

// resolveListenerID turns a listener name or ID into an ID, rejecting an
// ambiguous name rather than picking arbitrarily.
func resolveListenerID(ctx context.Context, client *gophercloud.ServiceClient, ref string) (string, error) {
	return resolveByName("listener", ref, func() ([]string, error) {
		pages, err := listeners.List(client, listeners.ListOpts{Name: ref}).AllPages(ctx)
		if err != nil {
			return nil, err
		}
		all, err := listeners.ExtractListeners(pages)
		if err != nil {
			return nil, err
		}
		ids := make([]string, 0, len(all))
		for _, l := range all {
			ids = append(ids, l.ID)
		}
		return ids, nil
	})
}

// --- list ------------------------------------------------------------------

type listenerListFlags struct {
	name         string
	loadBalancer string
	protocol     string
	protocolPort int
	project      string
	enable       bool
	disable      bool
	long         bool

	adminStateUp *bool
}

func newListenerListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &listenerListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List listeners",
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
			return runListenerList(ctx, client, o, f, refs.projectID, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "filter by listener name")
	fl.StringVar(&f.loadBalancer, "loadbalancer", "", "list only listeners of this load balancer (name or ID)")
	fl.StringVar(&f.protocol, "protocol", "", "filter by protocol, e.g. HTTP, HTTPS, TCP, TERMINATED_HTTPS")
	fl.IntVar(&f.protocolPort, "protocol-port", 0, "filter by listening port")
	fl.StringVar(&f.project, "project", "", "filter by owning project (name or ID)")
	fl.BoolVar(&f.enable, "enable", false, "list only administratively up listeners")
	fl.BoolVar(&f.disable, "disable", false, "list only administratively down listeners")
	fl.BoolVar(&f.long, "long", false, "list additional fields in output")
	return cmd
}

func runListenerList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	f *listenerListFlags, projectID string, w io.Writer,
) error {
	opts := listeners.ListOpts{
		Name:         f.name,
		Protocol:     f.protocol,
		ProtocolPort: f.protocolPort,
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
	pages, err := listeners.List(client, opts).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing listeners: %w", err)
	}
	all, err := listeners.ExtractListeners(pages)
	if err != nil {
		return fmt.Errorf("parsing listener list: %w", err)
	}

	cols := []string{"ID", "Name", "Protocol", "Protocol Port", "Default Pool ID", "Operating Status"}
	if f.long {
		cols = append(cols, "Provisioning Status", "Admin State Up", "Connection Limit", "Project", "Tags")
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(all))}
	for _, l := range all {
		row := []any{l.ID, l.Name, l.Protocol, l.ProtocolPort, l.DefaultPoolID, l.OperatingStatus}
		if f.long {
			row = append(row, l.ProvisioningStatus, l.AdminStateUp, l.ConnLimit, l.ProjectID, l.Tags)
		}
		t.Rows = append(t.Rows, row)
	}
	return o.WriteList(w, t)
}

// --- show ------------------------------------------------------------------

func newListenerShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <listener>",
		Short: "Show listener details",
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
			return runListenerShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runListenerShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, w io.Writer) error {
	id, err := resolveListenerID(ctx, client, ref)
	if err != nil {
		return err
	}
	l, err := listeners.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("showing listener %q: %w", ref, err)
	}
	fields, values := listenerFields(l)
	return o.WriteSingle(w, fields, values)
}

// --- create ----------------------------------------------------------------

type listenerWriteFlags struct {
	name                 string
	description          string
	loadBalancer         string
	protocol             string
	protocolPort         int
	defaultPool          string
	connectionLimit      int
	defaultTLSContainer  string
	sniContainer         []string
	allowedCIDR          []string
	timeoutClientData    int
	timeoutMemberConnect int
	timeoutMemberData    int
	timeoutTCPInspect    int
	insertHeader         []string
	tag                  []string
	noTag                bool
	project              string
	enable               bool
	disable              bool

	adminStateUp *bool
}

func (f *listenerWriteFlags) register(cmd *cobra.Command, isCreate bool) {
	fl := cmd.Flags()
	fl.StringVar(&f.description, "description", "", "listener description")
	fl.StringVar(&f.defaultPool, "default-pool", "", "default pool for the listener (name or ID)")
	fl.IntVar(&f.connectionLimit, "connection-limit", 0, "maximum concurrent connections; -1 for unlimited")
	fl.StringVar(&f.defaultTLSContainer, "default-tls-container-ref", "", "barbican secret ref for the default TLS certificate")
	fl.StringArrayVar(&f.sniContainer, "sni-container-refs", nil, "barbican secret ref for an SNI certificate (repeatable)")
	fl.StringArrayVar(&f.allowedCIDR, "allowed-cidr", nil, "CIDR allowed to reach the listener (repeatable)")
	fl.IntVar(&f.timeoutClientData, "timeout-client-data", 0, "client inactivity timeout in milliseconds")
	fl.IntVar(&f.timeoutMemberConnect, "timeout-member-connect", 0, "member connect timeout in milliseconds")
	fl.IntVar(&f.timeoutMemberData, "timeout-member-data", 0, "member inactivity timeout in milliseconds")
	fl.IntVar(&f.timeoutTCPInspect, "timeout-tcp-inspect", 0, "TCP inspection timeout in milliseconds")
	fl.StringArrayVar(&f.insertHeader, "insert-headers", nil, "header to insert as name=value (repeatable)")
	fl.StringArrayVar(&f.tag, "tag", nil, "tag to set (repeatable)")
	fl.BoolVar(&f.enable, "enable", false, "administratively up")
	fl.BoolVar(&f.disable, "disable", false, "administratively down")
	if isCreate {
		fl.StringVar(&f.loadBalancer, "loadbalancer", "", "load balancer to attach the listener to (name or ID, required)")
		fl.StringVar(&f.protocol, "protocol", "", "listener protocol: HTTP, HTTPS, TCP, TERMINATED_HTTPS, UDP, SCTP or PROMETHEUS (required)")
		fl.IntVar(&f.protocolPort, "protocol-port", 0, "port to listen on (required)")
		fl.StringVar(&f.project, "project", "", "owning project (name or ID)")
		return
	}
	fl.StringVar(&f.name, "name", "", "new listener name")
	fl.BoolVar(&f.noTag, "no-tag", false, "clear all tags")
}

func newListenerCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &listenerWriteFlags{}
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new listener",
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
			if f.loadBalancer == "" {
				return fmt.Errorf("--loadbalancer is required")
			}
			if f.protocol == "" || f.protocolPort == 0 {
				return fmt.Errorf("--protocol and --protocol-port are required")
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
			return runListenerCreate(ctx, client, o, args[0], f, refs.projectID, cmd.OutOrStdout())
		},
	}
	f.register(cmd, true)
	return cmd
}

func runListenerCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	name string, f *listenerWriteFlags, projectID string, w io.Writer,
) error {
	lbID, err := resolveLoadBalancerID(ctx, client, f.loadBalancer)
	if err != nil {
		return err
	}
	headers, err := parseKeyValues(f.insertHeader, "--insert-headers")
	if err != nil {
		return err
	}
	opts := listeners.CreateOpts{
		Name:                   name,
		Description:            f.description,
		LoadbalancerID:         lbID,
		Protocol:               listeners.Protocol(f.protocol),
		ProtocolPort:           f.protocolPort,
		ProjectID:              projectID,
		DefaultTlsContainerRef: f.defaultTLSContainer,
		SniContainerRefs:       f.sniContainer,
		AllowedCIDRs:           f.allowedCIDR,
		InsertHeaders:          headers,
		Tags:                   f.tag,
		AdminStateUp:           f.adminStateUp,
	}
	if f.defaultPool != "" {
		poolID, perr := resolvePoolID(ctx, client, f.defaultPool)
		if perr != nil {
			return perr
		}
		opts.DefaultPoolID = poolID
	}
	// The timeouts and the connection limit are pointers, so 0 means "not given"
	// rather than "zero milliseconds" — the octavia defaults apply instead.
	setIfNonZero(&opts.ConnLimit, f.connectionLimit)
	setIfNonZero(&opts.TimeoutClientData, f.timeoutClientData)
	setIfNonZero(&opts.TimeoutMemberConnect, f.timeoutMemberConnect)
	setIfNonZero(&opts.TimeoutMemberData, f.timeoutMemberData)
	setIfNonZero(&opts.TimeoutTCPInspect, f.timeoutTCPInspect)

	l, err := listeners.Create(ctx, client, opts).Extract()
	if err != nil {
		return fmt.Errorf("creating listener %q: %w", name, err)
	}
	fields, values := listenerFields(l)
	return o.WriteSingle(w, fields, values)
}

// --- set -------------------------------------------------------------------

func newListenerSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &listenerWriteFlags{}
	cmd := &cobra.Command{
		Use:   "set <listener>",
		Short: "Update a listener",
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
			return runListenerSet(ctx, client, o, args[0], f, changedFlags(fl), cmd.OutOrStdout())
		},
	}
	f.register(cmd, false)
	return cmd
}

func runListenerSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	ref string, f *listenerWriteFlags, changed changedSet, w io.Writer,
) error {
	opts := listeners.UpdateOpts{AdminStateUp: f.adminStateUp}
	touched := f.adminStateUp != nil

	assignString(changed, "name", f.name, &opts.Name, &touched)
	assignString(changed, "description", f.description, &opts.Description, &touched)
	assignString(changed, "default-tls-container-ref", f.defaultTLSContainer, &opts.DefaultTlsContainerRef, &touched)
	assignInt(changed, "connection-limit", f.connectionLimit, &opts.ConnLimit, &touched)
	assignInt(changed, "timeout-client-data", f.timeoutClientData, &opts.TimeoutClientData, &touched)
	assignInt(changed, "timeout-member-connect", f.timeoutMemberConnect, &opts.TimeoutMemberConnect, &touched)
	assignInt(changed, "timeout-member-data", f.timeoutMemberData, &opts.TimeoutMemberData, &touched)
	assignInt(changed, "timeout-tcp-inspect", f.timeoutTCPInspect, &opts.TimeoutTCPInspect, &touched)
	assignStrings(changed, "sni-container-refs", f.sniContainer, &opts.SniContainerRefs, &touched)
	assignStrings(changed, "allowed-cidr", f.allowedCIDR, &opts.AllowedCIDRs, &touched)

	if changed["insert-headers"] {
		headers, err := parseKeyValues(f.insertHeader, "--insert-headers")
		if err != nil {
			return err
		}
		opts.InsertHeaders = &headers
		touched = true
	}
	if changed["default-pool"] {
		poolID, err := resolvePoolID(ctx, client, f.defaultPool)
		if err != nil {
			return err
		}
		opts.DefaultPoolID = &poolID
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

	id, err := resolveListenerID(ctx, client, ref)
	if err != nil {
		return err
	}
	l, err := listeners.Update(ctx, client, id, opts).Extract()
	if err != nil {
		return fmt.Errorf("updating listener %q: %w", ref, err)
	}
	fields, values := listenerFields(l)
	return o.WriteSingle(w, fields, values)
}

// --- delete ----------------------------------------------------------------

func newListenerDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <listener> [<listener>...]",
		Short: "Delete one or more listeners",
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
			return runListenerDelete(ctx, client, args, cmd.OutOrStdout())
		},
	}
}

func runListenerDelete(ctx context.Context, client *gophercloud.ServiceClient, refs []string, w io.Writer) error {
	return batchdelete.Each(refs, func(ref string) error {
		id, err := resolveListenerID(ctx, client, ref)
		if err != nil {
			return err
		}
		if derr := listeners.Delete(ctx, client, id).ExtractErr(); derr != nil {
			return fmt.Errorf("deleting listener %q: %w", ref, derr)
		}
		if _, werr := fmt.Fprintf(w, "Requested deletion of listener %s\n", ref); werr != nil {
			return werr
		}
		return nil
	})
}

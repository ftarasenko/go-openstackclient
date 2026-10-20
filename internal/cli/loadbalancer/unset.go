package loadbalancer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// The unset verbs mirror python-octaviaclient 3.15.0's Unset* commands: each
// boolean flag clears one attribute, --tag removes tags and --all-tag clears
// them. Octavia reverts an attribute to its default when it is PUT as an
// explicit JSON null, which gophercloud's UpdateOpts cannot express — their
// fields are pointers tagged omitempty, so a nil is dropped rather than sent as
// null. So every unset is one raw PUT of {"<envelope>": {"<attr>": null, ...}}
// to the same URL the typed Update uses (the raw fallback per AGENTS.md, as
// "quota unset" already does), and the response decodes into the gophercloud
// type the set verb renders.
//
// Flag surfaces are UNVERIFIED against KeyStack (see the package doc).

// unsetAttr is one upstream unset flag: --<flag> clears the attribute attr.
type unsetAttr struct{ flag, attr, help string }

// unsetSpec describes one noun's unset verb.
type unsetSpec struct {
	kind     string // "load balancer", for messages and help
	envelope string // the JSON key octavia wraps the record in
	attrs    []unsetAttr
	tags     bool // --tag/--all-tag
	wait     bool // --wait/--wait-timeout: the record has a provisioning_status
	columns  []string
	render   func(o *output.Options, w io.Writer, record json.RawMessage) error
}

// unsetRequest is what one invocation asks for, separated from cobra so the
// seam tests construct it directly.
type unsetRequest struct {
	attrs       []string
	removeTags  []string
	allTags     bool
	wait        bool
	waitTimeout time.Duration
}

func (r unsetRequest) empty() bool {
	return len(r.attrs) == 0 && len(r.removeTags) == 0 && !r.allTags
}

// renderRecord decodes an envelope's record into the gophercloud type T and
// writes it through the noun's field builder.
func renderRecord[T any](fields func(*T) ([]string, []any)) func(*output.Options, io.Writer, json.RawMessage) error {
	return func(o *output.Options, w io.Writer, record json.RawMessage) error {
		var v T
		if err := json.Unmarshal(record, &v); err != nil {
			return fmt.Errorf("decoding the updated record: %w", err)
		}
		f, vals := fields(&v)
		return o.WriteSingle(w, f, vals)
	}
}

// unsetTarget is a resolved record: its URL path under the octavia endpoint and
// the reference the operator typed, for messages.
type unsetTarget struct {
	path []string
	ref  string
}

// newUnsetCommand builds "<noun> unset". resolve turns the positional
// arguments into the record's URL path.
func newUnsetCommand(a *auth.Options, o *output.Options, spec *unsetSpec, use string, nargs int,
	resolve func(ctx context.Context, client *gophercloud.ServiceClient, args []string) (unsetTarget, error),
) *cobra.Command {
	clearFlags := make(map[string]*bool, len(spec.attrs))
	var req unsetRequest
	cmd := &cobra.Command{
		Use:   use,
		Short: "Clear " + spec.kind + " settings, reverting them to octavia's defaults",
		Args:  cobra.ExactArgs(nargs),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			req.attrs = nil
			for _, at := range spec.attrs {
				if *clearFlags[at.flag] {
					req.attrs = append(req.attrs, at.attr)
				}
			}
			ctx := cmd.Context()
			client, err := newLoadBalancerClient(ctx, a)
			if err != nil {
				return err
			}
			target, err := resolve(ctx, client, args)
			if err != nil {
				return err
			}
			return runUnset(ctx, client, o, spec, target, req, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	for _, at := range spec.attrs {
		clearFlags[at.flag] = fl.Bool(at.flag, false, at.help)
	}
	if spec.tags {
		fl.StringArrayVar(&req.removeTags, "tag", nil, "tag to remove from the "+spec.kind+" (repeatable)")
		fl.BoolVar(&req.allTags, "all-tag", false, "clear all tags of the "+spec.kind)
	}
	if spec.wait {
		fl.BoolVar(&req.wait, "wait", false, "wait until the "+spec.kind+" is ACTIVE again")
		fl.DurationVar(&req.waitTimeout, flagWaitTimeout, provisioningPollTimeout, helpWaitTimeout)
	}
	return cmd
}

// runUnset PUTs an explicit null for each attribute and the remaining tags,
// optionally waits for the record to settle, and renders it.
func runUnset(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	spec *unsetSpec, target unsetTarget, req unsetRequest, w io.Writer,
) error {
	if err := o.CheckColumns(spec.columns...); err != nil {
		return err
	}
	if req.empty() {
		return fmt.Errorf("nothing to unset: pass at least one attribute flag")
	}
	url := client.ServiceURL(target.path...)
	body := make(map[string]any, len(req.attrs)+1)
	for _, attr := range req.attrs {
		body[attr] = nil
	}
	switch {
	case req.allTags:
		body["tags"] = []string{}
	case len(req.removeTags) > 0:
		// Octavia replaces the tag list wholesale, so removing some means
		// writing back the rest — upstream's set_tags_for_unset does the same.
		current, err := getRecord(ctx, client, url, spec.envelope)
		if err != nil {
			return fmt.Errorf("getting %s %q: %w", spec.kind, target.ref, err)
		}
		var rec struct {
			Tags []string `json:"tags"`
		}
		if err := json.Unmarshal(current, &rec); err != nil {
			return fmt.Errorf("decoding %s %q: %w", spec.kind, target.ref, err)
		}
		kept := make([]string, 0, len(rec.Tags))
		for _, t := range rec.Tags {
			if !slices.Contains(req.removeTags, t) {
				kept = append(kept, t)
			}
		}
		body["tags"] = kept
	}

	var resp map[string]json.RawMessage
	r, err := client.Put(ctx, url, map[string]any{spec.envelope: body}, &resp,
		&gophercloud.RequestOpts{OkCodes: []int{200, 202}})
	if r != nil {
		defer func() { _ = r.Body.Close() }()
	}
	if _, _, err = gophercloud.ParseResponse(r, err); err != nil {
		return fmt.Errorf("unsetting %s %q: %w", spec.kind, target.ref, err)
	}
	record := resp[spec.envelope]
	if req.wait {
		if record, err = waitForRecordActive(ctx, client, url, spec.envelope, req.waitTimeout); err != nil {
			return fmt.Errorf("waiting for %s %q: %w", spec.kind, target.ref, err)
		}
	}
	return spec.render(o, w, record)
}

// getRecord GETs one octavia record and returns its envelope's contents.
func getRecord(ctx context.Context, client *gophercloud.ServiceClient, url, envelope string) (json.RawMessage, error) {
	var resp map[string]json.RawMessage
	r, err := client.Get(ctx, url, &resp, nil)
	if r != nil {
		defer func() { _ = r.Body.Close() }()
	}
	if _, _, err = gophercloud.ParseResponse(r, err); err != nil {
		return nil, err
	}
	rec, ok := resp[envelope]
	if !ok {
		return nil, fmt.Errorf("response has no %q object", envelope)
	}
	return rec, nil
}

// waitForRecordActive polls a child record's own provisioning_status, as
// upstream's --wait does, until ACTIVE; ERROR fails fast. It returns the last
// record read, so the caller renders the settled state.
func waitForRecordActive(ctx context.Context, client *gophercloud.ServiceClient, url, envelope string,
	timeout time.Duration,
) (json.RawMessage, error) {
	if timeout <= 0 {
		timeout = provisioningPollTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(provisioningPollInterval)
	defer ticker.Stop()

	var getErrors int
	for {
		rec, err := getRecord(ctx, client, url, envelope)
		if err == nil {
			getErrors = 0
			var st struct {
				Status string `json:"provisioning_status"`
			}
			if jerr := json.Unmarshal(rec, &st); jerr != nil {
				return nil, jerr
			}
			switch st.Status {
			case statusActive:
				return rec, nil
			case statusError:
				return nil, fmt.Errorf("entered %s", statusError)
			}
		} else {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			getErrors++
			if getErrors > maxConsecutiveGetErrors {
				return nil, err
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

// --- the eight nouns ----------------------------------------------------------

func clearHelp(what string) string { return "clear the " + what }

var lbUnsetSpec = &unsetSpec{
	kind: "load balancer", envelope: "loadbalancer", tags: true, wait: true,
	attrs: []unsetAttr{
		{"name", "name", clearHelp("load balancer name")},
		{"description", "description", clearHelp("load balancer description")},
		{flagVIPQoSPolicyID, "vip_qos_policy_id", clearHelp("VIP's QoS policy")},
		{"vip-sg-id", "vip_sg_ids", clearHelp("VIP's custom security groups (octavia 2025.1 and later)")},
	},
	columns: lbColumns,
	render:  renderRecord(lbFields),
}

var listenerUnsetSpec = &unsetSpec{
	kind: "listener", envelope: "listener", tags: true, wait: true,
	attrs: []unsetAttr{
		{"name", "name", clearHelp("listener name")},
		{"description", "description", clearHelp("listener description")},
		{"connection-limit", "connection_limit", "reset the connection limit to unlimited"},
		{"default-pool", "default_pool_id", clearHelp("default pool")},
		{"default-tls-container-ref", "default_tls_container_ref", clearHelp("default TLS certificate")},
		{"sni-container-refs", "sni_container_refs", clearHelp("SNI certificates")},
		{"insert-headers", "insert_headers", clearHelp("inserted headers")},
		{"timeout-client-data", "timeout_client_data", "reset the client inactivity timeout to the default"},
		{"timeout-member-connect", "timeout_member_connect", "reset the member connect timeout to the default"},
		{"timeout-member-data", "timeout_member_data", "reset the member inactivity timeout to the default"},
		{"timeout-tcp-inspect", "timeout_tcp_inspect", "reset the TCP inspection timeout to the default"},
		{"client-ca-tls-container-ref", "client_ca_tls_container_ref", clearHelp("client CA certificate")},
		{"client-authentication", "client_authentication", "reset client authentication to NONE"},
		{"client-crl-container-ref", "client_crl_container_ref", clearHelp("client CA revocation list")},
		{"allowed-cidrs", "allowed_cidrs", clearHelp("allowed CIDRs")},
		{"tls-versions", "tls_versions", "reset the TLS versions to the default"},
		{"tls-ciphers", "tls_ciphers", "reset the TLS ciphers to the default"},
		{"alpn-protocols", "alpn_protocols", "reset the ALPN protocols to the default"},
		{"hsts-max-age", "hsts_max_age", "disable HSTS (octavia 2023.2 and later)"},
		{"hsts-include-subdomains", "hsts_include_subdomains", "clear the HSTS includeSubDomains directive (octavia 2023.2 and later)"},
		{"hsts-preload", "hsts_preload", "clear the HSTS preload directive (octavia 2023.2 and later)"},
	},
	columns: listenerColumns,
	render:  renderRecord(listenerFields),
}

var poolUnsetSpec = &unsetSpec{
	kind: "pool", envelope: "pool", tags: true, wait: true,
	attrs: []unsetAttr{
		{"name", "name", clearHelp("pool name")},
		{"description", "description", clearHelp("pool description")},
		{"ca-tls-container-ref", "ca_tls_container_ref", clearHelp("CA certificate for member TLS")},
		{"crl-container-ref", "crl_container_ref", clearHelp("revocation list for member TLS")},
		{"session-persistence", "session_persistence", "disable session persistence"},
		{"tls-container-ref", "tls_container_ref", clearHelp("client certificate for member TLS")},
		{"tls-versions", "tls_versions", "reset the TLS versions to the default"},
		{"tls-ciphers", "tls_ciphers", "reset the TLS ciphers to the default"},
		{"alpn-protocols", "alpn_protocols", "reset the ALPN protocols to the default"},
	},
	columns: poolColumns,
	render:  renderRecord(poolFields),
}

var memberUnsetSpec = &unsetSpec{
	kind: "member", envelope: "member", tags: true, wait: true,
	attrs: []unsetAttr{
		{"backup", "backup", "stop treating the member as a backup"},
		{"monitor-address", "monitor_address", clearHelp("alternate health-check address")},
		{"monitor-port", "monitor_port", clearHelp("alternate health-check port")},
		{"name", "name", clearHelp("member name")},
		{"weight", "weight", "reset the weight to the default (1)"},
	},
	columns: memberColumns,
	render:  renderRecord(memberFields),
}

var healthMonitorUnsetSpec = &unsetSpec{
	kind: "health monitor", envelope: "healthmonitor", tags: true, wait: true,
	attrs: []unsetAttr{
		{"domain-name", "domain_name", clearHelp("Host header sent with the check")},
		{"expected-codes", "expected_codes", "reset the expected codes to the default (200)"},
		{"http-method", "http_method", "reset the HTTP method to the default (GET)"},
		{"http-version", "http_version", "reset the HTTP version to the default"},
		{"max-retries-down", "max_retries_down", "reset max-retries-down to the default (3)"},
		{"name", "name", clearHelp("health monitor name")},
		{"url-path", "url_path", "reset the URL path to the default (/)"},
	},
	columns: healthMonitorColumns,
	render:  renderRecord(healthMonitorFields),
}

var l7PolicyUnsetSpec = &unsetSpec{
	kind: "l7policy", envelope: "l7policy", tags: true, wait: true,
	attrs: []unsetAttr{
		{"description", "description", clearHelp("policy description")},
		{"name", "name", clearHelp("policy name")},
		{"redirect-http-code", "redirect_http_code", "reset the redirect HTTP code to the default"},
	},
	columns: l7PolicyColumns,
	render:  renderRecord(l7PolicyFields),
}

var l7RuleUnsetSpec = &unsetSpec{
	kind: "l7rule", envelope: "rule", tags: true, wait: true,
	attrs: []unsetAttr{
		{flagInvert, "invert", "reset invert to the default (false)"},
		{"key", "key", clearHelp("rule key")},
	},
	columns: l7RuleColumns,
	render:  renderRecord(l7RuleFields),
}

var flavorUnsetSpec = &unsetSpec{
	kind: "flavor", envelope: "flavor",
	attrs: []unsetAttr{
		{"description", "description", clearHelp("flavor description")},
	},
	columns: flavorColumns,
	render:  renderRecord(flavorFields),
}

// byRef resolves a single positional reference into a top-level record's path.
func byRef(collection string, resolve func(context.Context, *gophercloud.ServiceClient, string) (string, error)) func(context.Context, *gophercloud.ServiceClient, []string) (unsetTarget, error) {
	return func(ctx context.Context, client *gophercloud.ServiceClient, args []string) (unsetTarget, error) {
		id, err := resolve(ctx, client, args[0])
		if err != nil {
			return unsetTarget{}, err
		}
		return unsetTarget{path: []string{"lbaas", collection, id}, ref: args[0]}, nil
	}
}

func newLBUnsetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return newUnsetCommand(a, o, lbUnsetSpec, "unset <load-balancer>", 1, byRef("loadbalancers", resolveLoadBalancerID))
}

func newListenerUnsetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return newUnsetCommand(a, o, listenerUnsetSpec, "unset <listener>", 1, byRef("listeners", resolveListenerID))
}

func newPoolUnsetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return newUnsetCommand(a, o, poolUnsetSpec, "unset <pool>", 1, byRef("pools", resolvePoolID))
}

func newHealthMonitorUnsetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return newUnsetCommand(a, o, healthMonitorUnsetSpec, "unset <health-monitor>", 1, byRef("healthmonitors", resolveHealthMonitorID))
}

func newL7PolicyUnsetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return newUnsetCommand(a, o, l7PolicyUnsetSpec, "unset <l7policy>", 1, byRef("l7policies", resolveL7PolicyID))
}

func newFlavorUnsetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return newUnsetCommand(a, o, flavorUnsetSpec, "unset <flavor>", 1, byRef("flavors", resolveFlavorID))
}

func newMemberUnsetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return newUnsetCommand(a, o, memberUnsetSpec, "unset <pool> <member>", 2,
		func(ctx context.Context, client *gophercloud.ServiceClient, args []string) (unsetTarget, error) {
			poolID, err := resolvePoolID(ctx, client, args[0])
			if err != nil {
				return unsetTarget{}, err
			}
			id, err := resolveMemberID(ctx, client, poolID, args[1])
			if err != nil {
				return unsetTarget{}, err
			}
			return unsetTarget{path: []string{"lbaas", "pools", poolID, "members", id}, ref: args[1]}, nil
		})
}

// l7rule unset takes the rule's ID: rules have no name, as upstream.
func newL7RuleUnsetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return newUnsetCommand(a, o, l7RuleUnsetSpec, "unset <l7policy> <l7rule>", 2,
		func(ctx context.Context, client *gophercloud.ServiceClient, args []string) (unsetTarget, error) {
			policyID, err := resolveL7PolicyID(ctx, client, args[0])
			if err != nil {
				return unsetTarget{}, err
			}
			return unsetTarget{path: []string{"lbaas", "l7policies", policyID, "rules", args[1]}, ref: args[1]}, nil
		})
}
